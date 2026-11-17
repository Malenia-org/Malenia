package diagnosis

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/causent/malenia/internal/domain"
	programrepo "github.com/causent/malenia/internal/repository"
)

func TestAssertionSlicePrefersValueProducerOverSetupAndSibling(t *testing.T) {
	root := t.TempDir()
	text := `package example
func TestValue(t *testing.T) {
  setup := New()
  _ = setup
  got := Produce()
  if got != "expected" {
    t.Errorf("unexpected: %s", got)
  }
}
func TestOther(t *testing.T) { Other() }
`
	if err := os.WriteFile(filepath.Join(root, "example_test.go"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	test := domain.ProgramNode{ID: "test", Name: "TestValue", FilePath: "example_test.go", Type: "Test"}
	producer := domain.ProgramNode{ID: "produce", Name: "Produce", FilePath: "producer.go", Type: "Function"}
	setup := domain.ProgramNode{ID: "new", Name: "New", FilePath: "setup.go", Type: "Function"}
	snapshot := programrepo.Snapshot{Nodes: []domain.ProgramNode{test, producer, setup}, Relationships: []domain.Relationship{
		{SourceID: "test", TargetID: "produce", Type: "CALLS", Metadata: map[string]string{"resolution": "go_types", "call_lines": ",5,"}},
		{SourceID: "test", TargetID: "new", Type: "CALLS", Metadata: map[string]string{"resolution": "go_types", "call_lines": ",3,"}},
	}}
	report := &Report{Baseline: Baseline{Results: []Attempt{{Stderr: "example_test.go:7: unexpected value"}}}}
	ops := assertionOperations(report, root, test, snapshot)
	if len(ops) != 1 || ops[0].ID != producer.ID {
		t.Fatalf("wrong assertion slice: %#v", ops)
	}
}

func TestReachableSourceFilesPrecedeNoisyRankedCandidates(t *testing.T) {
	report := &Report{NormalizedEvidence: &NormalizedEvidence{}, CausalChain: &DeterministicCausalChain{Status: "localized", Steps: []DeterministicCausalStep{{Kind: "assertion_dependency", File: "actual/producer.go", EvidenceID: "source"}}}}
	for i := 0; i < 10; i++ {
		report.Candidates = append(report.Candidates, domain.CandidateLocation{Location: domain.CodeLocation{FilePath: "noise.go", Provenance: "enclosing_symbol"}})
	}
	refineEvidenceLikelyFiles(report)
	if len(report.Localization.LikelyFiles) == 0 || report.Localization.LikelyFiles[0] != "actual/producer.go" {
		t.Fatalf("lost verified source: %#v", report.Localization)
	}
}
