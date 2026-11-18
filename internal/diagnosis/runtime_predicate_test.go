package diagnosis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/causent/malenia/internal/domain"
	"github.com/causent/malenia/internal/execution"
	programrepo "github.com/causent/malenia/internal/repository"
)

func TestComputedReturnsPrecedeConstantEarlyReturns(t *testing.T) {
	slot := 0
	report := &Report{SSAAssertionTrace: &programrepo.SSATrace{Values: []programrepo.SSAValue{
		{ID: "early", Kind: "*ssa.Return", Function: "sample.Compare", File: "value.go", Line: 3, Column: 2, TraceSlot: &slot, Operands: []string{"constant"}},
		{ID: "computed", Kind: "*ssa.Return", Function: "sample.Compare", File: "value.go", Line: 8, Column: 2, TraceSlot: &slot, Operands: []string{"calculation"}},
		{ID: "constant", Kind: "constant"}, {ID: "calculation", Kind: "*ssa.Call"},
	}}}
	op := selectProbeOperation(report, programrepo.Snapshot{}, nil)
	if op == nil || op.ID != "computed" {
		t.Fatalf("chose unreachable early return instead of actual producer: %#v", op)
	}
}

func TestNativeFailureMessagesRequireSameSubtestAndOriginalLocation(t *testing.T) {
	before := "--- FAIL: TestRows (0.00s)\n    --- FAIL: TestRows/wrap (0.00s)\n        rows_test.go:20: comparison mismatch: want false\n"
	after := "=== RUN   TestRows\n=== RUN   TestRows/wrap\n    rows_test.go:27: comparison mismatch: want false\n--- FAIL: TestRows (0.00s)\n"
	plan := &assertionProbePlan{File: "rows_test.go", Line: 20}
	if !samePredicateFailure(before, after, plan, "TestRows/wrap") {
		t.Fatal("same source-backed predicate rejected")
	}
	if samePredicateFailure(before, after, plan, "TestRows/other") {
		t.Fatal("another subtest accepted")
	}
	plan.Line = 21
	if samePredicateFailure(before, after, plan, "TestRows/wrap") {
		t.Fatal("another original assertion accepted")
	}
	plan.Line = 20
	if samePredicateFailure(before, strings.ReplaceAll(after, "want false", "want true"), plan, "TestRows/wrap") {
		t.Fatal("changed failure accepted")
	}
}

func TestIntegerConversionRequiresNumericLossAndExactActual(t *testing.T) {
	correct := NumericConversionEvidence{Input: "270", Output: "14", Actual: "14", InputType: "int", OutputType: "int8", ActualType: "int8", Lossy: true, MatchesActual: true}
	if !verifiedIntegerConversion(&correct) {
		t.Fatal("integer narrowing rejected")
	}
	for name, mutate := range map[string]func(*NumericConversionEvidence){
		"no loss":                func(v *NumericConversionEvidence) { v.Input = "14" },
		"unrelated actual":       func(v *NumericConversionEvidence) { v.Actual = "42" },
		"different type":         func(v *NumericConversionEvidence) { v.ActualType = "uint8" },
		"non-integer":            func(v *NumericConversionEvidence) { v.Input = "+Inf" },
		"equal numeric spelling": func(v *NumericConversionEvidence) { v.Input = "014" },
		"no loss flag":           func(v *NumericConversionEvidence) { v.Lossy = false },
		"no equality flag":       func(v *NumericConversionEvidence) { v.MatchesActual = false },
	} {
		t.Run(name, func(t *testing.T) {
			v := correct
			mutate(&v)
			if verifiedIntegerConversion(&v) {
				t.Fatal("unsupported narrowing claim accepted")
			}
		})
	}
}

func TestNumericPredicateMechanismEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and analyzes a Go module")
	}
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/numericprobe\n\ngo 1.23\n")
	write("numeric.go", `package sample
import "reflect"
var calls int
func Equivalent(left,right interface{}) bool {
 calls++
 if reflect.DeepEqual(left,right) { return true }
 value:=reflect.ValueOf(left)
 target:=reflect.TypeOf(right)
 if value.IsValid() && value.Type().ConvertibleTo(target) {
  return reflect.DeepEqual(value.Convert(target).Interface(),right)
 }
 return false
}
`)
	source := `package sample
import "testing"
func TestNumeric(t *testing.T){
 rows:=[]struct{name string;left,right interface{};want bool}{
  {"equal",int(270),int16(270),true},
  {"wrap",int(270),int8(14),false},
 }
 for _,row:=range rows {
  t.Run(row.name,func(t *testing.T){
   got:=Equivalent(row.left,row.right)
   if got!=row.want {
    t.Errorf("comparison mismatch: %v and %v want %v",row.left,row.right,row.want)
   }
   _=Equivalent(row.left,row.right)
  })
 }
 if calls!=4 { t.Errorf("evaluation count changed: %d",calls) }
}
`
	write("numeric_test.go", source)
	line := 0
	for i, row := range strings.Split(source, "\n") {
		if strings.Contains(row, "t.Errorf(\"comparison mismatch") {
			line = i + 1
		}
	}
	policy := execution.Policy{AllowedCommands: map[string]bool{"go": true, "sh": true}, AllowedDirPrefixes: []string{os.TempDir()}, AllowedEnviron: map[string]bool{"PATH": true, "HOME": true, "GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOROOT": true, "GOENV": true, "GOTOOLCHAIN": true, "TMPDIR": true}, MaxOutput: 1 << 20, Timeout: 120 * time.Second}
	executor := execution.NewLocalExecutor(policy, execution.NewArtifactFactory(t.TempDir()))
	command := []string{"go", "test", "-count=1", "-v", "-run", "^TestNumeric$", "."}
	baseline, err := executor.Run(context.Background(), domain.ExecutionRequest{Command: command, Dir: root, WorkspacePath: root, Timeout: 120 * time.Second, MaxOutput: 32 << 10})
	if err != nil || baseline.ExitCode == 0 {
		t.Fatalf("original fixture did not fail: %#v %v", baseline, err)
	}
	original := baseline.Stdout + "\n" + baseline.Stderr
	snapshot, err := (programrepo.Analyzer{TargetTests: []string{"TestNumeric"}}).Analyze(context.Background(), root, "repo", "rev", "abc")
	if err != nil || snapshot.GoSSA == nil {
		t.Fatalf("SSA unavailable: %v", err)
	}
	trace := snapshot.GoSSA.TraceAssertion("numeric_test.go", line)
	report := &Report{InvestigationID: "predicate-proof", Environment: Environment{HeadCommit: "abc"}, Command: command, SSAAssertionTrace: &trace, Evidence: []EvidenceItem{{ID: "ci", Kind: "ci_log", Detail: original}}, FailureIncidents: []FailureIncident{{Category: "assertion", EvidenceIDs: []string{"ci"}}}}
	if _, ok := originalFailureSignature(report); ok {
		t.Fatal("fixture unexpectedly supplied an expected/actual pair")
	}
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: executor, Timeout: 120 * time.Second}}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, root, snapshot, nil)
	if obs == nil || obs.Correlation == nil || obs.Correlation.Numeric == nil || obs.OperationName != "Equivalent" || obs.SignatureKind != "source_predicate_and_failure_message" {
		t.Fatalf("numeric predicate not investigated: %#v", obs)
	}
	if obs.Correlation.Numeric.Input != "270" || obs.Correlation.Numeric.Output != "14" {
		t.Fatalf("wrong conversion: %#v", obs.Correlation.Numeric)
	}
	if len(obs.Observations) != 4 {
		t.Fatalf("expression count/order changed: %#v output=%s", obs.Observations, obs.OutputExcerpt)
	}
	if got := assessRootCause(*report); got.Status != "established" {
		t.Fatalf("mechanism not established: %#v", got)
	}
}

func TestPredicateCorrelationDoesNotPromoteUnrelatedConversion(t *testing.T) {
	op := &probeOperation{ID: "return", File: "value.go", Line: 7, FunctionID: "sample.Equivalent", Slot: 1, Kind: "*ssa.Return"}
	plan := &assertionProbePlan{ID: "assertion", File: "value_test.go", Line: 10, Path: []string{"return", "actual"}, Functions: []string{"sample.Equivalent", "sample.TestCheck.func1"}, NativePredicate: true}
	original := "--- FAIL: TestCheck/sub (0.00s)\n    value_test.go:10: wanted false\n"
	predicate := true
	begin := ProbeValueObservation{Event: "assertion_begin", AssertionID: plan.ID, Invocation: 1, Goroutine: "7", Test: "TestCheck/sub"}
	operation := ProbeValueObservation{Event: "operation", OperationID: op.ID, File: op.File, Line: op.Line, Function: op.FunctionID, Slot: 1, Sequence: 2, Goroutine: "7", Value: "true", Frames: plan.Functions}
	conversion := ProbeValueObservation{Event: "conversion", OperationID: op.ID, File: op.File, Line: op.Line, Function: op.FunctionID, Slot: 1, Goroutine: "7", Numeric: &NumericConversionEvidence{Input: "270", Output: "14", Actual: "14", InputType: "int", OutputType: "int8", ActualType: "int8", Lossy: true, MatchesActual: true}}
	end := ProbeValueObservation{Event: "assertion_end", AssertionID: plan.ID, Invocation: 1, Goroutine: "7", Test: "TestCheck/sub", Value: "true", Expected: "false", Predicate: &predicate}
	encode := func(events ...ProbeValueObservation) string {
		out := original
		for _, v := range events {
			data, _ := json.Marshal(v)
			out += runtimeProbeMarker + " " + string(data) + "\n"
		}
		return out
	}
	if got := correlateAssertion(encode(begin, conversion, operation, end), original, op, plan); got == nil || got.Numeric == nil {
		t.Fatal("matching conversion rejected")
	}
	for name, events := range map[string][]ProbeValueObservation{
		"conversion outside invocation": {conversion, begin, operation, end},
		"multiple conversions":          {begin, conversion, conversion, operation, end},
		"missing conversion":            {begin, operation, end},
	} {
		t.Run(name, func(t *testing.T) {
			if got := correlateAssertion(encode(events...), original, op, plan); got != nil && got.Numeric != nil {
				t.Fatal("unbound conversion promoted")
			}
		})
	}
	wrong := conversion
	wrong.Goroutine = "8"
	if got := correlateAssertion(encode(begin, wrong, operation, end), original, op, plan); got != nil && got.Numeric != nil {
		t.Fatal("conversion from another goroutine promoted")
	}
	no := false
	wrongEnd := end
	wrongEnd.Predicate = &no
	if got := correlateAssertion(encode(begin, conversion, operation, wrongEnd), original, op, plan); got != nil {
		t.Fatal("passing predicate verified")
	}
	if got := correlateAssertion(encode(begin, conversion, operation, end), strings.ReplaceAll(original, "wanted false", "different failure"), op, plan); got != nil {
		t.Fatal("different original failure verified")
	}
}
