package diagnosis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/causent/malenia/internal/execution"
	programrepo "github.com/causent/malenia/internal/repository"
)

func TestAssertionCorrelationRejectsUnrelatedInvocations(t *testing.T) {
	yes := true
	op := &probeOperation{ID: "return-value", File: "value.go", Line: 7, FunctionID: "sample.checked", Slot: 1, Kind: "*ssa.Return", ErrorReturn: true}
	plan := &assertionProbePlan{ID: "assertion", File: "value_test.go", Line: 10, Expected: "0", Path: []string{"return-value", "actual-call"}, Functions: []string{"sample.checked", "sample.wrapper", "sample.TestCheck.func1"}}
	original := "Error Trace: value_test.go:10\nError: Not equal:\nexpected: 0\nactual: +Inf\nTest: TestCheck/sub\n"
	begin := ProbeValueObservation{Event: "assertion_begin", AssertionID: plan.ID, Invocation: 1, Goroutine: "7", Test: "TestCheck/sub"}
	operation := ProbeValueObservation{Event: "operation", OperationID: op.ID, File: op.File, Line: op.Line, Function: op.FunctionID, Slot: op.Slot, Sequence: 2, Goroutine: "7", Value: "+Inf", ErrorPresent: &yes, Frames: []string{"sample.checked", "sample.wrapper", "sample.TestCheck.func1"}}
	end := ProbeValueObservation{Event: "assertion_end", AssertionID: plan.ID, Invocation: 1, Goroutine: "7", Test: "TestCheck/sub", Value: "+Inf", Expected: "0"}
	encode := func(events ...ProbeValueObservation) string {
		var out strings.Builder
		out.WriteString(original)
		for _, v := range events {
			data, _ := json.Marshal(v)
			out.WriteString(runtimeProbeMarker + " " + string(data) + "\n")
		}
		return out.String()
	}
	if got := correlateAssertion(encode(begin, operation, end), original, op, plan); got == nil || !got.Verified {
		t.Fatal("matching synchronous invocation rejected")
	}
	for name, change := range map[string]func(*ProbeValueObservation){
		"different statement": func(v *ProbeValueObservation) { v.OperationID = "other" },
		"different goroutine": func(v *ProbeValueObservation) { v.Goroutine = "8" },
		"different slot":      func(v *ProbeValueObservation) { v.Slot = 2 },
		"recursive call": func(v *ProbeValueObservation) {
			v.Frames = []string{"sample.checked", "sample.checked", "sample.wrapper", "sample.TestCheck.func1"}
		},
		"callback": func(v *ProbeValueObservation) {
			v.Frames = []string{"sample.checked", "sample.Callback", "sample.wrapper", "sample.TestCheck.func1"}
		},
		"missing caller stack":      func(v *ProbeValueObservation) { v.Frames = nil },
		"different value":           func(v *ProbeValueObservation) { v.Value = "42" },
		"missing error observation": func(v *ProbeValueObservation) { v.ErrorPresent = nil },
		"nil error":                 func(v *ProbeValueObservation) { no := false; v.ErrorPresent = &no },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := operation
			change(&wrong)
			if correlateAssertion(encode(begin, wrong, end), original, op, plan) != nil {
				t.Fatal("unrelated event verified")
			}
		})
	}
	for name, events := range map[string][]ProbeValueObservation{
		"before actual evaluation":         {operation, begin, end},
		"after actual evaluation":          {begin, end, operation},
		"multiple calls with equal values": {begin, operation, operation, end},
		"no operation":                     {begin, end},
	} {
		t.Run(name, func(t *testing.T) {
			if correlateAssertion(encode(events...), original, op, plan) != nil {
				t.Fatal("ambiguous invocation verified")
			}
		})
	}
	for name, mutate := range map[string]func(*ProbeValueObservation){
		"different subtest":    func(v *ProbeValueObservation) { v.Test = "TestCheck/other" },
		"different assertion":  func(v *ProbeValueObservation) { v.AssertionID = "other" },
		"different invocation": func(v *ProbeValueObservation) { v.Invocation = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := end
			mutate(&wrong)
			if correlateAssertion(encode(begin, operation, wrong), original, op, plan) != nil {
				t.Fatal("different failing assertion verified")
			}
		})
	}
	wrongLine := *plan
	wrongLine.Line = 99
	if correlateAssertion(encode(begin, operation, end), original, op, &wrongLine) != nil {
		t.Fatal("original failure at another assertion verified")
	}
}

func TestTransparentReturnPathRejectsMemoryAndBranches(t *testing.T) {
	g := &programrepo.GoSSA{Functions: map[string]programrepo.SSAFunction{"wrapper": {ID: "wrapper", ReturnInstructions: []string{"wrapper:b0:3"}, Instructions: []string{"wrapper:b0:0", "wrapper:b0:3"}}}, Values: map[string]programrepo.SSAValue{
		"wrapper:b0:0": {ID: "wrapper:b0:0", Function: "wrapper", Kind: "*ssa.Call", Callee: "checked", Type: "(float64, error)"},
		"extract":      {ID: "extract", Kind: "*ssa.Extract", TupleIndex: 0, Operands: []string{"wrapper:b0:0"}},
		"wrapper:b0:3": {ID: "wrapper:b0:3", Kind: "*ssa.Return", Operands: []string{"extract"}},
		"actual":       {ID: "actual", Kind: "*ssa.Call", Callee: "wrapper", Type: "float64"},
	}}
	op := &probeOperation{ID: "checked:return", FunctionID: "checked", Kind: "*ssa.Return", Slot: 1}
	if transparentReturnPath(g, op, "actual") == nil {
		t.Fatal("direct scalar return rejected")
	}
	saved := g.Values["extract"]
	v := saved
	v.Kind = "*ssa.UnOp"
	g.Values["extract"] = v
	if transparentReturnPath(g, op, "actual") != nil {
		t.Fatal("memory load treated as transparent return")
	}
	g.Values["extract"] = saved
	ret := g.Values["wrapper:b0:3"]
	ret.ID = "wrapper:b1:3"
	g.Values["wrapper:b0:3"] = ret
	if transparentReturnPath(g, op, "actual") != nil {
		t.Fatal("later branch treated as unconditional return")
	}
}

func TestSynchronousErrorReturnMechanismEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and analyzes fixture modules")
	}
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A local assertion-contract fixture keeps this test independent of network
	// access. Public validation uses the real unmodified upstream assertion lib.
	write("go.mod", "module example.com/returnproof\n\ngo 1.23\nrequire github.com/stretchr/testify/assert v0.0.0\nreplace github.com/stretchr/testify/assert => ./assertlib\n")
	write("assertlib/go.mod", "module github.com/stretchr/testify/assert\n\ngo 1.23\n")
	write("assertlib/assert.go", `package assert
import "reflect"
type TestingT interface{ Helper(); Errorf(string,...interface{}) }
func Equal(t TestingT, expected, actual interface{}, msg ...interface{}) bool {
 t.Helper()
 if !reflect.DeepEqual(expected,actual) { t.Errorf("Error Trace: value_test.go:8\nError: Not equal:\nexpected: %v\nactual: %v\nTest: TestCheck/sub",expected,actual);return false }
 return true
}
`)
	write("value.go", `package sample
import ("errors";"math")
func checked(s string)(float64,error){
 value:=math.Inf(1)
 if s!="" {return value,errors.New("range")}
 return 0,nil
}
func result(s string) float64 { value,_:=checked(s); return value }
`)
	write("value_test.go", `package sample
import (
 "testing"
 "github.com/stretchr/testify/assert"
)
func TestCheck(t *testing.T) {
 t.Run("sub",func(t *testing.T){
  assert.Equal(t,float64(0),result("large"))
  _=result("large")
 })
}
`)
	snapshot, err := (programrepo.Analyzer{TargetTests: []string{"TestCheck"}}).Analyze(context.Background(), root, "repo", "rev", "abc")
	if err != nil || snapshot.GoSSA == nil {
		t.Fatalf("SSA unavailable: %v", err)
	}
	trace := snapshot.GoSSA.TraceAssertion("value_test.go", 8)
	report := &Report{InvestigationID: "return-proof", Environment: Environment{HeadCommit: "abc"}, Command: []string{"go", "test", "-run", "^TestCheck$", "."}, SSAAssertionTrace: &trace, Evidence: []EvidenceItem{{ID: "ci", Kind: "ci_log", Detail: "Error Trace: value_test.go:8\nError: Not equal:\nexpected: 0\nactual: +Inf\nTest: TestCheck/sub\n"}}, FailureIncidents: []FailureIncident{{Category: "assertion", EvidenceIDs: []string{"ci"}}}}
	policy := execution.Policy{AllowedCommands: map[string]bool{"go": true, "sh": true}, AllowedDirPrefixes: []string{os.TempDir()}, AllowedEnviron: map[string]bool{"PATH": true, "HOME": true, "GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOROOT": true, "GOENV": true, "GOTOOLCHAIN": true, "TMPDIR": true}, MaxOutput: 1 << 20, Timeout: 120 * time.Second}
	executor := execution.NewLocalExecutor(policy, execution.NewArtifactFactory(t.TempDir()))
	d := &Diagnoser{cfg: Config{RuntimeObservation: true, Executor: executor, Timeout: 120 * time.Second}}
	obs := d.collectRuntimeMechanismObservation(context.Background(), report, root, snapshot, nil)
	if obs == nil || obs.Correlation == nil || !obs.Correlation.Verified || obs.OperationName != "checked" {
		t.Fatalf("return mechanism not correlated: %#v", obs)
	}
	if len(obs.Observations) != 2 {
		t.Fatalf("fixture did not exercise another call outside the assertion: %#v", obs.Observations)
	}
	if got := assessRootCause(*report); got.Status != "established" || got.CausalityStatus != "mechanism_verified" {
		t.Fatalf("mechanism not established: %#v", got)
	}
	obs.Correlation = nil
	if got := assessRootCause(*report); got.Status == "established" {
		t.Fatal("missing invocation correlation promoted")
	}
}

func TestProbeCopyPreservesRequiredFixturesAndRejectsEscapingLinks(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "testdata"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "testdata", "input.json"), []byte("{\"number\":1}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("testdata/input.json", filepath.Join(root, "fixture")); err != nil {
		t.Fatal(err)
	}
	if err := copyProbeWorkspace(root, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "fixture"))
	if err != nil || string(data) != "{\"number\":1}" {
		t.Fatalf("fixture changed: %q %v", data, err)
	}
	if err := os.Symlink("../outside", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := copyProbeWorkspace(root, t.TempDir()); err == nil {
		t.Fatal("escaping fixture symlink accepted")
	}
}
