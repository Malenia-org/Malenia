package diagnosis

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/causent/malenia/internal/domain"
	"github.com/causent/malenia/internal/execution"
	programrepo "github.com/causent/malenia/internal/repository"
)

// fakeProbeExecutor returns a bounded stdout/stderr for the probe command and
// records the commands it was asked to run. It reuses the same Executor
// interface as production, so no parallel execution layer is introduced.
type fakeProbeExecutor struct {
	output     string
	calls      [][]string
	maxOutputs []int
}

func (f *fakeProbeExecutor) Run(_ context.Context, req domain.ExecutionRequest) (domain.ExecutionResult, error) {
	f.calls = append(f.calls, append([]string(nil), req.Command...))
	f.maxOutputs = append(f.maxOutputs, req.MaxOutput)
	// A build request must leave a binary for the probe to digest; otherwise
	// the build step fails before the observation is produced.
	for i, arg := range req.Command {
		if arg == "-o" && i+1 < len(req.Command) {
			_ = os.WriteFile(req.Command[i+1], []byte("probe-binary"), 0o755)
			return domain.ExecutionResult{}, nil
		}
	}
	return domain.ExecutionResult{Stdout: f.output, Stderr: "", ExitCode: 1}, nil
}

func probeReport() *Report {
	return &Report{
		InvestigationID:  "probe-test",
		Environment:      Environment{HeadCommit: "abc123"},
		Command:          []string{"go", "test", "-run", "TestX", "./pkg"},
		Baseline:         Baseline{Results: []Attempt{{Stderr: "expected: 0, actual: +Inf"}}},
		Evidence:         []EvidenceItem{{ID: "ci", Kind: "ci_log", Detail: "expected: 0\nactual  : +Inf\n"}},
		FailureIncidents: []FailureIncident{{ID: "incident-1", Category: "assertion", ObservedExplanation: "expected 0, got +Inf", EvidenceIDs: []string{"ci"}}},
		CausalChain: &DeterministicCausalChain{Status: "localized", Steps: []DeterministicCausalStep{
			{Kind: "failing_test", File: "pkg/x_test.go", Line: 10},
			{Kind: "called_function", File: "pkg/convert.go", Line: 24, Symbol: "Convert"},
			{Kind: "implementation_branch", File: "pkg/convert.go", Line: 24, Symbol: "helper"},
		}},
	}
}

func probeWorkspace(t *testing.T, operation string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package pkg\n\n" + strings.Repeat("// context\n", 20) +
		"func Convert(v string) float64 {\n\tf := helper(v)\n\treturn f\n}\n\n" +
		"func helper(v string) float64 { return 0 }\n"
	_ = operation
	if err := os.WriteFile(filepath.Join(dir, "pkg", "convert.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/probe\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRuntimeObservationDoesNotEstablishFromValueEquality(t *testing.T) {
	dir := probeWorkspace(t, "helper")
	report := probeReport()
	// Even an exact static dependency and equal observed value are insufficient
	// to prove the failing assertion consumed this particular invocation.
	report.SSAAssertionTrace = &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{{File: "pkg/convert.go", ID: "convert-value", Kind: "*ssa.Call", Line: 24, Function: "example.com/probe/pkg.Convert", Text: "t0 = toFloat64(1e1000)"}}}
	exec := &fakeProbeExecutor{output: "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + probeEvent("+Inf") + "FAIL\n"}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, probeSnapshot(), nil)
	if obs == nil {
		t.Fatal("no observation recorded")
	}
	if obs.Status != "observed" || !obs.SignatureMatch || !obs.ValueMatched || !obs.SSADataflowVerified || len(obs.ObservedValues) == 0 {
		t.Fatalf("matching operation was not observed: %#v", obs)
	}
	if obs.BinaryDigest == "" || len(obs.ExecutionCommand) < 4 || obs.ExecutionCommand[0] != "sh" || !strings.Contains(obs.ExecutionCommand[3], "malenia-probe.test") {
		t.Fatalf("digest does not identify the executed binary: %#v", obs)
	}
	if len(obs.EvidenceIDs) == 0 || obs.Revision == "" || len(exec.calls) == 0 {
		t.Fatalf("observation lacks provenance: %#v calls=%v", obs, exec.calls)
	}
	// The probe reuses the configured command and runs through one executor,
	// and every request stays under the standard policy output limit.
	if !strings.Contains(strings.Join(exec.calls[0], " "), "go test") {
		t.Fatalf("probe did not use the configured command: %v", exec.calls[0])
	}
	for _, maxOut := range exec.maxOutputs {
		if maxOut > 64<<10 {
			t.Fatalf("probe requested output above executor policy limit: %d", maxOut)
		}
	}
	assessment := assessRootCause(*report)
	if assessment.Status == "established" {
		t.Fatalf("value equality established root cause: %#v", assessment)
	}
	for _, item := range report.Evidence {
		if item.ClaimKind == "verified_failure_mechanism" {
			t.Fatal("uncorrelated observation minted mechanism proof")
		}
	}

}

// TestRuntimeObservationEndToEnd uses the real LocalExecutor to compile and
// run an instrumented Go module. It proves the probe is an end-to-end
// capability (workspace copy, AST injection, helper placement, command run,
// binary digest, signature match), not just a reporting helper.
func TestRuntimeObservationEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Go module")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	mod := "module example.com/probe\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	// The function under investigation returns +Inf; the injected regression
	// test records expected: 0, actual: +Inf. Compute the declaration line so
	// the fixture stays correct if the source layout changes.
	src := "package pkg\n\nimport \"math\"\n\n" + strings.Repeat("// context\n", 20) +
		"func toFloat64(s string) float64 {\n\tf := parse(s)\n\treturn f\n}\n\nfunc parse(s string) float64 { return math.Inf(1) }\n"
	if err := os.WriteFile(filepath.Join(root, "pkg", "convert.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	opLine := 0
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "f := parse") {
			opLine = i + 1
		}
	}
	test := "package pkg\n\nimport \"testing\"\n\nfunc TestConvert(t *testing.T) {\n\tgot := toFloat64(\"1e1000\")\n\tif got != 0 {\n\t\tt.Errorf(\"expected: 0\\nactual  : %v\", got)\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(root, "pkg", "convert_test.go"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}

	report := &Report{
		InvestigationID:  "e2e-probe",
		Environment:      Environment{HeadCommit: "e2e-rev"},
		Command:          []string{"go", "test", "-run", "TestConvert", "./pkg"},
		Evidence:         []EvidenceItem{{ID: "ci", Kind: "ci_log", Detail: "expected: 0\nactual  : +Inf\n"}},
		FailureIncidents: []FailureIncident{{ID: "inc", Category: "assertion", EvidenceIDs: []string{"ci"}}},
		CausalChain: &DeterministicCausalChain{Status: "localized", Steps: []DeterministicCausalStep{
			{Kind: "implementation_branch", File: "pkg/convert.go", Line: opLine, Symbol: "convert.go"},
		}},
		SSAAssertionTrace: &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{{File: "pkg/convert.go", ID: "convert-value", Kind: "*ssa.Call", Line: opLine, Function: "example.com/probe/pkg.toFloat64"}}},
	}
	policy := execution.Policy{
		AllowedCommands:    map[string]bool{"go": true, "sh": true},
		AllowedDirPrefixes: []string{os.TempDir()},
		AllowedEnviron: map[string]bool{
			"PATH": true, "HOME": true, "GOCACHE": true, "GOMODCACHE": true,
			"GOPATH": true, "GOROOT": true, "GOENV": true, "GOFLAGS": true,
			"GOTOOLCHAIN": true, "TMPDIR": true, "USER": true, "SHELL": true,
		},
		MaxOutput: 1 << 20,
		Timeout:   120 * time.Second,
	}
	exec := execution.NewLocalExecutor(policy, execution.NewArtifactFactory(t.TempDir()))
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 120 * time.Second}, investigationID: "e2e-probe"}

	snapshot, err := (programrepo.Analyzer{TargetTests: []string{"TestConvert"}}).Analyze(context.Background(), root, "repo", "e2e-rev", "head")
	if err != nil || snapshot.GoSSA == nil {
		t.Fatalf("real SSA unavailable: %v", err)
	}
	trace := snapshot.GoSSA.TraceAssertion("pkg/convert_test.go", 8)
	report.SSAAssertionTrace = &trace
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, root, snapshot, nil)
	if obs == nil {
		t.Fatal("no observation recorded")
	}
	if obs.Status != "observed" || !obs.SignatureMatch || !obs.ValueMatched || !obs.SSADataflowVerified || len(obs.ObservedValues) == 0 {
		t.Fatalf("end-to-end probe did not observe the exact operation: %#v\noutput:\n%s", obs, obs.OutputExcerpt)
	}
	if obs.BinaryDigest == "" || obs.OperationName != "toFloat64" || len(obs.ExecutionCommand) < 4 {
		t.Fatalf("incomplete provenance: %#v", obs)
	}
	if len(obs.EvidenceIDs) != 1 {
		t.Fatalf("expected runtime observation only: %#v", obs.EvidenceIDs)
	}
	if got := assessRootCause(*report); got.Status == "established" {
		t.Fatalf("uncorrelated runtime evidence promoted: %#v", got)
	}
}

func TestRuntimeObservationWithoutSSAAbstains(t *testing.T) {
	// A matching runtime value with no SSA dataflow anchor must not be
	// execute a guessed source operation.
	dir := probeWorkspace(t, "helper")
	report := probeReport()
	exec := &fakeProbeExecutor{output: "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + probeEvent("+Inf") + "FAIL\n"}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, probeSnapshot(), nil)
	if obs == nil || obs.Status != "unavailable" || len(exec.calls) != 0 {
		t.Fatalf("runtime observation did not match: %#v", obs)
	}
	for _, item := range report.Evidence {
		if item.ClaimKind == "verified_failure_mechanism" {
			t.Fatalf("verified mechanism emitted without SSA dataflow anchor: %#v", item)
		}
	}
}

func TestRuntimeObservationMismatchedSignatureDoesNotEstablish(t *testing.T) {
	dir := probeWorkspace(t, "helper")
	report := probeReport()
	report.SSAAssertionTrace = probeTrace()
	exec := &fakeProbeExecutor{output: "--- FAIL: TestOther\n    expected: 1\n    actual  : 2\n" + probeEvent("2") + "FAIL\n"}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, probeSnapshot(), nil)
	if obs == nil || obs.Status == "observed" || obs.SignatureMatch {
		t.Fatalf("mismatched runtime evidence established a cause: %#v", obs)
	}
	assessment := assessRootCause(*report)
	if assessment.Status == "established" {
		t.Fatalf("mismatched signature established root cause: %#v", assessment)
	}
}

func TestRuntimeObservationDisabledByDefault(t *testing.T) {
	dir := probeWorkspace(t, "helper")
	report := probeReport()
	exec := &fakeProbeExecutor{output: probeEvent("+Inf")}
	d := &Diagnoser{cfg: Config{Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, probeSnapshot(), nil)
	if obs != nil || len(exec.calls) != 0 {
		t.Fatalf("runtime observation ran without explicit enablement: %#v %v", obs, exec.calls)
	}
}

func matchingProbeOutput() string {
	return "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + probeEvent("+Inf") + "FAIL\n"
}

func TestProbeSSARequiresExactOperation(t *testing.T) {
	op := &probeOperation{File: "pkg/convert.go", Line: 24, Name: "Convert", ID: "convert-value", FunctionID: "example.com/probe/pkg.Convert"}
	if !probeSSADataflowSupports(&Report{SSAAssertionTrace: probeTrace()}, op) {
		t.Fatal("exact operation rejected")
	}
	for _, mutate := range []func(*programrepo.SSAValue){
		func(v *programrepo.SSAValue) { v.File = "other.go" },
		func(v *programrepo.SSAValue) { v.Line++ },
		func(v *programrepo.SSAValue) { v.Function += "Wrapper" },
		func(v *programrepo.SSAValue) { v.ID = "different-value-on-the-same-line" },
	} {
		trace := probeTrace()
		mutate(&trace.Values[0])
		if probeSSADataflowSupports(&Report{SSAAssertionTrace: trace}, op) {
			t.Fatalf("inexact operation accepted: %#v", trace.Values[0])
		}
	}
}

func TestFailureSignatureSupportsGotWantFormat(t *testing.T) {
	sig, ok := extractFailureSignature("uint64_test.go:53: 1. format 0: got -9223372036854775808 (int64), want 9223372036854775808 (string)")
	if !ok || sig.actual != "-9223372036854775808" || sig.expected != "9223372036854775808" {
		t.Fatalf("got/want signature not extracted: %#v %v", sig, ok)
	}
}

func TestProbeValueMustEqualFailingValue(t *testing.T) {
	if !probeValueMatchesFailure([]string{"+Inf"}, "+Inf") {
		t.Fatal("the original failing value was not matched")
	}
	for _, values := range [][]string{
		{"3 <nil>"},               // some value exists, but it is not the failure
		{"Inf <nil>"},             // near miss
		{"+Infinity <nil>"},       // different token
		{"value=+Infinity <nil>"}, // embedded substring
		{},
	} {
		if probeValueMatchesFailure(values, "+Inf") {
			t.Errorf("unrelated probe value was matched to the failure: %v", values)
		}
	}
}

func TestMatchingSignatureWithoutExactSSAOrValueDoesNotEstablish(t *testing.T) {
	// Adversarial: the rerun reproduces expected: 0 / actual: +Inf, but the
	// logged value is unrelated and SSA support is only a loose file match.
	dir := probeWorkspace(t, "helper")
	report := probeReport()
	report.SSAAssertionTrace = probeTrace()
	output := "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + probeEvent("42") + "FAIL\n"
	exec := &fakeProbeExecutor{output: output}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, probeSnapshot(), nil)
	if obs == nil || !obs.SignatureMatch {
		t.Fatalf("expected a matching assertion signature: %#v", obs)
	}
	if obs.ValueMatched || obs.Status == "observed" {
		t.Fatalf("unrelated value / loose SSA was promoted: %#v", obs)
	}
	for _, item := range report.Evidence {
		if item.ClaimKind == "verified_failure_mechanism" {
			t.Fatalf("verified mechanism emitted from weak evidence: %#v", item)
		}
	}
	if assessment := assessRootCause(*report); assessment.Status == "established" {
		t.Fatalf("weak evidence established root cause: %#v", assessment)
	}
}

func TestReturnInstrumentationEvaluatesOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "side.go")
	src := "package side\n\nvar calls int\n\nfunc sideEffect() int { calls++; return calls }\n\nfunc compute() int {\n\treturn sideEffect()\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var opLine int
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "return sideEffect") {
			opLine = i + 1
		}
	}
	if _, _, err := injectOperationProbe(path, &probeOperation{ID: "side-effect", File: "side.go", Line: opLine, Name: "compute"}); err != nil {
		t.Fatalf("instrumentation failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	generated := string(data)
	if strings.Count(generated, "sideEffect()") != 2 {
		// One occurrence is the function declaration; the other must be the
		// single evaluation captured into a temporary before returning it.
		t.Fatalf("return expression was evaluated more than once:\n%s", generated)
	}
	if !strings.Contains(generated, "__malenia_probe_v0 = sideEffect()") || !strings.Contains(generated, "return __malenia_probe_v0") {
		t.Fatalf("return was not captured into a single temporary:\n%s", generated)
	}
}

func TestConditionInstrumentationPreservesEvaluation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cond.go")
	src := "package cond\n\nfunc check(v int) bool { return v > 0 }\n\nfunc classify(v int) string {\n\tif check(v) {\n\t\treturn \"pos\"\n\t}\n\treturn \"neg\"\n}\n"
	os.WriteFile(path, []byte(src), 0o644)
	line := 0
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "if check(v)") {
			line = i + 1
		}
	}
	if _, _, err := injectOperationProbe(path, &probeOperation{ID: "cond", File: "cond.go", Line: line, Name: "classify"}); err != nil {
		t.Fatalf("condition instrumentation failed: %v", err)
	}
	data, _ := os.ReadFile(path)
	generated := string(data)
	if strings.Count(generated, "check(v)") != 1 {
		t.Fatalf("condition evaluated more than once:\n%s", generated)
	}
	if !strings.Contains(generated, "__malenia_probe_v0 := check(v)") || !strings.Contains(generated, "if __malenia_probe_v0") {
		t.Fatalf("condition was not hoisted before the branch:\n%s", generated)
	}
}

func TestShortCircuitConditionIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.go")
	src := "package short\n\nfunc a() bool { return true }\nfunc b() bool { return false }\nfunc f() bool {\n\tif a() && b() {\n\t\treturn true\n\t}\n\treturn false\n}\n"
	os.WriteFile(path, []byte(src), 0o644)
	line := 0
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "if a() && b()") {
			line = i + 1
		}
	}
	if _, _, err := injectOperationProbe(path, &probeOperation{ID: "short", File: "short.go", Line: line, Name: "f"}); err == nil {
		t.Fatal("short-circuit condition was hoisted, changing evaluation behavior")
	}
}

func TestTupleSlotInstrumentation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tuple.go")
	src := "package tuple\n\nfunc pair() (int, string) { return 1, \"x\" }\nfunc f() int {\n\tn, s := pair()\n\t_ = s\n\treturn n\n}\n"
	os.WriteFile(path, []byte(src), 0o644)
	line := 0
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "n, s := pair()") {
			line = i + 1
		}
	}
	if _, _, err := injectOperationProbe(path, &probeOperation{ID: "extract-1", File: "tuple.go", Line: line, Name: "f", Slot: 2, Kind: "*ssa.Extract"}); err != nil {
		t.Fatalf("tuple slot instrumentation failed: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), ", s)") {
		t.Fatalf("wrong tuple slot logged, expected second result:\n%s", data)
	}
}

func TestColumnDisambiguatesSameLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.go")
	src := "package same\n\nfunc a() int { return 1 }\nfunc b() int { return 2 }\nfunc f() int {\n\tx := a(); y := b(); return x + y\n}\n"
	os.WriteFile(path, []byte(src), 0o644)
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var secondAssignColumn int
	ast.Inspect(parsed, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name == "y" {
				secondAssignColumn = fset.Position(assign.Rhs[0].Pos()).Column
			}
		}
		return true
	})
	if secondAssignColumn == 0 {
		t.Fatal("could not locate second assignment column")
	}
	line := 0
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "y := b()") {
			line = i + 1
		}
	}
	if _, _, err := injectOperationProbe(path, &probeOperation{ID: "b-call", File: "same.go", Line: line, Column: secondAssignColumn, Name: "f", Kind: "*ssa.Call"}); err != nil {
		t.Fatalf("column-precise instrumentation failed: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), ", y)") {
		t.Fatalf("wrong same-line operation instrumented:\n%s", data)
	}
	// A column pointing at a nested expression must abstain rather than log a
	// neighboring value under the selected operation's identity.
	os.WriteFile(path, []byte(src), 0o644)
	nestedSrc := "package same\n\nfunc a() int { return 1 }\nfunc b() int { return 2 }\nfunc f() int { x := a() + b(); return x }\n"
	os.WriteFile(path, []byte(nestedSrc), 0o644)
	fset = token.NewFileSet()
	parsed, _ = parser.ParseFile(fset, path, nestedSrc, 0)
	var nestedColumn int
	ast.Inspect(parsed, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "b" {
				nestedColumn = fset.Position(call.Pos()).Column
			}
		}
		return true
	})
	if nestedColumn == 0 {
		t.Fatal("could not locate nested call column")
	}
	nestedLine := 0
	for i, l := range strings.Split(nestedSrc, "\n") {
		if strings.Contains(l, "x := a() + b()") {
			nestedLine = i + 1
		}
	}
	if _, _, err := injectOperationProbe(path, &probeOperation{ID: "b-call", File: "same.go", Line: nestedLine, Column: nestedColumn, Name: "f", Kind: "*ssa.Call"}); err == nil {
		t.Fatal("nested expression was instrumented under the wrong identity")
	}
}

func TestProbeSelectsImplementationStoreOperation(t *testing.T) {
	report := probeReport()
	report.SSAAssertionTrace = &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{
		{ID: "assert", Kind: "*ssa.BinOp", File: "pkg/x_test.go", Line: 10, Function: "pkg.TestX"},
		{ID: "copy-store", Kind: "*ssa.Store", File: "pkg/convert.go", Line: 24, Column: 9, Function: "example.com/probe/pkg.Convert"},
	}}
	op := selectProbeOperation(report, probeSnapshot(), nil)
	if op == nil || op.ID != "copy-store" || op.Kind != "*ssa.Store" {
		t.Fatalf("implementation-side store operation was not selected: %#v", op)
	}
}

func TestProbeHelpersRejectAmbiguousOperations(t *testing.T) {
	// Misleading constructor / receiver-name collision: a call at the wrong
	// line must not be instrumented, and a multi-value call without a matching
	// assignment must be reported as unsupported rather than guessed.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.go")
	src := "package bad\n\nfunc New(v int) int { return v }\nfunc (r *T) New(v int) int { return v }\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := injectOperationProbe(path, &probeOperation{ID: "missing", Line: 99}); err == nil {
		t.Fatal("an out-of-range line was instrumented")
	}
	if signatureMatches("", probeReport()) {
		t.Fatal("empty assertion matched the original signature")
	}
	if !signatureMatches("expected: 0, actual: +Inf", probeReport()) {
		t.Fatal("matching assertion did not match the original signature")
	}
}

func probeTrace() *programrepo.SSATrace {
	return &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{{ID: "convert-value", Kind: "*ssa.Call", File: "pkg/convert.go", Line: 24, Function: "example.com/probe/pkg.Convert"}}}
}

func probeSnapshot() programrepo.Snapshot {
	return programrepo.Snapshot{Nodes: []domain.ProgramNode{{FilePath: "pkg/convert.go", Name: "Convert", StartLine: 23, EndLine: 29}}}
}

func probeEvent(value string) string {
	event := ProbeValueObservation{OperationID: "convert-value", File: "pkg/convert.go", Line: 24, Function: "example.com/probe/pkg.Convert", Slot: 0, Sequence: 1, Value: value, Test: "example.com/probe/pkg.TestX"}
	data, _ := json.Marshal(event)
	return runtimeProbeMarker + " " + string(data) + "\n"
}

func TestProbeRejectsValuesFromDifferentOperations(t *testing.T) {
	op := &probeOperation{ID: "convert-value", File: "pkg/convert.go", Line: 24, FunctionID: "example.com/probe/pkg.Convert"}
	var matching ProbeValueObservation
	json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(probeEvent("+Inf"), runtimeProbeMarker+" "))), &matching)
	for name, mutate := range map[string]func(*ProbeValueObservation){
		"operation": func(v *ProbeValueObservation) { v.OperationID = "other" },
		"line":      func(v *ProbeValueObservation) { v.Line++ },
		"file":      func(v *ProbeValueObservation) { v.File = "other/convert.go" },
		"function":  func(v *ProbeValueObservation) { v.Function += "Wrapper" },
		"slot":      func(v *ProbeValueObservation) { v.Slot++ },
		"sequence":  func(v *ProbeValueObservation) { v.Sequence = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := matching
			mutate(&wrong)
			data, _ := json.Marshal(wrong)
			if got := parseOperationValues(runtimeProbeMarker+" "+string(data), op); len(got) != 0 {
				t.Fatalf("wrong observation accepted: %#v", got)
			}
		})
	}
	got := parseOperationValues(probeEvent("+Inf")+probeEvent("+Inf"), op)
	if len(got) != 1 || got[0].Value != "+Inf" {
		t.Fatalf("matching identity lost: %#v", got)
	}
}

func TestProbeInstrumentsExactStatement(t *testing.T) {
	src := "package p\nfunc f() int {\n a := first()\n b := second()\n return a+b\n}\n"
	path := filepath.Join(t.TempDir(), "p.go")
	os.WriteFile(path, []byte(src), 0600)
	_, _, err := injectOperationProbe(path, &probeOperation{ID: "second-value", File: "p.go", Line: 4, Name: "f"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), runtimeProbeHelper+"(") != 1 || !strings.Contains(string(data), ", b)") {
		t.Fatalf("wrong statement instrumented:\n%s", data)
	}
	for _, line := range []int{2, 99} {
		os.WriteFile(path, []byte(src), 0600)
		if _, _, err := injectOperationProbe(path, &probeOperation{ID: "bad", File: "p.go", Line: line, Name: "f"}); err == nil {
			t.Fatalf("unsupported line %d guessed another statement", line)
		}
	}
}

func TestProbeSelectionUsesSSAInsteadOfConstructor(t *testing.T) {
	report := probeReport()
	report.SSAAssertionTrace = probeTrace()
	report.CausalChain.Steps[len(report.CausalChain.Steps)-1].Symbol = "New"
	op := selectProbeOperation(report, probeSnapshot(), nil)
	if op == nil || op.Name != "Convert" || op.Line != 24 || op.ID != "convert-value" {
		t.Fatalf("selected constructor instead of dependency: %#v", op)
	}
	report.SSAAssertionTrace.Values = append(report.SSAAssertionTrace.Values, programrepo.SSAValue{ID: "another", File: op.File, Line: op.Line, Function: op.FunctionID, Kind: "*ssa.Call"})
	if op := selectProbeOperation(report, probeSnapshot(), nil); op != nil {
		t.Fatalf("ambiguous line selected: %#v", op)
	}
}

func TestUnrelatedMatchingValueDoesNotEstablish(t *testing.T) {
	report := probeReport()
	report.SSAAssertionTrace = probeTrace()
	output := strings.ReplaceAll(probeEvent("+Inf"), "convert-value", "unrelated-value") + "--- FAIL: TestX\nexpected: 0\nactual: +Inf\n" + probeEvent("42")
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: &fakeProbeExecutor{output: output}, Timeout: time.Second}}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, probeWorkspace(t, ""), probeSnapshot(), nil)
	if obs == nil || obs.ValueMatched || assessRootCause(*report).Status == "established" {
		t.Fatalf("unrelated matching value promoted: %#v", obs)
	}
	if fmt.Sprint(obs.ObservedValues) != "[42]" {
		t.Fatalf("pooled unrelated values: %#v", obs)
	}
}
