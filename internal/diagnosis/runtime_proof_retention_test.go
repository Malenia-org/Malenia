package diagnosis

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRetainedProofSurvivesTruncatedHumanExcerpt(t *testing.T) {
	c := &AssertionCorrelation{Verified: true, AssertionID: "a", Test: "TestX/sub", Invocation: 1, Goroutine: "7", OperationSequence: 2}
	begin := ProbeValueObservation{Event: "assertion_begin", AssertionID: "a", Test: c.Test, Invocation: 1, Goroutine: "7"}
	operation := ProbeValueObservation{Event: "operation", Goroutine: "7", Sequence: 2, OperationID: "return", Value: "true"}
	end := begin
	end.Event = "assertion_end"
	encode := func(v ProbeValueObservation) string {
		b, _ := json.Marshal(v)
		return runtimeProbeMarker + " " + string(b) + "\n"
	}
	output := strings.Repeat("unrelated test output\n", 1000) + encode(begin) + encode(operation) + encode(end)
	if strings.Contains(bound(output, MaxEvidenceExcerpt), "assertion_begin") {
		t.Fatal("fixture did not place proof beyond the excerpt")
	}
	events := retainAssertionProof(output, c)
	if len(events) != 3 || events[1].Sequence != 2 {
		t.Fatalf("proof lost after excerpt truncation: %#v", events)
	}
	if got := retainAssertionProof(encode(begin)+encode(operation), c); got != nil {
		t.Fatalf("incomplete span retained as complete: %#v", got)
	}
	if got := retainAssertionProof(encode(begin)+strings.Repeat(encode(operation), 40)+encode(end), c); got != nil {
		t.Fatal("oversized span retained")
	}
	other := operation
	other.Goroutine = "8"
	if got := retainAssertionProof(encode(begin)+encode(other)+encode(operation)+encode(end), c); len(got) != 3 {
		t.Fatalf("unrelated goroutine entered proof: %#v", got)
	}
}
