package diagnosis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Malenia-org/ceaser/internal/domain"
	"github.com/Malenia-org/ceaser/internal/execution"
	programrepo "github.com/Malenia-org/ceaser/internal/repository"
)

// No SSA, implementation stack frame or coverage is available. An exact
// localized function should still be testable as a hypothesis when opted in.
func TestLocalizedFunctionReachesRealProbeWithoutStaticAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and executes an instrumented test binary")
	}
	root := t.TempDir()
	for name, source := range map[string]string{
		"go.mod":   "module example.com/localized\n\ngo 1.26\n",
		"value.go": "package localized\nfunc Uncalled() int { return 99 }\nfunc Convert(x float64) int {\n return int(x)\n}\n",
		"value_test.go": `package localized
import "testing"
func TestConvert(t *testing.T) {
 invoke := Convert
 got := invoke(42.5)
 if got != 43 {
  t.Fatalf("expected: 43\nactual: %v", got)
 }
}
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	original := "=== RUN   TestConvert\n    value_test.go:7: expected: 43\n        actual: 42\n--- FAIL: TestConvert (0.00s)\nFAIL\n"
	report := &Report{
		InvestigationID: "localized-probe", Environment: Environment{HeadCommit: "fixture-rev"},
		Command:  []string{"go", "test", "-run", "^TestConvert$", "."},
		Evidence: []EvidenceItem{{ID: "ci", Kind: "ci_log", Detail: original}},
	}
	snapshot := programrepo.Snapshot{Nodes: []domain.ProgramNode{
		{Type: "Function", FilePath: "value.go", Name: "Uncalled", Identity: "go:value.go:Function:Uncalled", StartLine: 2, EndLine: 2},
		{Type: "Function", FilePath: "value.go", Name: "Convert", Identity: "go:value.go:Function:Convert", StartLine: 3, EndLine: 5},
	}}
	candidates := []domain.CandidateLocation{
		{Location: domain.CodeLocation{FilePath: "value.go", SymbolIdentity: "go:value.go:Function:Uncalled", StartLine: 2, EndLine: 2}},
		{Location: domain.CodeLocation{FilePath: "value.go", SymbolIdentity: "go:value.go:Function:Convert", StartLine: 3, EndLine: 5}},
	}
	policy := execution.Policy{
		AllowedCommands: map[string]bool{"go": true, "sh": true}, AllowedDirPrefixes: []string{os.TempDir()},
		AllowedEnviron: map[string]bool{"PATH": true, "HOME": true, "GOCACHE": true, "GOMODCACHE": true,
			"GOPATH": true, "GOROOT": true, "GOENV": true, "GOFLAGS": true, "GOTOOLCHAIN": true,
			"TMPDIR": true, "USER": true, "SHELL": true}, MaxOutput: 1 << 20, Timeout: 120 * time.Second,
	}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Timeout: 120 * time.Second,
		Executor: execution.NewLocalExecutor(policy, execution.NewArtifactFactory(t.TempDir()))}}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, root, snapshot, candidates)
	if obs == nil || obs.Status != "observed" || obs.BinaryDigest == "" || !obs.TestFailed || !obs.ValueMatched {
		t.Fatalf("localized function did not reach an executed observation: %#v", obs)
	}
	if len(obs.Attempts) != 2 || obs.Attempts[0].Preserved || obs.Attempts[0].Reason != "missing_operation_marker" ||
		!obs.Attempts[1].Preserved || len(obs.Attempts[1].Runs) != 3 || obs.OperationName != "Convert" {
		t.Fatalf("original test/check not preserved across three executions: %#v", obs.Attempts)
	}
	if obs.SSADataflowVerified || assessRootCause(*report).Status == "established" {
		t.Fatal("source-only localization was promoted to causal proof")
	}
	if obs.SelectionReason != "localized_function_hypothesis" || obs.Attempts[1].SelectionReason != obs.SelectionReason ||
		!strings.Contains(strings.Join(obs.Limitations, " "), "selection is not causal proof") {
		t.Fatalf("hypothesis provenance missing: %#v", obs)
	}
	originalSource, err := os.ReadFile(filepath.Join(root, "value.go"))
	if err != nil || strings.Contains(string(originalSource), runtimeProbeHelper) {
		t.Fatal("runtime probe changed the original source workspace")
	}
}

func TestLocalizedProbeFallbackIsScopedAndFair(t *testing.T) {
	root := t.TempDir()
	source := `package example
func Branchy(x int) int {
 if x == 0 { return 0 }
 if x == 1 { return 1 }
 if x == 2 { return 2 }
 return 3
}
func Relevant(x int) int {
 return x + 1
}
func Unrelated() int { return 99 }
`
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := programrepo.Snapshot{Nodes: []domain.ProgramNode{
		{Type: "Function", FilePath: "value.go", Name: "Branchy", Identity: "branchy", StartLine: 2, EndLine: 7},
		{Type: "Function", FilePath: "value.go", Name: "Relevant", Identity: "relevant", StartLine: 8, EndLine: 10},
		{Type: "Function", FilePath: "value.go", Name: "Unrelated", Identity: "unrelated", StartLine: 11, EndLine: 11},
	}}
	first := domain.CandidateLocation{Location: domain.CodeLocation{FilePath: "value.go", StartLine: 2, EndLine: 7, SymbolIdentity: "branchy"}}
	second := domain.CandidateLocation{Location: domain.CodeLocation{FilePath: "value.go", StartLine: 8, EndLine: 10, SymbolIdentity: "relevant"}}
	got := localizedFunctionProbeOperations(root, snapshot, []domain.CandidateLocation{first, first, second})
	if len(got) != 5 || got[0].Name != "Branchy" || got[1].Name != "Relevant" {
		t.Fatalf("later candidate starved or duplicate retained: %#v", got)
	}
	for _, operation := range got {
		if operation.Name == "Unrelated" || probeSSADataflowSupports(&Report{}, operation) {
			t.Fatalf("unlocalized function or SSA proof admitted: %#v", operation)
		}
	}
	for name, change := range map[string]func(*domain.CodeLocation){
		"file only":        func(loc *domain.CodeLocation) { loc.StartLine = 0 },
		"wrong identity":   func(loc *domain.CodeLocation) { loc.SymbolIdentity = "unrelated" },
		"outside function": func(loc *domain.CodeLocation) { loc.EndLine = 11 },
		"another package":  func(loc *domain.CodeLocation) { loc.FilePath = "other/value.go" },
		"path traversal":   func(loc *domain.CodeLocation) { loc.FilePath = "../value.go" },
		"test function":    func(loc *domain.CodeLocation) { loc.FilePath = "value_test.go" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := first
			change(&candidate.Location)
			if result := localizedFunctionProbeOperations(root, snapshot, []domain.CandidateLocation{candidate}); len(result) != 0 {
				t.Fatalf("invalid localization selected: %#v", result)
			}
		})
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "value.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "value.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "value.go"), filepath.Join(root, "value.go")); err != nil {
		t.Fatal(err)
	}
	if result := localizedFunctionProbeOperations(root, snapshot, []domain.CandidateLocation{first}); len(result) != 0 {
		t.Fatalf("symlink escaped workspace: %#v", result)
	}
}

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

func TestLocalizedProbeRejectsAmbiguousMethodRange(t *testing.T) {
	root := t.TempDir()
	source := "package example\ntype A int\ntype B int\nfunc (A) Value() int { return 1 }; func (B) Value() int { return 2 }\n"
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := programrepo.Snapshot{Nodes: []domain.ProgramNode{{
		Type: "Method", FilePath: "value.go", Name: "Value", Identity: "A.Value", StartLine: 4, EndLine: 4,
	}}}
	candidates := []domain.CandidateLocation{{Location: domain.CodeLocation{
		FilePath: "value.go", StartLine: 4, EndLine: 4, SymbolIdentity: "A.Value",
	}}}
	if got := localizedFunctionProbeOperations(root, snapshot, candidates); len(got) != 0 {
		t.Fatalf("same-name methods selected from an ambiguous line range: %#v", got)
	}
}
