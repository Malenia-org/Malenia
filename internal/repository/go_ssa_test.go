package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSAAssertionTracesProducerArgumentsWithoutUnrelatedCalls(t *testing.T) {
	root := t.TempDir()
	source := `package sample
import "testing"
func Unrelated() int { return 99 }
func Transform(x int) int { return x * 2 }
func TestValue(t *testing.T) {
  _ = Unrelated()
  got := Transform(4)
  if got != 9 {
    t.Errorf("unexpected %d", got)
  }
}
`
	for name, data := range map[string]string{"go.mod": "module example.com/sample\n\ngo 1.23\n", "value_test.go": source} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := (Analyzer{}).Analyze(context.Background(), root, "repo", "rev", "head")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GoSSA == nil {
		t.Fatal("missing SSA graph")
	}
	trace := snapshot.GoSSA.TraceAssertion("value_test.go", 9)
	if trace.Status != "traced" {
		t.Fatalf("trace failed: %+v graph limitations=%v", trace, snapshot.GoSSA.Limitations)
	}
	found := false
	foundArgument := false
	foundCondition := false
	for _, value := range trace.Values {
		if strings.Contains(value.Function, "Unrelated") {
			t.Fatal("unrelated call leaked into assertion slice")
		}
		if strings.Contains(value.Function, "Transform") && strings.Contains(value.Kind, "BinOp") {
			found = true
		}
		if value.Kind == "constant" && value.Text == "4:int" {
			foundArgument = true
		}
		if strings.Contains(value.Kind, "BinOp") && strings.Contains(value.Text, "9:int") {
			foundCondition = true
		}
	}
	if !found {
		t.Fatalf("did not trace into producer: %+v", trace)
	}
	if !foundArgument || !foundCondition {
		t.Fatalf("argument binding or branch predicate missing: %+v", trace)
	}
}

func TestSSATraceBoundsCyclesAndPreservesPhiUncertainty(t *testing.T) {
	graph := &GoSSA{Functions: map[string]SSAFunction{"f": {ID: "f", File: "f.go", StartLine: 1, EndLine: 10, Instructions: []string{"assert"}}}, Values: map[string]SSAValue{
		"assert": {ID: "assert", Function: "f", File: "f.go", Line: 8, Kind: "*ssa.Call", Operands: []string{"phi"}},
		"phi":    {ID: "phi", Function: "f", File: "f.go", Kind: "*ssa.Phi", Operands: []string{"phi"}, Gap: "branch feasibility unresolved"},
	}}
	trace := graph.TraceAssertion("f.go", 8)
	if trace.Visited > 3 || len(trace.Limitations) == 0 {
		t.Fatalf("cycle or uncertainty lost: %+v", trace)
	}
}
