package evalcorpus

import (
	"strings"
	"testing"

	"github.com/caeser-org/ceaser/internal/diagnosis"
	programrepo "github.com/caeser-org/ceaser/internal/repository"
)

func auditableReport(numeric bool) diagnosis.Report {
	yes := true
	op := diagnosis.ProbeValueObservation{Event: "operation", OperationID: "example.Convert:b0:1", File: "example.go", Line: 7, Function: "example.Convert", Slot: 1, Sequence: 2, Goroutine: "7", Value: "+Inf", Frames: []string{"example.Convert", "example.TestCall"}, ErrorPresent: &yes}
	begin := diagnosis.ProbeValueObservation{Event: "assertion_begin", AssertionID: "assert", Invocation: 1, Goroutine: "7", Test: "TestCall/sub"}
	end := diagnosis.ProbeValueObservation{Event: "assertion_end", AssertionID: "assert", Invocation: 1, Goroutine: "7", Test: "TestCall/sub", Value: "+Inf", Expected: "0"}
	path := []string{op.OperationID, "example.TestCall:b0:1"}
	trace := &programrepo.SSATrace{AnchorID: "assert", Values: []programrepo.SSAValue{
		{ID: op.OperationID, Kind: "*ssa.Return", Function: op.Function, File: op.File, Line: op.Line, Operands: []string{"value", "err"}, ErrorReturn: true},
		{ID: path[1], Kind: "*ssa.Call", Function: "example.TestCall", Callee: op.Function, Type: "float64"},
		{ID: "assert", Kind: "*ssa.Call", Function: "example.TestCall", Callee: "github.com/stretchr/testify/assert.Equal", Arguments: []string{"testing", "constant", "example.TestCall:b0:1"}},
		{ID: "constant", Kind: "constant", Text: "0:float64"},
	}}
	cor := &diagnosis.AssertionCorrelation{Verified: true, AssertionID: "assert", Test: begin.Test, Invocation: 1, Goroutine: "7", OperationSequence: 2, ReturnPath: path, ErrorPresent: true}
	report := scoredReport()
	report.Environment.HeadCommit = "revision"
	report.RootCause = diagnosis.RootCauseAssessment{Status: "established", EvidenceIDs: []string{"mechanism"}}
	report.Evidence = append(report.Evidence, diagnosis.EvidenceItem{ID: "mechanism", ClaimKind: "verified_failure_mechanism", Basis: "DataflowVerified", Source: op.File, DeclarationID: "Convert", Revision: "revision"})
	report.SSAAssertionTrace = trace
	o := &diagnosis.RuntimeMechanismObservation{Status: "observed", OperationID: op.OperationID, OperationFile: op.File, OperationLine: op.Line, OperationName: "Convert", ObservedValues: []string{op.Value}, Observations: []diagnosis.ProbeValueObservation{op}, Correlation: cor, Mechanism: "error-return propagation", SignatureKind: "expected_actual", SignatureMatch: true, ValueMatched: true, SSADataflowVerified: true, TestFailed: true, Revision: "revision", BinaryDigest: strings.Repeat("a", 64), ExecutionCommand: []string{"sh", "-c", `exec "$0" "$@"`, "/tmp/caeser-probe.test"}, EvidenceIDs: []string{"mechanism"}, ProofVersion: "runtime-assertion-v1", ProofEvents: []diagnosis.ProbeValueObservation{begin, op, end}}
	report.MechanismObservation = o
	if numeric {
		o.SignatureKind = "source_predicate_and_failure_message"
		report.SSAAssertionTrace.Values[1].Type = "bool"
		report.SSAAssertionTrace.Values[2].Callee = "(*testing.common).Errorf"
		report.SSAAssertionTrace.Values[2].Conditions = []string{"predicate"}
		report.SSAAssertionTrace.Values = append(report.SSAAssertionTrace.Values, programrepo.SSAValue{ID: "predicate", Kind: "*ssa.BinOp", Text: "actual != expected", Operands: []string{"example.TestCall:b0:1", "wanted"}})
		evidence := &diagnosis.NumericConversionEvidence{Input: "270", Output: "14", Actual: "14", InputType: "int", OutputType: "int8", ActualType: "int8", Lossy: true, MatchesActual: true}
		op.Value = "true"
		op.ErrorPresent = nil
		end.Value = "true"
		end.Expected = "false"
		end.Predicate = &yes
		conversion := op
		conversion.Event = "conversion"
		conversion.Numeric = evidence
		o.Observations = []diagnosis.ProbeValueObservation{op}
		o.ObservedValues = []string{"true"}
		o.ProofEvents = []diagnosis.ProbeValueObservation{begin, conversion, op, end}
		cor.Numeric = evidence
		cor.Expected = "false"
		cor.Actual = "true"
		cor.ErrorPresent = false
		report.SSAAssertionTrace.Values[0].Operands = []string{"compare"}
		report.SSAAssertionTrace.Values[0].ErrorReturn = false
		report.SSAAssertionTrace.Values = append(report.SSAAssertionTrace.Values,
			programrepo.SSAValue{ID: "compare", Kind: "*ssa.Call", Callee: "reflect.DeepEqual", Arguments: []string{"unbox", "actual"}},
			programrepo.SSAValue{ID: "unbox", Kind: "*ssa.Call", Callee: "(reflect.Value).Interface", Arguments: []string{"convert"}},
			programrepo.SSAValue{ID: "convert", Kind: "*ssa.Call", Callee: "(reflect.Value).Convert"},
		)
	}
	return report
}

func TestProofAuditRequiresEvidenceBeyondLabels(t *testing.T) {
	r := scoredReport()
	r.RootCause.Status = "established"
	r.Evidence = append(r.Evidence, diagnosis.EvidenceItem{ID: "mechanism", ClaimKind: "verified_failure_mechanism", Basis: "DataflowVerified", Source: "example.go", DeclarationID: "Convert", Revision: "revision"})
	if a := AuditMechanismProof(r); a.Status != "insufficient" {
		t.Fatalf("labels were treated as proof: %+v", a)
	}
}
func TestProofAuditConsistentEvidenceKeepsReviewObligations(t *testing.T) {
	for _, numeric := range []bool{false, true} {
		a := AuditMechanismProof(auditableReport(numeric))
		if a.Status != "consistent" || a.BinaryBytesVerified || !a.GroundTruthReviewRequired {
			t.Fatalf("incorrect audit guarantees: %+v", a)
		}
	}
}
func TestProofAuditRejectsContradictoryEvidence(t *testing.T) {
	for name, change := range map[string]func(*diagnosis.Report){
		"stale revision":          func(r *diagnosis.Report) { r.MechanismObservation.Revision = "other" },
		"bad digest":              func(r *diagnosis.Report) { r.MechanismObservation.BinaryDigest = "x" },
		"unrelated invocation":    func(r *diagnosis.Report) { r.MechanismObservation.ProofEvents[2].Invocation = 9 },
		"another goroutine":       func(r *diagnosis.Report) { r.MechanismObservation.ProofEvents[1].Goroutine = "8" },
		"another producing value": func(r *diagnosis.Report) { r.MechanismObservation.ProofEvents[1].Value = "42" },
		"another callee":          func(r *diagnosis.Report) { r.SSAAssertionTrace.Values[1].Callee = "example.Other" },
		"wrong caller stack": func(r *diagnosis.Report) {
			r.MechanismObservation.ProofEvents[1].Frames = []string{"example.Other", "example.TestCall"}
		},
		"unrelated actual operand": func(r *diagnosis.Report) { r.SSAAssertionTrace.Values[2].Arguments[2] = "constant" },
		"another expected value":   func(r *diagnosis.Report) { r.SSAAssertionTrace.Values[3].Text = "1:float64" },
		"another assertion":        func(r *diagnosis.Report) { r.SSAAssertionTrace.AnchorID = "other" },
		"uncited proof":            func(r *diagnosis.Report) { r.RootCause.EvidenceIDs = nil },
		"duplicate occurrence": func(r *diagnosis.Report) {
			o := r.MechanismObservation
			o.ProofEvents = append(o.ProofEvents[:2], append([]diagnosis.ProbeValueObservation{o.ProofEvents[1]}, o.ProofEvents[2])...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := auditableReport(false)
			change(&r)
			if a := AuditMechanismProof(r); a.Status != "rejected" {
				t.Fatalf("contradiction not rejected: %+v", a)
			}
		})
	}
}
func TestProofAuditRecomputesConversionRatherThanTrustingFlags(t *testing.T) {
	r := auditableReport(true)
	n := r.MechanismObservation.Correlation.Numeric
	n.Output = "15"
	n.Actual = "15" // Flags still claim both loss and equality.
	if a := AuditMechanismProof(r); a.Status != "rejected" {
		t.Fatalf("impossible 270 -> int8(15) accepted: %+v", a)
	}
	r = auditableReport(true)
	events := r.MechanismObservation.ProofEvents
	events[1], events[2] = events[2], events[1]
	if a := AuditMechanismProof(r); a.Status != "rejected" {
		t.Fatalf("conversion after return accepted: %+v", a)
	}
}
func TestProofAuditMissingEvidenceIsNotARefutedDiagnosis(t *testing.T) {
	r := auditableReport(false)
	r.MechanismObservation.ProofEvents = nil
	if a := AuditMechanismProof(r); a.Status != "insufficient" {
		t.Fatalf("missing evidence mislabeled: %+v", a)
	}
	r = auditableReport(false)
	r.SSAAssertionTrace.Values = r.SSAAssertionTrace.Values[:1]
	if a := AuditMechanismProof(r); a.Status != "insufficient" {
		t.Fatalf("missing trace mislabeled: %+v", a)
	}
	r.RootCause.Status = "localized"
	if a := AuditMechanismProof(r); a.Status != "not_claimed" {
		t.Fatalf("localization treated as a mechanism claim: %+v", a)
	}
}
