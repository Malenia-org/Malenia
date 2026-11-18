package diagnosis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Malenia-org/ceaser/internal/domain"
	programrepo "github.com/Malenia-org/ceaser/internal/repository"
)

func TestSourceProbeUsesCoveredReturnInsideSwitch(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "value.go")
	const source = `package example
func value(input any, fallback int) int {
	switch number := input.(type) {
	case float64:
		return int(number)
	default:
		return fallback
	}
}
`
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	loc := domain.CodeLocation{FilePath: "value.go", StartLine: 2, EndLine: 9}
	candidates := []domain.CandidateLocation{{Location: loc}}
	snapshot := programrepo.Snapshot{Nodes: []domain.ProgramNode{{FilePath: "value.go", Name: "value", StartLine: 2, EndLine: 9}}}
	report := &Report{Evidence: []EvidenceItem{{
		Kind: "coverage_report", ArtifactFormat: "lcov",
		Detail: "SF:value.go\nDA:5,1\nDA:7,0\nend_of_record\n",
	}}}
	op := selectSourceProbeOperation(report, root, snapshot, candidates)
	if op == nil || op.Line != 5 {
		t.Fatalf("expected covered return without SSA, got %#v", op)
	}
	if _, _, err := injectOperationProbe(file, op); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); strings.Count(got, "int(number)") != 1 || !strings.Contains(got, "return fallback") || !strings.Contains(got, runtimeProbeHelper) {
		t.Fatalf("switch rewrite should capture only the conversion: %s", got)
	}
}

func TestProbePrefersComputedResultOverFallbackParameter(t *testing.T) {
	slot := 0
	report := &Report{SSAAssertionTrace: &programrepo.SSATrace{Values: []programrepo.SSAValue{
		{ID: "fallback", Function: "sample.Value", Kind: "*ssa.Return", File: "value.go", Line: 7, Column: 2, TraceSlot: &slot, Operands: []string{"param"}},
		{ID: "result", Function: "sample.Value", Kind: "*ssa.Return", File: "value.go", Line: 5, Column: 2, TraceSlot: &slot, Operands: []string{"convert"}},
		{ID: "param", Kind: "parameter"},
		{ID: "convert", Kind: "*ssa.Convert"},
	}}}
	op := selectProbeOperation(report, programrepo.Snapshot{}, nil)
	if op == nil || op.ID != "result" {
		t.Fatalf("expected computed return, got %#v", op)
	}
}

func TestProbeLoopTriesNextCandidateAfterInstrumentationFailure(t *testing.T) {
	report := probeReport()
	report.SSAAssertionTrace = &programrepo.SSATrace{Values: []programrepo.SSAValue{
		{ID: "missing-source", File: "pkg/absent.go", Line: 1, Function: "sample.Missing", Kind: "*ssa.Call"},
		{ID: "convert-value", File: "pkg/convert.go", Line: 24, Function: "example.com/probe/pkg.Convert", Kind: "*ssa.Call"},
	}}
	executor := &fakeProbeExecutor{output: "--- FAIL: TestX\nexpected: 0\nactual: +Inf\n" + probeEvent("+Inf")}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: executor, Timeout: time.Second}}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, probeWorkspace(t, ""), probeSnapshot(), nil)
	if obs.OperationID != "convert-value" || len(obs.Attempts) != 2 ||
		obs.Attempts[0].Reason != "instrumentation" || len(obs.Attempts[1].Runs) != 3 || len(executor.calls) != 4 {
		t.Fatalf("expected one skipped candidate and one build with three executions: %#v", obs)
	}
}
