package diagnosis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/causent/malenia/internal/domain"
	"github.com/causent/malenia/internal/localization"
	programrepo "github.com/causent/malenia/internal/repository"
)

// buildNeutralSnapshot builds a generic snapshot for a test calling a method
// that in turn calls a helper — no repository- or fixture-specific names.
func buildNeutralSnapshot(t *testing.T) (string, programrepo.Snapshot, domain.CandidateLocation) {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pkg.go", `package pkg
func Convert(v string) string { return helper(v) }
func helper(v string) string { return v }
`)
	write("case_test.go", `package pkg
import "testing"
func TestConvertOverflow(t *testing.T) {
	if Convert("x") != "0" { t.Fatal("bad") }
}
`)
	s, err := (programrepo.Analyzer{}).Analyze(context.Background(), root, "repo", "rev", "head")
	if err != nil {
		t.Fatal(err)
	}
	var testID string
	for _, n := range s.Nodes {
		if n.Type == "Test" && n.Name == "TestConvertOverflow" {
			testID = n.ID
		}
	}
	if testID == "" {
		t.Fatal("neutral test node was not indexed")
	}
	cands, _ := localization.Generate(s, localization.Input{
		InvestigationID: "inv", FailureIDs: []string{"f"}, FailureText: "TestConvertOverflow",
		EvidenceIDs: []domain.ID{"e"}, BehaviouralEvidenceIDs: []domain.ID{"b"}, MaxDepth: 2,
	})
	for i := range cands {
		if cands[i].ID == "candidate:"+testID {
			cands[i].Location.Provenance = "enclosing_symbol"
			return root, s, cands[i]
		}
	}
	t.Fatal("neutral test candidate was not generated")
	return "", programrepo.Snapshot{}, domain.CandidateLocation{}
}

func TestDeterministicCausalChainEstablished(t *testing.T) {
	root, s, testCandidate := buildNeutralSnapshot(t)
	report := &Report{
		InvestigationID:  "inv",
		Environment:      Environment{HeadCommit: "head"},
		FailureIncidents: []FailureIncident{{Category: "test_failure_observed", AffectedTests: []string{"TestConvertOverflow"}}},
		FailureEpisodes:  []domain.FailureEpisode{{TestIdentifier: "TestConvertOverflow/subtest"}},
	}
	buildDeterministicCausalChain(report, root, s, []domain.CandidateLocation{testCandidate})

	chain := report.CausalChain
	if chain == nil || chain.Status != "localized" {
		t.Fatalf("expected localized chain, got %#v", chain)
	}
	if len(chain.Steps) < 2 || len(chain.EvidenceIDs) < 2 {
		t.Fatalf("chain lacks evidence-cited steps: %#v", chain)
	}
	// The unit under test (Convert) must be chosen, not the first call in the file.
	foundConvert := false
	for _, step := range chain.Steps {
		if step.Kind == "called_function" && strings.Contains(step.Statement, "Convert") {
			foundConvert = true
		}
	}
	if !foundConvert {
		t.Fatalf("chain did not resolve the unit-under-test symbol: %#v", chain.Steps)
	}
	// Every cited source evidence ID must exist in the report.
	known := map[string]bool{}
	for _, e := range report.Evidence {
		known[e.ID] = true
	}
	for _, id := range chain.EvidenceIDs {
		if !known[id] {
			t.Fatalf("chain cites unknown evidence %q", id)
		}
	}
}

func TestDeterministicCausalChainSkipsRacesAndUnknownFailures(t *testing.T) {
	root, s, testCandidate := buildNeutralSnapshot(t)
	// Races are handled by the separate race investigation path.
	report := &Report{
		InvestigationID:  "inv",
		Environment:      Environment{HeadCommit: "head"},
		FailureIncidents: []FailureIncident{{Category: "go_data_race"}},
	}
	buildDeterministicCausalChain(report, root, s, []domain.CandidateLocation{testCandidate})
	if report.CausalChain != nil {
		t.Fatalf("race must not use the deterministic chain: %#v", report.CausalChain)
	}
	// No failing test identifier -> no chain.
	report = &Report{InvestigationID: "inv", Environment: Environment{HeadCommit: "head"}, FailureIncidents: []FailureIncident{{Category: "test_failure_observed"}}}
	buildDeterministicCausalChain(report, root, s, []domain.CandidateLocation{testCandidate})
	if report.CausalChain != nil {
		t.Fatalf("failure without a test identifier must not produce a chain: %#v", report.CausalChain)
	}
}

func TestEstablishedChainUpgradesRootCause(t *testing.T) {
	report := Report{
		Classification:   ClassTestFailureObserved,
		FailureIncidents: []FailureIncident{{Category: "test_failure_observed", AffectedTests: []string{"TestConvertOverflow"}, EvidenceIDs: []string{"ci"}}},
		Evidence:         []EvidenceItem{{ID: "ci", Kind: "ci_log"}},
		CausalChain: &DeterministicCausalChain{
			Status: "established",
			Steps: []DeterministicCausalStep{
				{Kind: "failing_test", Statement: "step one", EvidenceID: "e1"},
				{Kind: "called_function", Statement: "step two", EvidenceID: "e2"},
				{Kind: "implementation_branch", Statement: "step three", EvidenceID: "e3"},
			},
			EvidenceIDs: []string{"e1", "e2", "e3"},
		},
	}
	assessment := assessRootCause(report)
	if assessment.Status != "localized" || assessment.CausalityStatus != "reachability_only" {
		t.Fatalf("reachability was incorrectly promoted to a root cause: %#v", assessment)
	}
}
