package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/causent/malenia/internal/cache"
	"github.com/causent/malenia/internal/domain"
	projectenv "github.com/causent/malenia/internal/environment"
	"github.com/causent/malenia/internal/history"
	"github.com/causent/malenia/internal/investigation"
	"github.com/causent/malenia/internal/localization"
	"github.com/causent/malenia/internal/observability"
	"github.com/causent/malenia/internal/reasoning"
	"github.com/causent/malenia/internal/relationships"
	programrepo "github.com/causent/malenia/internal/repository"
)

// persistArchitecture runs the staged repository-intelligence and diagnosis path.
// Stages publish a preliminary finding as soon as candidates and hypotheses exist,
// then optionally continue into adaptive deep investigation.
func (d *Diagnoser) persistArchitecture(ctx context.Context, report *Report, workspace string, baseline Baseline, env Environment, loc Localization, evidenceOnly bool) error {
	if d.metrics == nil {
		d.metrics = observability.NewMetrics(report.InvestigationID)
	}
	if d.cfg.Store == nil {
		return d.runStagedIntelligence(ctx, report, workspace, baseline, env, loc, false, evidenceOnly)
	}
	return d.runStagedIntelligence(ctx, report, workspace, baseline, env, loc, true, evidenceOnly)
}

func (d *Diagnoser) runStagedIntelligence(ctx context.Context, report *Report, workspace string, baseline Baseline, env Environment, loc Localization, persist, evidenceOnly bool) error {
	repoID := "local:" + d.cfg.RepoPath
	revisionID := "revision:" + env.HeadCommit
	provider := d.cfg.CIProvider
	if provider == "" {
		provider = "local"
	}
	externalExecutionID := d.cfg.ExternalExecutionID
	if externalExecutionID == "" {
		externalExecutionID = report.InvestigationID
	}
	pullRequestID := ""
	if persist {
		if err := d.cfg.Store.SaveRepository(domain.Repository{ID: repoID, Provider: provider, CanonicalID: d.cfg.RepoPath, Name: d.cfg.RepoPath}); err != nil {
			return err
		}
		if err := d.cfg.Store.SaveRevision(domain.Revision{ID: revisionID, RepositoryID: repoID, CommitSHA: env.HeadCommit, CreatedAt: report.CreatedAt}); err != nil {
			return err
		}
		if d.cfg.PullRequestID != "" {
			pullRequestID = "pr:" + d.cfg.PullRequestID
			baseRevisionID := ""
			if d.cfg.BaseRef != "" {
				if base := stringsTrim(gitOutput(d.cfg.RepoPath, "rev-parse", d.cfg.BaseRef)); base != "" {
					baseRevisionID = "revision:" + base
					_ = d.cfg.Store.SaveRevision(domain.Revision{ID: baseRevisionID, RepositoryID: repoID, CommitSHA: base, CreatedAt: report.CreatedAt})
				}
			}
			if err := d.cfg.Store.SavePullRequest(domain.PullRequest{ID: pullRequestID, RepositoryID: repoID, ExternalPRID: d.cfg.PullRequestID, BaseRevisionID: baseRevisionID, HeadRevisionID: revisionID, Status: "open", Metadata: map[string]string{"source_url": d.cfg.SourceURL}}); err != nil {
				return err
			}
		}
		if err := d.persistReportEvidence(report); err != nil {
			return err
		}
	}

	var artifactCache *cache.FSCache
	var err error
	if !evidenceOnly {
		artifactCache, err = d.openArtifactCache()
		if err != nil {
			report.Uncertainty = append(report.Uncertainty, "artifact cache unavailable: "+err.Error())
		}

		overview, overviewErr := d.stageOverview(ctx, report, workspace, repoID, revisionID, env.HeadCommit, artifactCache)
		if overviewErr != nil {
			return overviewErr
		}
		report.Overview = &overview
		capabilities, capabilityErr := projectenv.NewRegistry().Capabilities(workspace)
		if capabilityErr != nil {
			report.Uncertainty = append(report.Uncertainty, "environment capability detection incomplete: "+capabilityErr.Error())
		} else {
			report.AdapterCapabilities = capabilities
		}
	} else {
		overviewStage := d.metrics.Begin(observability.StageRepositoryOverview)
		overviewStage.Meta("mode", "skipped")
		overviewStage.Meta("reason", "evidence_only")
		overviewStage.Done("skipped", nil)
		report.IndexingMode = "skipped_evidence_only"
		report.Uncertainty = append(report.Uncertainty, "repository overview and indexing were deferred because source-level analysis was not justified")
	}

	executionID := "execution:" + report.InvestigationID
	if persist {
		if err := d.cfg.Store.SaveCIExecution(domain.CIExecution{ID: executionID, RepositoryID: repoID, PullRequestID: pullRequestID, HeadRevisionID: revisionID, Provider: provider, ExternalExecutionID: externalExecutionID, Status: string(report.Classification), StartedAt: &baseline.StartedAt, CompletedAt: &baseline.FinishedAt, RawMetadata: map[string]string{"source_url": d.cfg.SourceURL}}); err != nil {
			return err
		}
	}

	stage := d.metrics.Begin(observability.StageFailureIngestion)
	failures, err := d.persistFailureEpisodes(report, executionID, baseline)
	if err != nil {
		stage.Done("failed", err)
		return err
	}
	report.FailureEpisodes = failures
	var runtimeGraph RuntimeEvidenceGraph
	runtimeGraphSet := false
	for _, item := range report.Evidence {
		if !isCIEvidenceKind(item.Kind) {
			continue
		}
		traces := parseRaceReports(item.Detail)
		for _, race := range traces {
			race.EvidenceIDs = []string{item.ID}
			race.BuildID = item.BuildID
			race.SourceRevision = item.SourceRevision
			if race.SourceRevision == "" {
				race.SourceRevision = env.HeadCommit
			}
			report.RaceReports = append(report.RaceReports, race)
		}
		if len(traces) > 0 {
			storageRef := item.Provenance
			if storageRef == "" {
				storageRef = item.Source
			}
			sourceRevision := item.SourceRevision
			if sourceRevision == "" {
				sourceRevision = env.HeadCommit
			}
			workflowRunID := item.WorkflowRunID
			if workflowRunID == "" {
				workflowRunID = externalExecutionID
			}
			artifactID := item.ArtifactID
			if artifactID == "" {
				artifactID = item.ID
			}
			artifactDigest := item.ArtifactDigest
			if artifactDigest == "" {
				artifactDigest = item.Digest
			}
			part := IngestRuntimeRaceReportsWithProvenance(traces, ExecutionIdentity{WorkflowRunID: workflowRunID, ReportID: item.ID, BuildID: item.BuildID, ExecutionID: item.ExecutionID, SourceRevision: sourceRevision, TestAttemptID: item.TestAttemptID}, RuntimeArtifactProvenance{ArtifactID: artifactID, Digest: artifactDigest, StorageRef: storageRef, ParserName: "go-race-report-parser", ParserVersion: "p2.1"})
			if !runtimeGraphSet {
				runtimeGraph = part
				runtimeGraphSet = true
			} else {
				runtimeGraph.Races = append(runtimeGraph.Races, part.Races...)
				runtimeGraph.Accesses = append(runtimeGraph.Accesses, part.Accesses...)
				runtimeGraph.Overlaps = append(runtimeGraph.Overlaps, part.Overlaps...)
			}
		}
	}
	for i := range report.RaceReports {
		ResolveRaceReportInstructions(&report.RaceReports[i], report.Evidence)
	}
	if runtimeGraphSet {
		report.RuntimeEvidence = &runtimeGraph
		runtimeItems := RuntimeEvidenceItems(runtimeGraph, "unavailable")
		report.Evidence = append(report.Evidence, runtimeItems...)
		if persist {
			for _, item := range runtimeItems {
				if err := saveEvidenceItem(d.cfg.Store, report, item); err != nil {
					return err
				}
			}
		}
	}
	report.FailureIncidents = buildFailureIncidents(report)
	stage.Count("episodes", len(failures))
	stage.Done("completed", nil)

	var snapshot programrepo.Snapshot
	if !evidenceOnly {
		var indexMode string
		snapshot, indexMode, err = d.stageIndex(ctx, report, workspace, repoID, revisionID, env.HeadCommit, loc, artifactCache)
		if err != nil {
			return err
		}
		d.snapshot = &snapshot
		report.IndexingMode = indexMode
	} else {
		indexStage := d.metrics.Begin(observability.StageIndexing)
		indexStage.Meta("mode", "skipped")
		indexStage.Meta("reason", "evidence_only")
		indexStage.Done("skipped", nil)
	}
	if persist {
		persistStage := d.metrics.Begin(observability.StagePersistence)
		persistStage.Meta("mode", "batched_snapshot")
		if err := d.cfg.Store.SaveProgramNodes(snapshot.Nodes); err != nil {
			persistStage.Done("failed", err)
			return err
		}
		if err := d.cfg.Store.SaveRelationships(snapshot.Relationships); err != nil {
			persistStage.Done("failed", err)
			return err
		}
		persistStage.Count("nodes", len(snapshot.Nodes))
		persistStage.Count("relationships", len(snapshot.Relationships))
		persistStage.Done("completed", nil)
	}
	targetedEvidence := report.NormalizedEvidence != nil
	if !evidenceOnly && d.cfg.BaseRef != "" && !targetedEvidence {
		deltas, deltaEvidence, deltaErr := d.stageDeltas(ctx, workspace, repoID, revisionID, snapshot)
		if deltaErr != nil {
			report.Uncertainty = append(report.Uncertainty, "revision delta analysis incomplete: "+deltaErr.Error())
			report.Partial = true
		} else if len(deltas) > 0 {
			report.Deltas = deltas
			report.Evidence = append(report.Evidence, deltaEvidence)
			if persist {
				if err := d.cfg.Store.SaveEvidence(domain.Evidence{ID: domain.ID(deltaEvidence.ID), InvestigationID: domain.ID(report.InvestigationID), Type: domain.EvidenceType(deltaEvidence.Kind), Source: deltaEvidence.Source, Timestamp: report.CreatedAt, ContentRef: deltaEvidence.Kind, Digest: deltaEvidence.Digest, Payload: deltaEvidence.Detail, Provenance: deltaEvidence.Provenance}); err != nil {
					return err
				}
				for _, delta := range deltas {
					if err := d.cfg.Store.SaveCodeDelta(delta); err != nil {
						return err
					}
				}
			}
		}
	}

	frames := architectureFrames(baseline, failures, report.Evidence)
	frameEvidence := observedFrameEvidence(frames)
	report.Evidence = append(report.Evidence, frameEvidence...)
	for i := range frames {
		if i < len(frameEvidence) {
			frames[i].EvidenceIDs = []domain.ID{domain.ID(frameEvidence[i].ID)}
		}
	}
	var evidenceIDs []domain.ID
	var changeEvidenceIDs []domain.ID
	var behaviouralEvidenceIDs []domain.ID
	for _, e := range report.Evidence {
		evidenceIDs = append(evidenceIDs, domain.ID(e.ID))
		if e.Kind == "git_diff" || e.Kind == "pr_delta" {
			changeEvidenceIDs = append(changeEvidenceIDs, domain.ID(e.ID))
		}
		if isCIEvidenceKind(e.Kind) || e.Kind == "ci_artifact_observation" {
			behaviouralEvidenceIDs = append(behaviouralEvidenceIDs, domain.ID(e.ID))
		}
	}
	failureIDs := make([]string, 0, len(failures))
	for _, failure := range failures {
		failureIDs = append(failureIDs, failure.ID)
	}
	changed := map[string]bool{}
	for _, path := range loc.LikelyFiles {
		for i := range snapshot.Nodes {
			if snapshot.Nodes[i].FilePath == path {
				snapshot.Nodes[i].Changed = true
				changed[snapshot.Nodes[i].Identity] = true
			}
		}
	}
	for _, delta := range report.Deltas {
		changed[delta.SymbolIdentity] = true
	}

	candStage := d.metrics.Begin(observability.StageCandidateGeneration)
	var candidates []domain.CandidateLocation
	var relations []domain.Relationship
	maxCandidates := 100
	if report.NormalizedEvidence != nil || report.Baseline.Source == "ci_artifact" {
		maxCandidates = 25
	}
	input := localization.Input{InvestigationID: domain.ID(report.InvestigationID), RepositoryID: repoID, RevisionID: revisionID, FailureIDs: failureIDs, EvidenceIDs: evidenceIDs, BehaviouralEvidenceIDs: behaviouralEvidenceIDs, ChangeEvidenceIDs: changeEvidenceIDs, StackFrames: frames, FailureText: failureText(baseline, report.Evidence), ChangedIdentities: changed, MaxDepth: 2, MaxCandidates: maxCandidates}
	if evidenceOnly {
		candidates, relations = localization.GenerateObserved(frames, input)
	} else {
		candidates, relations = localization.Generate(snapshot, input)
	}
	if !evidenceOnly {
		var sourceEvidence []EvidenceItem
		candidates, sourceEvidence = sourceEvidenceForCandidates(workspace, env.HeadCommit, candidates)
		report.Evidence = append(report.Evidence, sourceEvidence...)
		if persist {
			for _, item := range sourceEvidence {
				if err := d.cfg.Store.SaveEvidence(domain.Evidence{ID: domain.ID(item.ID), InvestigationID: domain.ID(report.InvestigationID), Type: domain.EvidenceType(item.Kind), Source: item.Source, Timestamp: report.CreatedAt, ContentRef: item.Kind, Digest: item.Digest, Payload: item.Detail, Provenance: item.Provenance}); err != nil {
					return err
				}
			}
		}
	}
	for _, candidate := range candidates {
		for _, delta := range report.Deltas {
			if candidate.Location.SymbolIdentity != delta.SymbolIdentity && candidate.Location.FilePath != delta.FilePath {
				continue
			}
			relations = append(relations, domain.Relationship{
				ID: "candidate:" + candidate.ID + ":delta:" + delta.ID, InvestigationID: domain.ID(report.InvestigationID),
				SourceType: "candidate_location", SourceID: candidate.ID, TargetType: "code_delta", TargetID: delta.ID,
				Type: "CHANGED_BY_PR", RevisionID: delta.HeadRevisionID, EvidenceIDs: []domain.ID{delta.EvidenceID},
			})
		}
	}
	paths, pathEvidence := buildExecutionPaths(domain.ID(report.InvestigationID), snapshot, candidates, failureText(baseline, report.Evidence), failureIDs, evidenceIDs)
	report.ExecutionPaths = paths
	report.Evidence = append(report.Evidence, pathEvidence...)
	if persist {
		for _, path := range paths {
			if err := d.cfg.Store.SaveExecutionPath(path); err != nil {
				return err
			}
		}
		for _, item := range pathEvidence {
			if err := d.cfg.Store.SaveEvidence(domain.Evidence{ID: domain.ID(item.ID), InvestigationID: domain.ID(report.InvestigationID), Type: domain.EvidenceType(item.Kind), Source: item.Source, Timestamp: report.CreatedAt, ContentRef: item.Kind, Digest: item.Digest, Payload: item.Detail, Provenance: item.Provenance}); err != nil {
				return err
			}
		}
	}
	report.Candidates = candidates
	if !evidenceOnly && len(report.FailureIncidents) > 0 {
		if err := d.runSourceInvestigation(ctx, report, workspace, snapshot, candidates, persist); err != nil {
			report.Uncertainty = append(report.Uncertainty, "source investigation incomplete: "+err.Error())
			report.Partial = true
		}
		// Build a generic, evidence-cited causal chain for deterministic (non-race)
		// failures from the same source evidence. This is a separate path from the
		// race-oriented investigation and never claims a causal conclusion itself.
		buildDeterministicCausalChain(report, workspace, snapshot, candidates)
	}
	refineEvidenceLikelyFiles(report)
	report.Relationships = relations
	candStage.Count("candidates", len(candidates))
	candStage.Done("completed", nil)
	if persist {
		for _, c := range candidates {
			if err := d.cfg.Store.SaveCodeLocation(c.Location); err != nil {
				return err
			}
			if err := d.cfg.Store.SaveCandidate(c); err != nil {
				return err
			}
		}
		if err := d.cfg.Store.SaveRelationships(relations); err != nil {
			return err
		}
	}

	groupStage := d.metrics.Begin(observability.StageFailureGrouping)
	discovery := relationships.Discover(domain.ID(report.InvestigationID), failures, candidates, relations)
	report.FailureGroups = discovery.Groups
	groupStage.Count("groups", len(discovery.Groups))
	groupStage.Done("completed", nil)
	if persist {
		for _, group := range discovery.Groups {
			if err := d.cfg.Store.SaveFailureGroup(group); err != nil {
				return err
			}
		}
		for _, rel := range discovery.Relationships {
			if err := d.cfg.Store.SaveRelationship(rel); err != nil {
				return err
			}
			report.Relationships = append(report.Relationships, rel)
		}
	} else {
		report.Relationships = append(report.Relationships, discovery.Relationships...)
	}

	if persist {
		if !evidenceOnly {
			historical, histErr := history.Discover(d.cfg.Store, history.Input{InvestigationID: domain.ID(report.InvestigationID), RepositoryID: repoID, RevisionID: revisionID, Failures: failures, Candidates: candidates, EvidenceIDs: evidenceIDs})
			if histErr != nil {
				return histErr
			}
			report.HistoricalContext = historical.References
			if len(historical.Evidence) > 0 {
				for _, evidence := range historical.Evidence {
					if err := d.cfg.Store.SaveEvidence(evidence); err != nil {
						return err
					}
					report.Evidence = append(report.Evidence, evidenceItemFromDomain(evidence))
				}
				report.Candidates = localization.ApplyHistoricalEvidence(report.Candidates, evidenceIDsOfHistorical(historical.Evidence))
				for _, candidate := range report.Candidates {
					if err := d.cfg.Store.SaveCandidate(candidate); err != nil {
						return err
					}
				}
				candidates = report.Candidates
			}
			for _, rel := range historical.Relationships {
				if err := d.cfg.Store.SaveRelationship(rel); err != nil {
					return err
				}
				report.Relationships = append(report.Relationships, rel)
			}
			for _, stageName := range []string{"AnalyzeRepository", "GenerateCandidates", "DiscoverRelationships"} {
				activity := investigation.NewActivity(domain.ID(report.InvestigationID), "activity:"+report.InvestigationID+":"+stageName, stageName, nil, evidenceIDsToStrings(evidenceIDs))
				activity = investigation.Complete(activity, []string{"report:" + report.InvestigationID + ":" + stageName})
				if err := d.cfg.Store.SaveActivity(activity); err != nil {
					return err
				}
			}
		}
	}

	hypStage := d.metrics.Begin(observability.StageHypothesisGeneration)
	hypothesisCandidates := candidates
	if report.NormalizedEvidence != nil || report.Baseline.Source == "ci_artifact" {
		hypothesisCandidates = make([]domain.CandidateLocation, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.Score.ExecutionRelevance > 0 || candidate.Score.BehaviouralRelevance > 0 {
				hypothesisCandidates = append(hypothesisCandidates, candidate)
			}
		}
	}
	if hasRaceIncident(report.FailureIncidents) {
		// Candidate locations remain localization evidence. Race investigations
		// receive mechanism hypotheses with predictions; a candidate function is
		// never promoted to a causal hypothesis by itself.
		report.Hypotheses = raceHypotheses(report)
	} else {
		report.Hypotheses = reasoning.Generate(domain.ID(report.InvestigationID), discovery.Groups, hypothesisCandidates)
	}
	evidenceMap := map[domain.ID]domain.Evidence{}
	for _, item := range report.Evidence {
		evidenceMap[domain.ID(item.ID)] = domain.Evidence{ID: domain.ID(item.ID), InvestigationID: domain.ID(report.InvestigationID), Type: domain.EvidenceType(item.Kind)}
	}
	candidateMap := map[string]domain.CandidateLocation{}
	for _, candidate := range candidates {
		candidateMap[candidate.ID] = candidate
	}
	hypothesisMap := map[domain.ID]domain.Hypothesis{}
	for index, hypothesis := range report.Hypotheses {
		assessed := reasoning.Assess(hypothesis, evidenceMap, nil, nil).Hypothesis
		report.Hypotheses[index] = assessed
		hypothesisMap[assessed.ID] = assessed
		if persist {
			if err := d.cfg.Store.SaveHypothesis(assessed); err != nil {
				return err
			}
			if err := d.cfg.Store.SaveHypothesisHistory(domain.HypothesisHistory{ID: domain.NewID(), HypothesisID: assessed.ID, InvestigationID: assessed.InvestigationID, PreviousStatus: domain.HypProposed, NextStatus: assessed.Status, Evaluation: "assessment from persisted evidence", CreatedAt: time.Now().UTC()}); err != nil {
				return err
			}
		}
	}
	hypStage.Count("hypotheses", len(report.Hypotheses))
	hypStage.Done("completed", nil)

	revisions := map[string]bool{revisionID: true}
	for _, candidate := range candidates {
		revisions[candidate.Location.RevisionID] = true
	}
	if len(report.FailureIncidents) > 0 {
		for _, incident := range report.FailureIncidents {
			candidateIDs := make([]string, 0, minInt(len(candidates), 5))
			for _, candidate := range candidates {
				candidateIDs = append(candidateIDs, candidate.ID)
				if len(candidateIDs) == 5 {
					break
				}
			}
			finding := domain.DiagnosticFinding{
				ID:              "finding:" + incident.ID,
				InvestigationID: domain.ID(report.InvestigationID),
				Kind:            "incident_diagnosis",
				Statement:       incident.ObservedExplanation,
				EvidenceIDs:     stringIDsToDomain(incident.EvidenceIDs),
				CandidateIDs:    candidateIDs,
				Status:          "observed",
				Uncertainty:     append([]string{}, incident.Uncertainty...),
				CreatedAt:       time.Now().UTC(),
			}
			report.Findings = append(report.Findings, finding)
			if persist {
				if err := d.cfg.Store.SaveFinding(finding); err != nil {
					return err
				}
			}
		}
	} else {
		for _, hypothesis := range report.Hypotheses {
			finding := domain.DiagnosticFinding{ID: "finding:" + string(hypothesis.ID), InvestigationID: hypothesis.InvestigationID, Kind: "hypothesis_assessment", Statement: hypothesis.Statement, HypothesisID: hypothesis.ID, EvidenceIDs: hypothesis.SupportingEvidenceIDs, CandidateIDs: hypothesis.CandidateLocationIDs, Status: string(hypothesis.Status), Uncertainty: hypothesis.MissingEvidence, CreatedAt: time.Now().UTC()}
			validation := reasoning.ValidateFinding(finding, domain.ID(report.InvestigationID), evidenceMap, hypothesisMap, candidateMap, revisions)
			if !validation.Valid {
				finding.Status = "unresolved"
				finding.Uncertainty = append(finding.Uncertainty, validation.Reasons...)
			}
			report.Findings = append(report.Findings, finding)
			if persist {
				if err := d.cfg.Store.SaveFinding(finding); err != nil {
					return err
				}
				if err := d.cfg.Store.SaveClaimValidation(domain.ClaimValidation{ClaimID: finding.ID, Valid: validation.Valid, Reasons: validation.Reasons, ValidatedAt: validation.ValidatedAt}); err != nil {
					return err
				}
			}
		}
	}
	if len(candidates) > 0 && len(report.FailureIncidents) == 0 {
		statement := fmt.Sprintf("Candidate locations were ranked from execution and repository evidence; top candidate is %s", candidates[0].Location.SymbolIdentity)
		kind := "candidate_localization"
		status := "potential"
		uncertainty := []string{"candidate ranking is not a causal proof"}
		if evidenceOnly {
			statement = fmt.Sprintf("An observed location was extracted from execution output: %s. Its relationship to the underlying failure has not been verified against source code.", candidates[0].Location.FilePath)
			kind = "observed_location"
			status = "observed"
			uncertainty = []string{"source code was not inspected", "the observed location is not a verified fault site"}
		}
		finding := domain.DiagnosticFinding{ID: "finding:" + report.InvestigationID, InvestigationID: domain.ID(report.InvestigationID), Kind: kind, Statement: statement, EvidenceIDs: candidates[0].SupportingEvidenceIDs, CandidateIDs: []string{candidates[0].ID}, Status: status, Uncertainty: uncertainty, CreatedAt: time.Now().UTC()}
		report.Findings = append(report.Findings, finding)
		if persist {
			if err := d.cfg.Store.SaveFinding(finding); err != nil {
				return err
			}
		}
	}

	prelim := d.metrics.Begin(observability.StagePreliminaryDiagnosis)
	d.publishPreliminary(report)
	prelim.Done("completed", nil)
	d.metrics.MarkFirstFinding()
	d.emit(EventPreliminaryDiagnosis, domain.StatusFormingHypotheses, "preliminary diagnosis available")
	d.refreshProgressiveBundle(report)
	return nil
}

func hasRaceIncident(incidents []FailureIncident) bool {
	for _, incident := range incidents {
		if incident.Category == "go_data_race" {
			return true
		}
	}
	return false
}

func assessRootCause(report Report) RootCauseAssessment {
	assessment := RootCauseAssessment{
		Status:             "unknown",
		Statement:          "The available evidence establishes the failure observation but does not establish its root cause.",
		CausalityStatus:    "unverified",
		VerificationStatus: "not_attempted",
		MissingEvidence: []string{
			"a source-backed mechanism connecting the observed operation to the failure",
			"verification that the proposed mechanism is the violated invariant",
		},
	}
	if len(report.FailureIncidents) == 0 {
		assessment.Statement = "No root cause was established because no structured failure incident was available."
		assessment.MissingEvidence = []string{"a structured failure incident and source-backed explanation"}
		return assessment
	}
	incident := report.FailureIncidents[0]
	assessment.ObservedLocations = append([]IncidentLocation(nil), incident.ObservedLocations...)
	assessment.EvidenceIDs = append([]string(nil), incident.EvidenceIDs...)
	for _, race := range report.RaceReports {
		if !hasWaitGroupRaceSignature(race) {
			continue
		}
		assessment.Status = "partially_established"
		assessment.Statement = "The Go runtime markers establish a sync.WaitGroup lifecycle-ordering defect: cleanup reached the first Wait while a positive Add was occurring concurrently. The exact Add call and WaitGroup instance remain unresolved because the report points at a compiler-generated goroutine wrapper and the original race binary is unavailable."
		assessment.CausalityStatus = "mechanism_established_source_unmapped"
		assessment.MissingEvidence = []string{
			"instruction-level mapping of the compiler-generated wrapper to the exact positive WaitGroup Add",
			"base-versus-head verification before attributing the defect to the PR",
		}
		assessment.NextActions = []string{
			"rerun only the failing test with race detection and retain the exact test binary",
			"map the wrapper PC offset to the positive WaitGroup Add and its owning instance",
			"compare the same targeted test on base and head before attributing the defect to the PR",
		}
		if verification := report.GoColdPath; verification != nil {
			assessment.VerificationStatus = string(verification.Verdict)
			switch verification.Verdict {
			case ClassRealRegression:
				assessment.CausalityStatus = "pr_regression_verified"
				assessment.Statement += " Focused sandbox execution reproduced the same signature on the PR head and not on the passing base revision."
			case ClassPreExisting:
				assessment.CausalityStatus = "pre_existing_verified"
				assessment.Statement += " Focused sandbox execution reproduced the same signature on both base and head, so the PR is not established as its cause."
			case ClassNotReproduced:
				assessment.CausalityStatus = "mechanism_established_not_reproduced"
				assessment.Statement += " Focused sandbox execution did not reproduce the original signature."
			case ClassFlaky:
				assessment.CausalityStatus = "flaky_comparison"
				assessment.Statement += " Focused sandbox execution produced mixed outcomes, so PR attribution remains inconclusive."
			}
			assessment.NextActions = goColdPathNextActions(*verification)
		}
		return assessment
	}
	if len(report.InvestigationActions) > 0 && incident.Category == "go_data_race" {
		assessment.Statement = "Source inspection retrieved the enclosing functions and relevant variable, synchronization, lifecycle, caller, and test facts, but it did not establish the ownership invariant or a verified happens-before relationship that explains the conflicting accesses."
		assessment.MissingEvidence = []string{
			"a verified happens-before or ownership relationship between the read and write",
			"a controlled observation distinguishing a synchronization defect from a benign concurrent access",
		}
		assessment.NextActions = []string{
			"rerun only the failing test in a network-disabled sandbox with race detection enabled",
			"retain the exact race binary and build metadata so both accesses can be mapped to instructions and a runtime instance",
			"compare the same targeted test on base and head before attributing the race to the PR",
		}
	} else if len(report.InvestigationActions) > 0 {
		assessment.Statement = "Source inspection localized the failure and collected structural relationships, but no verified causal mechanism connected the observed failure to a specific invariant violation."
		assessment.NextActions = []string{
			"run the smallest failing test in a network-disabled sandbox",
			"compare the same targeted test on base and head",
			"collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective",
		}
	}
	if causalContractSatisfied(&report) {
		ids := append([]string(nil), incident.EvidenceIDs...)
		for _, id := range report.Concurrency.EvidenceIDs {
			ids = appendUniqueString(ids, id)
		}
		return RootCauseAssessment{
			Status: "confirmed", Statement: "The source-backed concurrency model establishes the conflicting accesses, the shared state, and a verified ordering relationship that explains the observed failure.",
			CausalSequence:    []string{"CI observed conflicting concurrent accesses", "source analysis identified the shared state and participating execution contexts", "the verified synchronization/lifecycle relationship establishes the violated ordering invariant"},
			ViolatedInvariant: "The conflicting accesses must obey the verified ownership and synchronization invariant.",
			EvidenceIDs:       ids, ObservedLocations: append([]IncidentLocation(nil), incident.ObservedLocations...), CausalityStatus: "established", VerificationStatus: "not_attempted",
		}
	}
	if chain := report.CausalChain; chain != nil && len(chain.Steps) > 0 {
		assessment.Status = "localized"
		assessment.Statement = "Source inspection identified calls reachable from the failing test; the operation causing the failed assertion and its violated invariant remain unresolved."
		assessment.CausalityStatus = "reachability_only"
		assessment.EvidenceIDs = uniqueStrings(append(assessment.EvidenceIDs, chain.EvidenceIDs...))
		assessment.MissingEvidence = []string{"data flow connecting the specific operation to the failed assertion", "evidence establishing the violated invariant"}
		if trace := report.SSAAssertionTrace; trace != nil && len(trace.Values) > 0 {
			assessment.CausalityStatus = "static_value_dependencies_unverified"
			assessment.Statement = "Typed SSA traced static value dependencies feeding the failed assertion. Branch feasibility, memory effects, and the violated invariant remain unverified."
			assessment.EvidenceIDs = uniqueStrings(append(assessment.EvidenceIDs, trace.EvidenceIDs...))
			for _, gap := range trace.Limitations {
				if len(assessment.MissingEvidence) >= 8 {
					break
				}
				assessment.MissingEvidence = appendUniqueString(assessment.MissingEvidence, gap)
			}
			assessment.NextActions = []string{"use the existing isolated experiment runner to observe unresolved branches, dynamic calls, or shared-state effects identified in the SSA trace", "match the observed runtime failure before promoting a static dependency to a verified mechanism"}
		}
		return assessment
	}
	// Deterministic non-race failures (assertion, logic, contract, dependency,
	// infrastructure) have no concurrency model, so the causal contract above can
	// never be satisfied for them. Describe the failure mechanism at the level the
	// evidence actually supports: the observed behavior plus its verified base/head
	// outcome. Do not fabricate a source-level invariant the investigation did not
	// establish. This keeps the claim evidence-backed and honest.
	if deterministicRootCauseAvailable(report, incident) {
		return deterministicRootCause(report, incident, assessment)
	}
	return assessment
}

// deterministicRootCauseAvailable reports whether a non-race failure has enough
// evidence to state a behavioral root cause. It deliberately excludes races
// (handled above), inconclusive classifications, and failures with no incident
// evidence at all.
func deterministicRootCauseAvailable(report Report, incident FailureIncident) bool {
	if incident.Category == "go_data_race" {
		return false
	}
	switch report.Classification {
	case ClassInconclusive, ClassTestNotFound, ClassInvalidExecution, ClassNotReproduced:
		return false
	}
	// The non-race incident is built from failure episodes and may not carry
	// EvidenceIDs directly. What matters is that the report has citable CI or
	// classification evidence (the deterministicRootCause scan collects those IDs).
	for _, e := range report.Evidence {
		if e.Kind == "classification" || e.Kind == "original_ci_log" || e.Kind == "ci_log" {
			return true
		}
	}
	return len(incident.EvidenceIDs) > 0
}

// deterministicRootCause builds an evidence-cited behavioral explanation for a
// deterministic non-race failure. The strongest claim is made only when a
// verified base-versus-head comparison or an explicit classification is present;
// otherwise the mechanism stays explicitly source-unmapped.
func deterministicRootCause(report Report, incident FailureIncident, base RootCauseAssessment) RootCauseAssessment {
	assessment := base
	ids := append([]string(nil), incident.EvidenceIDs...)
	for _, e := range report.Evidence {
		if e.Kind == "classification" || e.Kind == "original_ci_log" || e.Kind == "ci_log" {
			ids = appendUniqueString(ids, e.ID)
		}
	}
	assessment.EvidenceIDs = uniqueStrings(ids)

	mechanism := classificationMechanism(report.Classification)
	assessment.ViolatedInvariant = mechanism.invariant
	assessment.CausalSequence = []string{
		"CI evidence recorded the observed failure: " + failureSummary(incident),
		mechanism.explanation,
	}
	assessment.Status = "partially_established"
	assessment.CausalityStatus = "behavior_established_source_unmapped"
	assessment.Statement = mechanism.statement
	assessment.MissingEvidence = []string{
		"a source-level mechanism connecting the reported location to the failing operation",
		"verification that the proposed mechanism is the violated invariant",
	}
	assessment.NextActions = []string{
		"run the smallest failing test in a network-disabled sandbox to verify the observed behavior",
		"inspect the implementation reached by the failing operation, not only the reported test frame",
		"compare the same targeted test on base and head before attributing the defect to a change",
	}

	// A verified behavioral verdict upgrades the causal status, but never to a
	// source-level "confirmed" mechanism the investigation did not establish.
	switch report.Classification {
	case ClassRealRegression:
		assessment.CausalityStatus = "behavior_verified_as_regression"
		assessment.VerificationStatus = "verified"
		assessment.Statement += " The failure was verified as a regression by base-versus-head comparison."
		assessment.CausalSequence = append(assessment.CausalSequence, "base-versus-head comparison showed the failure on the change under test and not on the base revision")
	case ClassPreExisting:
		assessment.CausalityStatus = "behavior_verified_as_pre_existing"
		assessment.VerificationStatus = "verified"
		assessment.Statement += " The failure was verified as pre-existing, so the change under test is not its cause."
		assessment.CausalSequence = append(assessment.CausalSequence, "base-versus-head comparison showed the same failure on both revisions")
	case ClassFlaky:
		assessment.CausalityStatus = "behavior_verified_as_flaky"
		assessment.VerificationStatus = "verified"
		assessment.Statement += " The failure was observed intermittently across repeated attempts."
		assessment.CausalSequence = append(assessment.CausalSequence, "repeated execution produced both passing and failing outcomes")
	case ClassDependencyDrift:
		assessment.CausalityStatus = "behavior_verified_as_dependency"
		assessment.VerificationStatus = "verified"
		assessment.Statement += " The failure output matches dependency or toolchain drift rather than application logic."
	case ClassInfrastructure:
		assessment.CausalityStatus = "behavior_verified_as_environment"
		assessment.VerificationStatus = "verified"
		assessment.Statement += " The failure output matches an environment or infrastructure fault."
	}
	if report.Comparison != nil {
		assessment.CausalSequence = append(assessment.CausalSequence,
			fmt.Sprintf("comparison verdict for %s versus %s was %s", report.Comparison.BaseCommit, report.Comparison.HeadCommit, report.Comparison.Verdict))
	}
	return assessment
}

type failureMechanism struct {
	statement   string
	explanation string
	invariant   string
}

func classificationMechanism(class Classification) failureMechanism {
	switch class {
	case ClassTestFailureObserved:
		return failureMechanism{
			statement:   "The observed failure is a deterministic test failure recorded in CI evidence; the failing operation and its expected-versus-actual behavior are described by the failure log.",
			explanation: "the failing test and its assertion output identify the operation whose behavior violated the test's expectation",
			invariant:   "the tested operation must satisfy the behavior asserted by the failing test",
		}
	case ClassLintFailureObserved:
		return failureMechanism{
			statement:   "The observed failure is a linter or static-analysis violation recorded in CI evidence.",
			explanation: "the reported diagnostic identifies the source location and rule that was violated",
			invariant:   "the source must satisfy the static-analysis rule reported by the linter",
		}
	case ClassDependencyDrift:
		return failureMechanism{
			statement:   "The observed failure is consistent with dependency or toolchain drift rather than an application-code defect.",
			explanation: "the failure output matches dependency resolution or toolchain errors instead of an application assertion",
			invariant:   "dependency and toolchain versions must match the versions the failing operation expects",
		}
	case ClassInfrastructure:
		return failureMechanism{
			statement:   "The observed failure is consistent with an environment or infrastructure fault rather than an application-code defect.",
			explanation: "the failure output matches environment or infrastructure errors instead of an application assertion",
			invariant:   "the execution environment must provide the resources and configuration the command requires",
		}
	case ClassFlaky, ClassPreExisting, ClassRealRegression:
		return failureMechanism{
			statement:   "The observed failure was reproduced and classified by the diagnosis loop.",
			explanation: "verification evidence establishes the behavioral character of the failure",
			invariant:   "the behavior under test must remain consistent across the verified revisions and attempts",
		}
	default:
		return failureMechanism{
			statement:   "The observed failure is described by the collected CI and source evidence.",
			explanation: "the collected evidence identifies the reported failure and its location",
			invariant:   "the failing operation must satisfy the behavior expected by its caller or test",
		}
	}
}

func failureSummary(incident FailureIncident) string {
	if len(incident.AffectedTests) > 0 {
		return strings.Join(incident.AffectedTests, ", ") + " (" + incident.Category + ")"
	}
	if len(incident.ObservedLocations) > 0 {
		loc := incident.ObservedLocations[0]
		return fmt.Sprintf("%s at %s:%d", incident.Category, loc.File, loc.Line)
	}
	return incident.Category
}

func causalContractSatisfied(report *Report) bool {
	if !allObjectivesResolved(report) || report.Concurrency == nil || len(report.Concurrency.Accesses) < 2 || len(report.Concurrency.SharedState) == 0 || len(report.Concurrency.MissingOrdering) != 0 {
		return false
	}
	for _, edge := range report.Concurrency.HappensBeforeEdges {
		if edge.Verified && edge.PredecessorOp != "" && edge.SuccessorOp != "" && edge.Mechanism != "" && edge.MemoryModel != "" {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func stringIDsToDomain(values []string) []domain.ID {
	out := make([]domain.ID, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, domain.ID(value))
		}
	}
	return out
}

func (d *Diagnoser) stageOverview(ctx context.Context, report *Report, workspace, repoID, revisionID, commitSHA string, artifactCache *cache.FSCache) (programrepo.Overview, error) {
	slog.Info("diagnosis stage started", "stage", "repository_overview", "investigation_id", report.InvestigationID)
	if report.NormalizedEvidence != nil || report.Baseline.Source == "ci_artifact" {
		stage := d.metrics.Begin(observability.StageRepositoryOverview)
		stage.Meta("mode", "focused")
		overview := focusedOverview(repoID, revisionID, commitSHA, report.Localization.LikelyFiles)
		stage.Count("files", sumCounts(overview.FileCounts))
		stage.Count("languages", len(overview.Languages))
		stage.Done("completed", nil)
		slog.Info("diagnosis stage completed", "stage", "repository_overview", "mode", "focused", "selected_paths", len(report.Localization.LikelyFiles))
		report.CoverageNotes = append(report.CoverageNotes, "repository overview is focused on CI-selected paths; broad metadata scan is deferred")
		return overview, nil
	}
	stage := d.metrics.Begin(observability.StageRepositoryOverview)
	key := cache.Key{RepositoryID: repoID, CommitSHA: commitSHA, AnalyzerVersion: cache.AnalyzerVersion, ArtifactType: cache.ArtifactOverview, ArtifactID: "overview"}
	var overview programrepo.Overview
	if artifactCache != nil {
		if err := artifactCache.Get(key, &overview); err == nil {
			d.metrics.RecordCacheHit()
			stage.Meta("cache", "hit")
			stage.Count("files", sumCounts(overview.FileCounts))
			stage.Done("completed", nil)
			return overview, nil
		} else if !errors.Is(err, cache.ErrMiss) && !errors.Is(err, cache.ErrDisabled) && !errors.Is(err, cache.ErrCorrupt) {
			stage.Done("failed", err)
			return overview, err
		}
		d.metrics.RecordCacheMiss()
		stage.Meta("cache", "miss")
	}
	overview, err := programrepo.BuildOverview(ctx, workspace, repoID, revisionID, commitSHA)
	if err != nil {
		stage.Done("failed", err)
		return overview, err
	}
	if artifactCache != nil {
		_ = artifactCache.Put(key, overview)
	}
	stage.Count("files", sumCounts(overview.FileCounts))
	stage.Count("languages", len(overview.Languages))
	stage.Done("completed", nil)
	_ = report
	return overview, nil
}

func focusedOverview(repoID, revisionID, commitSHA string, paths []string) programrepo.Overview {
	counts := map[string]int{}
	langs := map[string]bool{}
	for _, path := range paths {
		var lang string
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go":
			lang = "go"
		case ".py":
			lang = "python"
		case ".js", ".jsx", ".mjs", ".ts", ".tsx":
			lang = "javascript"
		case ".rs":
			lang = "rust"
		}
		if lang != "" {
			counts[lang]++
			langs[lang] = true
		}
	}
	var languages []string
	for lang := range langs {
		languages = append(languages, lang)
	}
	sort.Strings(languages)
	return programrepo.Overview{
		RepositoryID: repoID, RevisionID: revisionID, CommitSHA: commitSHA,
		Languages: languages, FileCounts: counts, GeneratedAt: time.Now().UTC(),
		CoverageNotes: []string{"focused overview is not exhaustive; missing languages and files are unresolved"},
		Metadata:      map[string]string{"mode": "focused", "selected_paths": fmt.Sprint(len(paths))},
	}
}

func (d *Diagnoser) stageIndex(ctx context.Context, report *Report, workspace, repoID, revisionID, commitSHA string, loc Localization, artifactCache *cache.FSCache) (programrepo.Snapshot, string, error) {
	slog.Info("diagnosis stage started", "stage", "indexing", "investigation_id", report.InvestigationID)
	var targetTests []string
	for _, episode := range report.FailureEpisodes {
		for _, name := range regexp.MustCompile(`\bTest[A-Za-z0-9_]+`).FindAllString(episode.TestIdentifier, -1) {
			targetTests = appendUniqueString(targetTests, name)
		}
	}
	sort.Strings(targetTests)
	key := cache.Key{RepositoryID: repoID, CommitSHA: commitSHA, AnalyzerVersion: cache.AnalyzerVersion, ArtifactType: cache.ArtifactSnapshot, ArtifactID: "snapshot"}
	if len(targetTests) > 0 {
		key.ArtifactID += ":ssa:" + digestString(strings.Join(targetTests, "\x00"))
	}
	if artifactCache != nil {
		var cached programrepo.Snapshot
		if err := artifactCache.Get(key, &cached); err == nil {
			d.metrics.RecordCacheHit()
			stage := d.metrics.Begin(observability.StageIndexing)
			stage.Meta("cache", "hit")
			stage.Meta("mode", "cached")
			stage.Count("files", countSnapshotFiles(cached))
			stage.Count("symbols", countSnapshotSymbols(cached))
			stage.Done("completed", nil)
			return cached, "cached", nil
		} else if errors.Is(err, cache.ErrMiss) || errors.Is(err, cache.ErrCorrupt) {
			d.metrics.RecordCacheMiss()
		} else if !errors.Is(err, cache.ErrDisabled) {
			return programrepo.Snapshot{}, "", err
		}
	}

	analyzer := programrepo.Analyzer{TargetTests: targetTests}
	// Targeted indexing is for externally observed CI failures. Ordinary local
	// reproduction still uses the complete snapshot because its localization
	// and adaptive stages already provide an explicit repository scope.
	if report.NormalizedEvidence != nil || report.Baseline.Source == "ci_artifact" {
		if targeted := expandTargetPaths(workspace, loc.LikelyFiles, 80); len(targeted) > 0 {
			stage := d.metrics.Begin(observability.StageIndexing)
			stage.Meta("mode", "targeted")
			snapshot, err := analyzer.AnalyzePaths(ctx, workspace, repoID, revisionID, commitSHA, targeted)
			if err != nil {
				stage.Done("failed", err)
				return snapshot, "", err
			}
			stage.Count("files", countSnapshotFiles(snapshot))
			stage.Count("symbols", countSnapshotSymbols(snapshot))
			stage.Count("requested_files", len(targeted))
			stage.Done("completed", nil)
			report.CoverageNotes = append(report.CoverageNotes,
				"targeted indexing selected CI-reported and PR-relevant files before broader expansion",
				"snapshot coverage is intentionally bounded; absent files are unresolved, not negative evidence")
			return snapshot, "targeted", nil
		}
		stage := d.metrics.Begin(observability.StageIndexing)
		stage.Meta("mode", "targeted-empty")
		stage.Done("incomplete", nil)
		report.CoverageNotes = append(report.CoverageNotes, "no usable source paths were extracted from CI evidence; broad indexing was refused")
		slog.Warn("targeted indexing had no usable paths; broad scan refused", "investigation_id", report.InvestigationID)
		return programrepo.Snapshot{RepositoryID: repoID, RevisionID: revisionID, CommitSHA: commitSHA}, "targeted-empty", nil
	}
	baseRef := d.cfg.BaseRef
	if baseRef != "" && artifactCache != nil {
		baseSHA := stringsTrim(gitOutput(d.cfg.RepoPath, "rev-parse", baseRef))
		if baseSHA != "" {
			baseKey := cache.Key{RepositoryID: repoID, CommitSHA: baseSHA, AnalyzerVersion: cache.AnalyzerVersion, ArtifactType: cache.ArtifactSnapshot, ArtifactID: "snapshot"}
			var base programrepo.Snapshot
			if err := artifactCache.Get(baseKey, &base); err == nil {
				d.metrics.RecordCacheHit()
				changes, diffErr := programrepo.DiffFiles(ctx, workspace, baseSHA, commitSHA)
				if diffErr == nil {
					stage := d.metrics.Begin(observability.StageIncrementalIndexing)
					inc, incErr := analyzer.AnalyzeIncremental(ctx, workspace, repoID, revisionID, commitSHA, base, changes)
					if incErr != nil {
						stage.Done("failed", incErr)
						return programrepo.Snapshot{}, "", incErr
					}
					stage.Count("files", countSnapshotFiles(inc.Snapshot))
					stage.Count("symbols", countSnapshotSymbols(inc.Snapshot))
					stage.Count("reparsed", inc.ReparsedFiles)
					stage.Count("reused", inc.ReusedFiles)
					stage.Meta("mode", "incremental")
					stage.Done("completed", nil)
					report.CoverageNotes = append(report.CoverageNotes, inc.CoverageNotes...)
					_ = artifactCache.Put(key, inc.Snapshot)
					_ = loc
					return inc.Snapshot, "incremental", nil
				}
			} else if errors.Is(err, cache.ErrMiss) {
				d.metrics.RecordCacheMiss()
			}
		}
	}

	stage := d.metrics.Begin(observability.StageIndexing)
	stage.Meta("mode", "full")
	snapshot, err := analyzer.Analyze(ctx, workspace, repoID, revisionID, commitSHA)
	if err != nil {
		stage.Done("failed", err)
		return snapshot, "", err
	}
	stage.Count("files", countSnapshotFiles(snapshot))
	stage.Count("symbols", countSnapshotSymbols(snapshot))
	stage.Done("completed", nil)
	if artifactCache != nil {
		_ = artifactCache.Put(key, snapshot)
	}
	return snapshot, "full", nil
}

func boundedTargetPaths(paths []string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, path := range paths {
		path = strings.TrimSpace(filepath.ToSlash(path))
		if path == "" || seen[path] || len(out) >= max {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func expandTargetPaths(root string, paths []string, max int) []string {
	requested := boundedTargetPaths(paths, max)
	var out []string
	seen := map[string]bool{}
	unresolvedNames := map[string]bool{}
	add := func(path string) {
		path = filepath.ToSlash(path)
		if path == "" || seen[path] || len(out) >= max {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, path := range requested {
		// CI stack frames carry absolute paths from the original runner. Make
		// them relative to this checkout so the workspace prefix does not hide
		// the source (and so the test's parent-package expansion can find it).
		if filepath.IsAbs(path) {
			if rel, err := filepath.Rel(root, filepath.FromSlash(path)); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				path = filepath.ToSlash(rel)
			}
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err == nil && !info.IsDir() {
			add(path)
		} else if base := filepath.Base(path); isSourcePath(base) {
			unresolvedNames[base] = true
		}
	}
	// Resolve basename-only seeds (e.g. a race frame reduced to numbers_test.go)
	// BEFORE package expansion, so an external test package's test file lands in
	// out and its parent directory can be scanned for the implementation.
	if len(unresolvedNames) > 0 && len(out) < max {
		scanned := 0
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || len(out) >= max || scanned >= 20000 {
				if scanned >= 20000 {
					return filepath.SkipAll
				}
				return nil
			}
			scanned++
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", ".malenia", "node_modules", "vendor", "target", "dist", "build":
					if path != root {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !unresolvedNames[entry.Name()] {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil {
				add(rel)
			}
			return nil
		})
	}
	// A failing test's implementation lives in its package, which may be the
	// same directory or (for external test packages such as tests/ submodules)
	// the parent that the test imports. CI-observed paths are usually only the
	// test file, so add non-test source files from the test's directory and its
	// parent; otherwise the focused snapshot never contains the callee nodes
	// needed to connect the failure to its cause. Bounded by the path cap.
	for _, path := range append([]string(nil), out...) {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		dir := filepath.Dir(path)
		parent := filepath.Dir(dir)
		for _, sourceDir := range []string{dir, parent} {
			if sourceDir == "." || sourceDir == "" {
				sourceDir = "."
			}
			entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(sourceDir)))
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
					continue
				}
				add(filepath.ToSlash(filepath.Join(sourceDir, entry.Name())))
			}
		}
	}
	for _, path := range append([]string(nil), out...) {
		if len(out) >= max {
			break
		}
		dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(path)))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !isSourcePath(entry.Name()) {
				continue
			}
			rel, err := filepath.Rel(root, filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !seen[rel] && len(out) < max {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	return out
}

func isSourcePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".js", ".jsx", ".mjs", ".ts", ".tsx", ".rs", ".java", ".kt", ".kts":
		return true
	default:
		return false
	}
}

func (d *Diagnoser) stageDeltas(ctx context.Context, workspace, repoID, headRevisionID string, head programrepo.Snapshot) ([]domain.CodeDelta, EvidenceItem, error) {
	baseSHA := stringsTrim(gitOutput(d.cfg.RepoPath, "rev-parse", d.cfg.BaseRef))
	if baseSHA == "" {
		return nil, EvidenceItem{}, fmt.Errorf("base revision %q could not be resolved", d.cfg.BaseRef)
	}
	var base programrepo.Snapshot
	err := d.workspaceAt(ctx, baseSHA, func(dir string) error {
		var analyzeErr error
		base, analyzeErr = (programrepo.Analyzer{}).Analyze(ctx, dir, repoID, "revision:"+baseSHA, baseSHA)
		return analyzeErr
	})
	if err != nil {
		return nil, EvidenceItem{}, err
	}
	added, removed, changed := programrepo.Compare(base, head)
	baseNodes := map[string]domain.ProgramNode{}
	headNodes := map[string]domain.ProgramNode{}
	for _, node := range base.Nodes {
		if node.Type != "File" {
			baseNodes[node.Identity] = node
		}
	}
	for _, node := range head.Nodes {
		if node.Type != "File" {
			headNodes[node.Identity] = node
		}
	}
	const evidenceID = "E-pr-delta"
	var deltas []domain.CodeDelta
	addDelta := func(identity, kind string) {
		node := headNodes[identity]
		baseNode := baseNodes[identity]
		if node.Identity == "" {
			node = baseNode
		}
		line := node.StartLine
		if kind == "removed" {
			line = baseNode.StartLine
		}
		deltas = append(deltas, domain.CodeDelta{
			ID: "delta:" + digestString(baseSHA+"\x00"+head.CommitSHA+"\x00"+kind+"\x00"+identity), RepositoryID: repoID,
			BaseRevisionID: "revision:" + baseSHA, HeadRevisionID: headRevisionID, Kind: kind,
			SymbolIdentity: identity, FilePath: node.FilePath, BaseStartLine: baseNode.StartLine, HeadStartLine: headNodes[identity].StartLine,
			EvidenceID: evidenceID, Summary: fmt.Sprintf("%s symbol %s (%s:%d)", kind, identity, node.FilePath, line),
		})
	}
	for _, identity := range added {
		addDelta(identity, "added")
	}
	for _, identity := range removed {
		addDelta(identity, "removed")
	}
	for _, identity := range changed {
		addDelta(identity, "modified")
	}
	if len(deltas) == 0 {
		return nil, EvidenceItem{}, nil
	}
	var detail strings.Builder
	for _, delta := range deltas {
		detail.WriteString(delta.Summary)
		detail.WriteByte('\n')
	}
	evidence := EvidenceItem{ID: evidenceID, Kind: "pr_delta", Source: "base/head repository graph comparison", Digest: digestString(detail.String()), Detail: detail.String(), Provenance: "deterministic comparison of indexed base and head revisions"}
	return deltas, evidence, nil
}

type graphPathState struct {
	nodeIDs []string
	relIDs  []string
}

func buildExecutionPaths(investigationID domain.ID, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation, failureText string, failureIDs []string, evidenceIDs []domain.ID) ([]domain.ExecutionPath, []EvidenceItem) {
	nodes := map[string]domain.ProgramNode{}
	for _, node := range snapshot.Nodes {
		nodes[node.ID] = node
	}
	edges := map[string][]domain.Relationship{}
	for _, relation := range snapshot.Relationships {
		edges[relation.SourceID] = append(edges[relation.SourceID], relation)
	}
	targets := map[string]bool{}
	provenanceByNode := map[string]string{}
	for _, candidate := range candidates {
		const prefix = "candidate:"
		if strings.HasPrefix(candidate.ID, prefix) {
			nodeID := strings.TrimPrefix(candidate.ID, prefix)
			targets[nodeID] = true
			provenanceByNode[nodeID] = candidate.Location.Provenance
		}
	}
	var tests []domain.ProgramNode
	for _, node := range snapshot.Nodes {
		if node.Type == "Test" && (strings.Contains(failureText, node.Name) || strings.Contains(failureText, node.FilePath)) {
			tests = append(tests, node)
		}
	}
	var paths []domain.ExecutionPath
	seen := map[string]bool{}
	for _, test := range tests {
		queue := []graphPathState{{nodeIDs: []string{test.ID}}}
		for len(queue) > 0 && len(paths) < 20 {
			state := queue[0]
			queue = queue[1:]
			current := state.nodeIDs[len(state.nodeIDs)-1]
			if len(state.nodeIDs) > 1 && targets[current] {
				locations := make([]domain.CodeLocation, 0, len(state.nodeIDs))
				parts := make([]string, 0, len(state.nodeIDs))
				for _, nodeID := range state.nodeIDs {
					node := nodes[nodeID]
					provenance := provenanceByNode[node.ID]
					if provenance == "" && node.ID == test.ID {
						provenance = "package_association"
					}
					locations = append(locations, domain.CodeLocation{ID: "location:" + node.ID, RepositoryID: node.RepositoryID, RevisionID: node.RevisionID, FilePath: node.FilePath, SymbolIdentity: node.Identity, StartLine: node.StartLine, EndLine: node.EndLine, LocationType: node.Type, Provenance: provenance})
					parts = append(parts, node.FilePath+":"+node.Name)
				}
				pathKey := strings.Join(state.nodeIDs, ">")
				if !seen[pathKey] {
					seen[pathKey] = true
					path := domain.ExecutionPath{ID: "path:" + digestString(string(investigationID)+"\x00"+pathKey), InvestigationID: investigationID, FailureIDs: append([]string{}, failureIDs...), NodeIDs: append([]string{}, state.nodeIDs...), RelationshipIDs: append([]string{}, state.relIDs...), Locations: locations, EvidenceIDs: append([]domain.ID{}, evidenceIDs...), Status: "inferred_call_path", Summary: strings.Join(parts, " → ")}
					paths = append(paths, path)
				}
				continue
			}
			if len(state.relIDs) >= 4 {
				continue
			}
			for _, relation := range edges[current] {
				// Structural references, imports, and package proximity are not
				// execution edges. Only explicit test/call relationships may
				// participate in an inferred call path.
				if relation.Type != "CALLS" || relation.Metadata["provenance"] != "verified_static_call" {
					continue
				}
				if _, ok := nodes[relation.TargetID]; !ok {
					continue
				}
				already := false
				for _, nodeID := range state.nodeIDs {
					if nodeID == relation.TargetID {
						already = true
						break
					}
				}
				if already {
					continue
				}
				queue = append(queue, graphPathState{nodeIDs: append(append([]string{}, state.nodeIDs...), relation.TargetID), relIDs: append(append([]string{}, state.relIDs...), relation.ID)})
			}
		}
	}
	var evidence []EvidenceItem
	for _, path := range paths {
		evidence = append(evidence, EvidenceItem{ID: "E-" + path.ID, Kind: "execution_path", Source: "revision-scoped repository graph", Digest: digestString(path.Summary), Detail: path.Summary, Provenance: "deterministic graph traversal; structural path, not causal proof", Revision: snapshot.CommitSHA})
	}
	return paths, evidence
}

func (d *Diagnoser) openArtifactCache() (*cache.FSCache, error) {
	root := d.cfg.CacheDir
	if root == "" && d.cfg.RepoPath != "" {
		root = filepath.Join(d.cfg.RepoPath, ".malenia", "artifact-cache")
	}
	return cache.OpenFS(root)
}

func (d *Diagnoser) publishPreliminary(report *Report) {
	status := "preliminary"
	var statement string
	if len(report.FailureIncidents) > 0 {
		statement = report.FailureIncidents[0].ObservedExplanation
		status = "observed"
	} else if len(report.FailureEpisodes) == 0 {
		statement = "No distinct failure episodes extracted yet"
		status = "insufficient_evidence"
	} else if len(report.Candidates) > 0 {
		statement = fmt.Sprintf("Preliminary: %d failure episode(s), %d group(s); leading candidate %s", len(report.FailureEpisodes), len(report.FailureGroups), report.Candidates[0].Location.SymbolIdentity)
	} else {
		statement = fmt.Sprintf("Preliminary: %d failure episode(s) observed; candidate localization incomplete", len(report.FailureEpisodes))
		status = "partial"
	}
	finding := domain.DiagnosticFinding{
		ID:              "finding:preliminary:" + report.InvestigationID,
		InvestigationID: domain.ID(report.InvestigationID),
		Kind:            "preliminary_diagnosis",
		Statement:       statement,
		Status:          status,
		Uncertainty:     []string{"preliminary findings are not confirmed root causes", "adaptive investigation may revise this report"},
		CreatedAt:       time.Now().UTC(),
	}
	if len(report.Candidates) > 0 {
		finding.CandidateIDs = []string{report.Candidates[0].ID}
		finding.EvidenceIDs = report.Candidates[0].SupportingEvidenceIDs
	}
	report.Preliminary = &finding
	// Keep progressive findings de-duplicated across refreshes.
	filtered := report.Findings[:0]
	for _, existing := range report.Findings {
		if existing.ID != finding.ID {
			filtered = append(filtered, existing)
		}
	}
	if len(report.FailureIncidents) == 0 {
		report.Findings = append(filtered, finding)
	} else {
		report.Findings = filtered
	}
	if d.cfg.Store != nil {
		_ = d.cfg.Store.SaveFinding(finding)
	}
}

func (d *Diagnoser) refreshProgressiveBundle(report *Report) {
	if d.cfg.BundleDir == "" {
		return
	}
	path, err := WriteBundle(*report, d.events, d.cfg.BundleDir)
	if err != nil {
		report.Uncertainty = append(report.Uncertainty, "progressive bundle update failed: "+err.Error())
		return
	}
	report.BundlePath = path
	report.ReplayCommand = fmt.Sprintf("malenia replay %s", path)
}

func sumCounts(values map[string]int) int {
	total := 0
	for _, n := range values {
		total += n
	}
	return total
}

func countSnapshotFiles(s programrepo.Snapshot) int {
	n := 0
	for _, node := range s.Nodes {
		if node.Type == "File" {
			n++
		}
	}
	return n
}

func countSnapshotSymbols(s programrepo.Snapshot) int {
	n := 0
	for _, node := range s.Nodes {
		switch node.Type {
		case "Function", "Method", "Test", "Class":
			n++
		}
	}
	return n
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
