package diagnosis

import (
	"context"
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
			{Kind: "called_function", File: "pkg/convert.go", Line: 23, Symbol: "Convert"},
			{Kind: "implementation_branch", File: "pkg/convert.go", Line: 23, Symbol: "helper"},
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
		"func Convert(v string) float64 {\n\tf, err := helper(v)\n\tif err != nil {\n\t\treturn f, err\n\t}\n\treturn f\n}\n\n" +
		"func helper(v string) (float64, error) { return 0, nil }\n"
	_ = operation
	if err := os.WriteFile(filepath.Join(dir, "pkg", "convert.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/probe\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRuntimeObservationEstablishesMechanism(t *testing.T) {
	dir := probeWorkspace(t, "helper")
	report := probeReport()
	// Typed SSA traced the value dependencies feeding the assertion to this
	// file, so the matching runtime observation can be a verified mechanism.
	report.SSAAssertionTrace = &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{{File: "pkg/convert.go", Line: 23, Function: "Convert", Text: "t0 = toFloat64(1e1000)"}}}
	exec := &fakeProbeExecutor{output: "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + runtimeProbeMarker + " convert.go:20 +Inf <nil>\nFAIL\n"}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, programrepo.Snapshot{}, nil)
	if obs == nil {
		t.Fatal("no observation recorded")
	}
	if obs.Status != "observed" || !obs.SignatureMatch || !obs.ValueMatched || !obs.SSADataflowVerified || len(obs.ObservedValues) == 0 {
		t.Fatalf("observation did not establish mechanism: %#v", obs)
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
	// Root cause is now established because the runtime value matched.
	assessment := assessRootCause(*report)
	if assessment.Status != "established" || assessment.CausalityStatus != "mechanism_verified" {
		t.Fatalf("root cause not established: %#v", assessment)
	}
	// A contract-aligned mechanism evidence item was emitted and cited so the
	// evaluator can check it against independently reviewed ground truth.
	var mechanism *EvidenceItem
	for i := range report.Evidence {
		if report.Evidence[i].ClaimKind == "verified_failure_mechanism" {
			mechanism = &report.Evidence[i]
		}
	}
	if mechanism == nil || mechanism.Basis != BasisDataflowVerified || mechanism.Source != "pkg/convert.go" || mechanism.DeclarationID != "Convert" {
		t.Fatalf("no contract-aligned mechanism evidence: %#v", mechanism)
	}
	if !containsString(assessment.EvidenceIDs, mechanism.ID) || mechanism.Revision != report.Environment.HeadCommit {
		t.Fatalf("mechanism evidence not properly cited/provenanced: %#v", mechanism)
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
		if strings.Contains(line, "func toFloat64") {
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
		SSAAssertionTrace: &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{{File: "pkg/convert.go", Line: opLine, Function: "toFloat64"}}},
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

	obs := d.collectRuntimeMechanismObservation(context.Background(), report, root, programrepo.Snapshot{}, nil)
	if obs == nil {
		t.Fatal("no observation recorded")
	}
	if obs.Status != "observed" || !obs.SignatureMatch || !obs.ValueMatched || !obs.SSADataflowVerified || len(obs.ObservedValues) == 0 {
		t.Fatalf("end-to-end probe did not establish mechanism: %#v\noutput:\n%s", obs, obs.OutputExcerpt)
	}
	if obs.BinaryDigest == "" || obs.OperationName != "toFloat64" || len(obs.ExecutionCommand) < 4 {
		t.Fatalf("incomplete provenance: %#v", obs)
	}
	if len(obs.EvidenceIDs) != 2 {
		t.Fatalf("expected runtime + contract-aligned mechanism evidence: %#v", obs.EvidenceIDs)
	}
}

func TestRuntimeObservationWithoutSSAIsOnlyAnObservation(t *testing.T) {
	// A matching runtime value with no SSA dataflow anchor must not be
	// labelled a verified failure mechanism. It remains a runtime observation.
	dir := probeWorkspace(t, "helper")
	report := probeReport()
	exec := &fakeProbeExecutor{output: "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + runtimeProbeMarker + " convert.go:20 +Inf <nil>\nFAIL\n"}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, programrepo.Snapshot{}, nil)
	if obs == nil || obs.Status != "observed" || !obs.SignatureMatch {
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
	exec := &fakeProbeExecutor{output: "--- FAIL: TestOther\n    expected: 1\n    actual  : 2\n" + runtimeProbeMarker + " convert.go:20 2 <nil>\nFAIL\n"}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, programrepo.Snapshot{}, nil)
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
	exec := &fakeProbeExecutor{output: runtimeProbeMarker + " convert.go:20 +Inf\n"}
	d := &Diagnoser{cfg: Config{Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, programrepo.Snapshot{}, nil)
	if obs != nil || len(exec.calls) != 0 {
		t.Fatalf("runtime observation ran without explicit enablement: %#v %v", obs, exec.calls)
	}
}

func matchingProbeOutput() string {
	return "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + runtimeProbeMarker + " convert.go:20 +Inf <nil>\nFAIL\n"
}

func TestProbeSSARequiresExactOperation(t *testing.T) {
	op := &probeOperation{File: "pkg/convert.go", Line: 23, Name: "Convert"}
	exact := &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{{File: "pkg/convert.go", Line: 23, Function: "Convert"}}}
	looseCases := map[string]*programrepo.SSATrace{
		"same function different file":          {Values: []programrepo.SSAValue{{File: "pkg/other.go", Line: 23, Function: "Convert"}}},
		"same file different function":          {Values: []programrepo.SSAValue{{File: "pkg/convert.go", Line: 23, Function: "Other"}}},
		"same file and function different line": {Values: []programrepo.SSAValue{{File: "pkg/convert.go", Line: 24, Function: "Convert"}}},
		"function-name substring only":          {Values: []programrepo.SSAValue{{File: "pkg/other.go", Line: 99, Function: "ConvertWrapper"}}},
		"no values":                             {Status: "unavailable"},
	}
	if !probeSSADataflowSupports(&Report{SSAAssertionTrace: exact}, op) {
		t.Fatal("exact SSA value at the operation was rejected")
	}
	for name, trace := range looseCases {
		if probeSSADataflowSupports(&Report{SSAAssertionTrace: trace}, op) {
			t.Errorf("%s was accepted as exact SSA dataflow support", name)
		}
	}
}

func TestProbeValueMustEqualFailingValue(t *testing.T) {
	if !probeValueMatchesFailure([]string{"+Inf <nil>"}, "+Inf") {
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
	report.SSAAssertionTrace = &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{{File: "pkg/convert.go", Line: 24, Function: "Other"}}}
	output := "--- FAIL: TestX\n    expected: 0\n    actual  : +Inf\n" + runtimeProbeMarker + " convert.go:20 42 <nil>\nFAIL\n"
	exec := &fakeProbeExecutor{output: output}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: exec, Timeout: 30e9}, investigationID: "probe-test"}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, dir, programrepo.Snapshot{}, nil)
	if obs == nil || !obs.SignatureMatch {
		t.Fatalf("expected a matching assertion signature: %#v", obs)
	}
	if obs.ValueMatched || obs.SSADataflowVerified || obs.Status == "observed" {
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
		if strings.Contains(line, "func compute") {
			opLine = i + 1
		}
	}
	if _, _, err := injectValueProbe(path, opLine); err != nil {
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
	if !strings.Contains(generated, "__malenia_probe_v0 := sideEffect()") || !strings.Contains(generated, "return __malenia_probe_v0") {
		t.Fatalf("return was not captured into a single temporary:\n%s", generated)
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
	if _, _, err := injectValueProbe(path, 99); err == nil {
		t.Fatal("an out-of-range line was instrumented")
	}
	values := parseProbeValues("noise\n" + runtimeProbeMarker + " bad.go:10 one\n" + runtimeProbeMarker + " bad.go:10 one\n" + runtimeProbeMarker + " bad.go:12 two\n")
	if len(values) != 2 || values[0] != "one" || values[1] != "two" {
		t.Fatalf("probe values were not bounded/deduplicated: %v", values)
	}
	if signatureMatches("", probeReport()) {
		t.Fatal("empty assertion matched the original signature")
	}
	if !signatureMatches("expected: 0, actual: +Inf", probeReport()) {
		t.Fatal("matching assertion did not match the original signature")
	}
}
