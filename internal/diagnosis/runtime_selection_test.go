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
	for name, evidence := range map[string]EvidenceItem{
		"go coverage":  {Kind: "coverage_report", ArtifactFormat: "go_coverprofile", Detail: "mode: count\nexample.org/repo/value.go:5.3,5.21 1 1\nexample.org/repo/value.go:7.3,7.18 1 0\n"},
		"runner stack": {Kind: "original_ci_log", Detail: "  /home/runner/work/repo/value.go:4\n"},
	} {
		t.Run(name, func(t *testing.T) {
			found := selectSourceProbeOperation(&Report{Evidence: []EvidenceItem{evidence}}, root, snapshot, candidates)
			if found == nil || found.Line != 5 {
				t.Fatalf("expected implementation return from %s, got %#v", name, found)
			}
		})
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

func TestLocalizedFunctionSuppliesSSAWithoutAssertionTrace(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{
		"go.mod":        "module example.com/candidate\n\ngo 1.26\n",
		"value.go":      "package candidate\nfunc Convert(x float64) int {\n return int(x)\n}\nfunc Unrelated() int { return 99 }\n",
		"value_test.go": "package candidate\nimport \"testing\"\nfunc TestOther(t *testing.T) { t.Log(\"no static call to Convert\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := (programrepo.Analyzer{TargetTests: []string{"TestOther"}}).Analyze(context.Background(), root, "repo", "rev", "head")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []domain.CandidateLocation{{Location: domain.CodeLocation{FilePath: "value.go", StartLine: 2, EndLine: 4}}}
	report := &Report{}
	operations := rankedProbeOperations(report, snapshot, candidates)
	if len(operations) == 0 || operations[0].Name != "Convert" || operations[0].Line != 3 || operations[0].Slot != 1 {
		t.Fatalf("localized implementation not available to selector: %#v", operations)
	}
	for _, operation := range operations {
		if operation.Name == "Unrelated" || probeSSADataflowSupports(report, operation) {
			t.Fatalf("candidate selection admitted unrelated code or invented assertion dataflow: %#v", operation)
		}
	}
	if got := rankedProbeOperations(report, snapshot, nil); len(got) != 0 {
		t.Fatalf("source graph selected without localization: %#v", got)
	}
	wrong := []domain.CandidateLocation{{Location: domain.CodeLocation{FilePath: "other/value.go", StartLine: 2}}}
	if got := rankedProbeOperations(report, snapshot, wrong); len(got) != 0 {
		t.Fatalf("same basename in another package was matched: %#v", got)
	}
}

func TestProbeMixPreservesFallbackAndDeduplicates(t *testing.T) {
	var ssa []*probeOperation
	for i := 1; i <= 8; i++ {
		ssa = append(ssa, &probeOperation{File: "a.go", Line: i, Slot: 1})
	}
	fallback := &probeOperation{File: "b.go", Line: 10, Slot: 1}
	got := mixProbeOperations(ssa, []*probeOperation{ssa[0], fallback})
	if len(got) != 9 || got[2] != fallback {
		t.Fatalf("fallback starved or duplicates retained: %#v", got)
	}
}

func TestProbeSourcePathRejectsAmbiguousBasenames(t *testing.T) {
	snapshot := programrepo.Snapshot{Nodes: []domain.ProgramNode{
		{FilePath: "one/value.go"}, {FilePath: "two/value.go"},
	}}
	if got := probeSourcePath("/home/runner/work/repo/one/value.go", snapshot); got != "one/value.go" {
		t.Fatalf("absolute runner path unresolved: %s", got)
	}
	if got := probeSourcePath("example.org/repo/two/value.go", snapshot); got != "two/value.go" {
		t.Fatalf("module coverage path unresolved: %s", got)
	}
	if got := probeSourcePath("value.go", snapshot); got != "" {
		t.Fatalf("ambiguous basename resolved: %s", got)
	}
}

func TestLocalizedReturnRetainsUntracedResultSlot(t *testing.T) {
	slot := 0
	value := programrepo.SSAValue{ID: "return", File: "value.go", Function: "sample.Value",
		Line: 3, Column: 2, Kind: "*ssa.Return", Operands: []string{"value", "err"}}
	traced := value
	traced.TraceSlot = &slot
	report := &Report{SSAAssertionTrace: &programrepo.SSATrace{Values: []programrepo.SSAValue{traced}}}
	snapshot := programrepo.Snapshot{GoSSA: &programrepo.GoSSA{
		Functions: map[string]programrepo.SSAFunction{"sample.Value": {
			ID: "sample.Value", File: "value.go", StartLine: 2, EndLine: 4, Instructions: []string{"return"},
		}},
		Values: map[string]programrepo.SSAValue{"return": value},
	}}
	candidates := []domain.CandidateLocation{{Location: domain.CodeLocation{FilePath: "value.go", StartLine: 2, EndLine: 4}}}
	got := rankedProbeOperations(report, snapshot, candidates)
	if len(got) != 2 || got[0].Slot != 1 || got[1].Slot != 2 {
		t.Fatalf("missing or duplicate return slots: %#v", got)
	}
}
