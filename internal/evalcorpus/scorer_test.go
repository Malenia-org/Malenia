package evalcorpus

import (
	"testing"

	"github.com/causent/malenia/internal/diagnosis"
)

func scoredReport() diagnosis.Report {
	return diagnosis.Report{
		Classification: diagnosis.ClassRealRegression,
		Localization: diagnosis.Localization{
			LikelyFiles:     []string{"example.go"},
			CandidateCommit: []diagnosis.CommitInfo{{SHA: "abcdef1234567890", Subject: "fix"}},
		},
		Evidence: []diagnosis.EvidenceItem{{ID: "E1", Kind: "failure", Detail: "example test failure"}},
		Claims:   []diagnosis.Claim{{ID: "H1", Statement: "example", Supporting: []string{"E1"}}},
	}
}

func expectedAnswerable() Expected {
	return Expected{PrimaryFailure: "example test failure", Category: CategoryRegression, ExpectedOutcome: OutcomeAnswerable, CulpritFile: "example.go", CulpritCommit: "abcdef1", EvidenceSource: "fix commit", Annotator: "test"}
}

func TestScoreAllFourOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		report  diagnosis.Report
		expect  Expected
		outcome Outcome
	}{
		{name: "correct", report: scoredReport(), expect: expectedAnswerable(), outcome: Correct},
		{name: "wrong", report: scoredReport(), expect: Expected{PrimaryFailure: "x", Category: CategoryDependency, ExpectedOutcome: OutcomeAnswerable, EvidenceSource: "fix commit", Annotator: "test"}, outcome: Wrong},
		{name: "justified abstention", report: func() diagnosis.Report { r := scoredReport(); r.Refused = true; return r }(), expect: Expected{PrimaryFailure: "x", Category: CategoryRegression, ExpectedOutcome: OutcomeInsufficientEvidence, EvidenceSource: "maintainer comment", Annotator: "test"}, outcome: JustifiedAbstention},
		{name: "unjustified abstention", report: func() diagnosis.Report { r := scoredReport(); r.Refused = true; return r }(), expect: expectedAnswerable(), outcome: UnjustifiedAbstention},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ScoreReport("case", test.report, test.expect)
			if got.Outcome != test.outcome {
				t.Fatalf("outcome=%s want=%s reason=%s", got.Outcome, test.outcome, got.Reason)
			}
		})
	}
}

func TestScoreRejectsUnsupportedAnswer(t *testing.T) {
	report := scoredReport()
	report.Claims = []diagnosis.Claim{{ID: "H1", Statement: "unsupported", Supporting: nil}}
	got := ScoreReport("case", report, expectedAnswerable())
	if got.Outcome != Wrong {
		t.Fatalf("outcome=%s reason=%s", got.Outcome, got.Reason)
	}
}

func TestCountsKeepAllRatesSeparate(t *testing.T) {
	var counts Counts
	for _, outcome := range []Outcome{Correct, Wrong, JustifiedAbstention, UnjustifiedAbstention} {
		counts.Add(Score{Outcome: outcome})
	}
	if counts.Total != 4 || counts.CorrectRate != .25 || counts.WrongRate != .25 || counts.JustifiedRate != .25 || counts.UnjustifiedRate != .25 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
}

func TestHistoricalChainStoppingPointCannotBecomeGroundTruth(t *testing.T) {
	report := scoredReport()
	report.RootCause.Status = "established"
	expected := expectedAnswerable()
	expected.CausalCulpritFile = "constructor.go"
	report.CausalChain = &diagnosis.DeterministicCausalChain{Status: "established", Steps: []diagnosis.DeterministicCausalStep{{File: "constructor.go", EvidenceID: "E1"}}}
	score := ScoreReport("go-test", report, expected)
	if score.Outcome != Wrong || !score.UnsupportedEstablished || score.MechanismVerified {
		t.Fatalf("reachability accepted as mechanism: %+v", score)
	}
}

func TestMechanismScoreRequiresExactUpstreamDeclarationAndRevision(t *testing.T) {
	report := scoredReport()
	expected := expectedAnswerable()
	expected.CulpritFunction = "example.Convert"
	report.Environment.HeadCommit = "revision"
	report.RootCause = diagnosis.RootCauseAssessment{Status: "established", EvidenceIDs: []string{"mechanism"}}
	report.Evidence = append(report.Evidence, diagnosis.EvidenceItem{ID: "mechanism", ClaimKind: "verified_failure_mechanism", Basis: "DataflowVerified", Source: "example.go", DeclarationID: "example.Convert", Revision: "revision"})
	if score := ScoreReport("go-test", report, expected); score.Outcome != Correct || !score.MechanismVerified {
		t.Fatalf("verified mechanism rejected: %+v", score)
	}
	report.Evidence[len(report.Evidence)-1].Source = "unrelated/example.go"
	if score := ScoreReport("go-test", report, expected); score.Outcome != Wrong {
		t.Fatalf("basename collision accepted: %+v", score)
	}
}
