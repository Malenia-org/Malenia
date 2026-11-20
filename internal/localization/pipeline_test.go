package localization

import (
	"strings"
	"testing"
	"time"

	"github.com/Malenia-org/ceaser/internal/domain"
	programrepo "github.com/Malenia-org/ceaser/internal/repository"
)

func TestGenerateDoesNotPromoteUnverifiedRelationships(t *testing.T) {
	file := domain.ProgramNode{ID: "file", Type: "File", FilePath: "pkg.go", RevisionID: "rev", RepositoryID: "repo"}
	test := domain.ProgramNode{ID: "test", Type: "Test", Name: "TestFailure", FilePath: "pkg_test.go", StartLine: 1, EndLine: 10, RevisionID: "rev", RepositoryID: "repo", Identity: "go:pkg_test.go:Test:TestFailure"}
	changed := domain.ProgramNode{ID: "changed", Type: "Function", Name: "Changed", FilePath: "pkg.go", StartLine: 1, EndLine: 10, RevisionID: "rev", RepositoryID: "repo", Identity: "go:pkg.go:Function:Changed", Changed: true}
	unchanged := domain.ProgramNode{ID: "utility", Type: "Function", Name: "Utility", FilePath: "utility.go", StartLine: 1, EndLine: 10, RevisionID: "rev", RepositoryID: "repo", Identity: "go:utility.go:Function:Utility"}
	s := programrepo.Snapshot{
		RepositoryID: "repo",
		RevisionID:   "rev",
		Nodes:        []domain.ProgramNode{file, test, changed, unchanged},
		Relationships: []domain.Relationship{
			{ID: "r1", SourceID: "test", SourceType: "Test", TargetID: "changed", TargetType: "Function", Type: "TESTS", RevisionID: "rev", CreatedAt: time.Now()},
			{ID: "r2", SourceID: "changed", SourceType: "Function", TargetID: "utility", TargetType: "Function", Type: "CALLS", RevisionID: "rev", CreatedAt: time.Now()},
		},
	}
	got, rels := Generate(s, Input{InvestigationID: "inv", FailureIDs: []string{"failure-1"}, FailureText: "TestFailure", EvidenceIDs: []domain.ID{"ev-1"}, MaxDepth: 2})
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want only the directly supported candidate", len(got))
	}
	if got[0].Location.SymbolIdentity != test.Identity || len(rels) == 0 {
		t.Fatalf("unexpected candidates/relationships: %#v %#v", got, rels)
	}
}

func TestGeneratePromotesFailingTestCalleeAsInferredCallee(t *testing.T) {
	// A test calling k.Float64() across a package boundary is indexed as a
	// TESTS edge (provenance inferred_caller). The failing test's main-code
	// callee must become a candidate so the implementation file can be found.
	test := domain.ProgramNode{ID: "test", Type: "Test", Name: "TestFloat64Overflow", FilePath: "tests/numbers_test.go", StartLine: 207, EndLine: 220, RevisionID: "rev", RepositoryID: "repo", Identity: "go:tests/numbers_test.go:Test:TestFloat64Overflow"}
	impl := domain.ProgramNode{ID: "impl", Type: "Method", Name: "Float64", FilePath: "getters.go", StartLine: 242, EndLine: 260, RevisionID: "rev", RepositoryID: "repo", Identity: "go:getters.go:Method:Koanf.Float64"}
	s := programrepo.Snapshot{
		RepositoryID: "repo",
		RevisionID:   "rev",
		Nodes:        []domain.ProgramNode{test, impl},
		Relationships: []domain.Relationship{
			{ID: "inferred-test", SourceID: "test", SourceType: "Test", TargetID: "impl", TargetType: "Method", Type: "TESTS", RevisionID: "rev", CreatedAt: time.Now(), Metadata: map[string]string{"provenance": "inferred_caller"}},
		},
	}
	got, _ := Generate(s, Input{InvestigationID: "inv", FailureIDs: []string{"failure-1"}, FailureText: "TestFloat64Overflow", EvidenceIDs: []domain.ID{"ev-1"}, BehaviouralEvidenceIDs: []domain.ID{"bev-1"}, MaxDepth: 2})
	var implCandidate *domain.CandidateLocation
	for i := range got {
		if got[i].Location.SymbolIdentity == impl.Identity {
			implCandidate = &got[i]
		}
	}
	if implCandidate == nil {
		t.Fatalf("failing test's main-code callee was not promoted: %#v", got)
	}
	if implCandidate.Location.Provenance != "inferred_callee" {
		t.Fatalf("callee provenance = %q, want inferred_callee: %#v", implCandidate.Location.Provenance, implCandidate)
	}
	if len(implCandidate.Score.AvailableDimensions) == 0 {
		t.Fatalf("callee has no scoring evidence: %#v", implCandidate)
	}
}

func TestGenerateObservedCandidatesWithoutSnapshot(t *testing.T) {
	frames := []Frame{{File: "pkg/handler.go", Line: 142, FailureIDs: []string{"failure-1"}, EvidenceIDs: []domain.ID{"e1"}}}
	candidates, relations := GenerateObserved(frames, Input{
		InvestigationID: "inv",
		RepositoryID:    "repo",
		RevisionID:      "revision:head",
		EvidenceIDs:     []domain.ID{"e1"},
		MaxCandidates:   10,
	})
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Location.LocationType != "observed_frame" {
		t.Fatalf("location type = %q, want observed_frame", candidate.Location.LocationType)
	}
	if candidate.Location.FilePath != "pkg/handler.go" || candidate.Location.StartLine != 142 {
		t.Fatalf("unexpected observed location: %#v", candidate.Location)
	}
	if len(candidate.SupportingEvidenceIDs) != 1 || candidate.SupportingEvidenceIDs[0] != "e1" {
		t.Fatalf("candidate evidence = %#v", candidate.SupportingEvidenceIDs)
	}
	if len(relations) != 1 || relations[0].Type != "MANIFESTED_AT" {
		t.Fatalf("relations = %#v", relations)
	}
}

func TestGenerateObservedCandidateIDsAreInvestigationScoped(t *testing.T) {
	frame := []Frame{{File: "pkg/handler.go", Line: 142}}
	first, _ := GenerateObserved(frame, Input{InvestigationID: "inv-1", RepositoryID: "repo", RevisionID: "rev", MaxCandidates: 10})
	second, _ := GenerateObserved(frame, Input{InvestigationID: "inv-2", RepositoryID: "repo", RevisionID: "rev", MaxCandidates: 10})
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one candidate per investigation: %#v %#v", first, second)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("candidate IDs collided: %q", first[0].ID)
	}
}

func TestGenerateMapsFrameToOnlyItsEnclosingSymbol(t *testing.T) {
	snapshot := programrepo.Snapshot{RepositoryID: "repo", RevisionID: "rev", Nodes: []domain.ProgramNode{
		{ID: "outer", Type: "Function", Name: "Outer", FilePath: "cache.go", StartLine: 10, EndLine: 30, Identity: "outer"},
		{ID: "inner", Type: "Function", Name: "Inner", FilePath: "cache.go", StartLine: 20, EndLine: 22, Identity: "inner"},
	}}
	got, _ := Generate(snapshot, Input{InvestigationID: "inv", RepositoryID: "repo", RevisionID: "rev", StackFrames: []Frame{{File: "cache.go", Line: 21, EvidenceIDs: []domain.ID{"frame"}}}, MaxCandidates: 10})
	if len(got) != 1 || got[0].Location.SymbolIdentity != "inner" {
		t.Fatalf("frame mapped to broad or multiple symbols: %#v", got)
	}
	if got[0].Score.ExecutionRelevance == 0 || got[0].Location.Provenance != "enclosing_symbol" {
		t.Fatalf("direct frame attribution missing: %#v", got[0])
	}
}

func TestGenerateFrameMatchingOnlyFileNodeDoesNotPanic(t *testing.T) {
	// A frame whose line is only covered by the whole-file node (no enclosing
	// symbol) must not be attributed to that File node or crash. File nodes
	// span the entire file and are not eligible enclosing symbols.
	snapshot := programrepo.Snapshot{RepositoryID: "repo", RevisionID: "rev", Nodes: []domain.ProgramNode{
		{ID: "context.go", Type: "File", Name: "context.go", FilePath: "context.go", StartLine: 1, EndLine: 300, Identity: "go:context.go"},
	}}
	got, _ := Generate(snapshot, Input{InvestigationID: "inv", RepositoryID: "repo", RevisionID: "rev", StackFrames: []Frame{{File: "context.go", Line: 135, EvidenceIDs: []domain.ID{"frame"}}}, MaxCandidates: 10})
	if len(got) != 0 {
		t.Fatalf("file node produced candidate locations: %#v", got)
	}
}

func TestPackageAndChangedEvidenceDoNotCreateExecutionScore(t *testing.T) {
	snapshot := programrepo.Snapshot{RepositoryID: "repo", RevisionID: "rev", Nodes: []domain.ProgramNode{
		{ID: "changed", Type: "Function", Name: "Changed", FilePath: "cache.go", StartLine: 40, EndLine: 45, Identity: "changed", Changed: true},
	}}
	got, _ := Generate(snapshot, Input{InvestigationID: "inv", RepositoryID: "repo", RevisionID: "rev", ChangedIdentities: map[string]bool{"changed": true}, FailureText: "cache package failed", EvidenceIDs: []domain.ID{"ci"}, MaxCandidates: 10})
	if len(got) != 0 {
		t.Fatalf("unconnected changed symbol became candidate: %#v", got)
	}
}

func TestScoreEvidenceStaysInItsDimension(t *testing.T) {
	changed := domain.ProgramNode{ID: "changed", Type: "Function", Name: "Changed", FilePath: "cache.go", StartLine: 10, EndLine: 20, Identity: "changed", Changed: true}
	utility := domain.ProgramNode{ID: "utility", Type: "Function", Name: "Utility", FilePath: "cache.go", StartLine: 30, EndLine: 40, Identity: "utility"}
	snapshot := programrepo.Snapshot{RepositoryID: "repo", RevisionID: "rev", Nodes: []domain.ProgramNode{changed, utility}, Relationships: []domain.Relationship{{ID: "call", SourceID: "changed", TargetID: "utility", Type: "CALLS"}}}
	got, _ := Generate(snapshot, Input{InvestigationID: "inv", RepositoryID: "repo", RevisionID: "rev", StackFrames: []Frame{{File: "cache.go", Line: 12, EvidenceIDs: []domain.ID{"frame"}}}, EvidenceIDs: []domain.ID{"ci"}, ChangeEvidenceIDs: []domain.ID{"diff"}, ChangedIdentities: map[string]bool{"changed": true}, MaxCandidates: 10})
	var direct, expanded *domain.CandidateLocation
	for i := range got {
		if got[i].Location.SymbolIdentity == changed.Identity {
			direct = &got[i]
		}
		if got[i].Location.SymbolIdentity == utility.Identity {
			expanded = &got[i]
		}
	}
	if direct == nil || direct.Score.ChangeRelevance == 0 {
		t.Fatalf("changed evidence did not reach direct candidate: %#v", got)
	}
	if gotIDs := direct.Score.EvidenceByDimension["change"]; len(gotIDs) != 1 || gotIDs[0] != "diff" {
		t.Fatalf("change evidence reused unrelated IDs: %#v", direct.Score.EvidenceByDimension)
	}
	if direct.Score.EvidenceByDimension["execution"][0] != "frame" {
		t.Fatalf("execution evidence was not frame-scoped: %#v", direct.Score.EvidenceByDimension)
	}
	if expanded != nil && expanded.Score.EvidenceByDimension["dependency"][0] == "ci" {
		t.Fatalf("dependency evidence reused CI evidence: %#v", expanded.Score.EvidenceByDimension)
	}
}

func TestSignalsRequireDimensionSpecificEvidence(t *testing.T) {
	score := evaluateSignals(CandidateContext{
		Execution:          true,
		Changed:            true,
		Expanded:           true,
		Behavioural:        true,
		Evidence:           []domain.ID{"ci-stack"},
		ChangeEvidence:     nil,
		DependencyEvidence: nil,
	})
	if score.ExecutionRelevance == 0 || score.EvidenceByDimension["execution"][0] != "ci-stack" {
		t.Fatalf("execution evidence missing: %#v", score)
	}
	if score.ChangeRelevance != 0 || score.DependencyRelevance != 0 || contains(score.AvailableDimensions, "change") || contains(score.AvailableDimensions, "dependency") {
		t.Fatalf("unrelated CI evidence increased change/dependency relevance: %#v", score)
	}
}

// TestGenerateOrdersObservedFramesDeterministically guards against Go map
// iteration shuffling equally-scored stack frames: the primary suspect among
// observed frames must be stable across runs and must prefer the innermost
// frame (listed first in the failure stack).
func TestGenerateOrdersObservedFramesDeterministically(t *testing.T) {
	s := programrepo.Snapshot{
		RepositoryID: "repo", RevisionID: "rev",
		Nodes: []domain.ProgramNode{
			{ID: "getValue", Type: "Method", Name: "getValue", FilePath: "tree.go", StartLine: 400, EndLine: 460, RevisionID: "rev", RepositoryID: "repo", Identity: "go:tree.go:Method:node.getValue"},
			{ID: "handle", Type: "Method", Name: "handleHTTPRequest", FilePath: "gin.go", StartLine: 700, EndLine: 790, RevisionID: "rev", RepositoryID: "repo", Identity: "go:gin.go:Method:Engine.handleHTTPRequest"},
			{ID: "serve", Type: "Method", Name: "ServeHTTP", FilePath: "gin.go", StartLine: 640, EndLine: 699, RevisionID: "rev", RepositoryID: "repo", Identity: "go:gin.go:Method:Engine.ServeHTTP"}},
	}
	input := Input{
		InvestigationID: "inv", FailureIDs: []string{"f1"}, EvidenceIDs: []domain.ID{"e1"},
		FailureText: "TestLateRouteRegistrationSkippedNodes",
		StackFrames: []Frame{
			{File: "tree.go", Line: 443, Function: "node.getValue", EvidenceIDs: []domain.ID{"e1"}},
			{File: "gin.go", Line: 775, Function: "Engine.handleHTTPRequest", EvidenceIDs: []domain.ID{"e1"}},
			{File: "gin.go", Line: 690, Function: "Engine.ServeHTTP", EvidenceIDs: []domain.ID{"e1"}},
		},
	}
	var first []string
	for run := 0; run < 8; run++ {
		got, _ := Generate(s, input)
		if len(got) < 3 {
			t.Fatalf("expected three observed candidates, got %d", len(got))
		}
		order := []string{got[0].Location.FilePath, got[1].Location.FilePath, got[2].Location.FilePath}
		if got[0].Location.SymbolIdentity != "go:tree.go:Method:node.getValue" {
			t.Fatalf("innermost frame not first: %#v", order)
		}
		if first == nil {
			first = order
		} else if strings.Join(first, ",") != strings.Join(order, ",") {
			t.Fatalf("candidate order not deterministic: %v vs %v", first, order)
		}
	}
}
