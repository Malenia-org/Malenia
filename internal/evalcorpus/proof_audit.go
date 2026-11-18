package evalcorpus

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/causent/malenia/internal/diagnosis"
	programrepo "github.com/causent/malenia/internal/repository"
)

// ProofAudit validates internal evidence consistency. It does not authenticate
// report data, recompile source, inspect binary bytes or judge upstream intent.
type ProofAudit struct {
	Status                    string `json:"status"`
	Reason                    string `json:"reason"`
	BinaryBytesVerified       bool   `json:"binary_bytes_verified"`
	GroundTruthReviewRequired bool   `json:"ground_truth_review_required"`
}

func auditResult(status, reason string) ProofAudit {
	return ProofAudit{Status: status, Reason: reason, GroundTruthReviewRequired: true}
}

func AuditMechanismProof(r diagnosis.Report) ProofAudit {
	if r.RootCause.Status != "established" && r.RootCause.Status != "confirmed" {
		return auditResult("not_claimed", "no established mechanism claim")
	}
	o := r.MechanismObservation
	if o == nil || o.Correlation == nil || o.ProofVersion != "runtime-assertion-v1" || len(o.ProofEvents) < 3 {
		return auditResult("insufficient", "complete versioned assertion events were not retained")
	}
	c := o.Correlation
	if !o.SignatureMatch || !o.ValueMatched || !o.SSADataflowVerified || !o.TestFailed || !c.Verified || o.Status != "observed" || o.Mechanism == "" {
		return auditResult("rejected", "verification flags contradict the mechanism claim")
	}
	if o.Revision == "" || o.Revision != r.Environment.HeadCommit {
		return auditResult("rejected", "observation revision does not match diagnosed revision")
	}
	if data, err := hex.DecodeString(o.BinaryDigest); err != nil || len(data) != 32 {
		return auditResult("rejected", "executed binary SHA-256 is missing or malformed")
	}
	if len(o.ExecutionCommand) < 4 || o.ExecutionCommand[0] != "sh" || o.ExecutionCommand[1] != "-c" || o.ExecutionCommand[2] != `exec "$0" "$@"` || filepath.Base(o.ExecutionCommand[3]) != "malenia-probe.test" {
		return auditResult("rejected", "invocation does not identify the digested test binary")
	}
	cited := false
	for _, e := range r.Evidence {
		if e.ClaimKind == "verified_failure_mechanism" && e.Basis == "DataflowVerified" && e.Source == o.OperationFile && e.DeclarationID == o.OperationName && e.Revision == o.Revision && contains(o.EvidenceIDs, e.ID) && contains(r.RootCause.EvidenceIDs, e.ID) {
			cited = true
		}
	}
	if !cited {
		return auditResult("rejected", "mechanism evidence is not consistently cited and revision-bound")
	}
	if c.AssertionID == "" || c.Goroutine == "" || c.Test == "" || c.Invocation == 0 || c.OperationSequence == 0 {
		return auditResult("rejected", "assertion invocation identity is incomplete")
	}
	events := o.ProofEvents
	first, last := events[0], events[len(events)-1]
	boundary := func(e diagnosis.ProbeValueObservation) bool {
		return e.AssertionID == c.AssertionID && e.Goroutine == c.Goroutine && e.Test == c.Test && e.Invocation == c.Invocation
	}
	if first.Event != "assertion_begin" || last.Event != "assertion_end" || !boundary(first) || !boundary(last) {
		return auditResult("rejected", "assertion boundaries belong to different invocations")
	}
	var operations, conversions []diagnosis.ProbeValueObservation
	for _, e := range events[1 : len(events)-1] {
		if e.Goroutine != c.Goroutine || e.Event == "assertion_begin" || e.Event == "assertion_end" {
			return auditResult("rejected", "proof span contains an unrelated or nested invocation")
		}
		switch e.Event {
		case "operation", "conversion":
			if e.OperationID != o.OperationID || e.File != o.OperationFile || e.Line != o.OperationLine {
				return auditResult("rejected", "retained event identifies another operation")
			}
			if e.Event == "conversion" && len(operations) > 0 {
				return auditResult("rejected", "conversion occurs after the producing return")
			}
			if e.Event == "operation" {
				operations = append(operations, e)
			} else {
				conversions = append(conversions, e)
			}
		default:
			return auditResult("insufficient", "unknown event in versioned proof span")
		}
	}
	if len(operations) != 1 {
		return auditResult("rejected", "span does not contain exactly one producing operation")
	}
	op := operations[0]
	if op.Sequence != c.OperationSequence || op.Value != last.Value || last.Value == last.Expected || op.Slot < 1 {
		return auditResult("rejected", "producing value does not match the failed assertion")
	}
	if !contains(o.ObservedValues, op.Value) {
		return auditResult("rejected", "correlated value is absent from recorded observations")
	}
	recorded := 0
	for _, v := range o.Observations {
		if v.Sequence == op.Sequence && v.Goroutine == op.Goroutine {
			if v.OperationID != op.OperationID || v.Value != op.Value || v.Slot != op.Slot {
				return auditResult("rejected", "span contradicts the observation list")
			}
			recorded++
		}
	}
	if recorded != 1 {
		return auditResult("rejected", "operation occurrence is not uniquely recorded")
	}
	if r.SSAAssertionTrace == nil {
		return auditResult("insufficient", "SSA trace is absent")
	}
	values := map[string]programrepo.SSAValue{}
	for _, v := range r.SSAAssertionTrace.Values {
		values[v.ID] = v
	}
	if c.AssertionID != r.SSAAssertionTrace.AnchorID {
		return auditResult("rejected", "assertion identity differs from the failed SSA anchor")
	}
	anchor, ok := values[c.AssertionID]
	if !ok {
		return auditResult("insufficient", "failed assertion anchor is absent")
	}
	if len(c.ReturnPath) < 2 {
		return auditResult("insufficient", "return path is absent")
	}
	actual, retainedActual := values[c.ReturnPath[len(c.ReturnPath)-1]]
	if !retainedActual {
		return auditResult("insufficient", "actual call was not retained")
	}
	if anchor.Kind != "*ssa.Call" || anchor.Function != actual.Function {
		return auditResult("rejected", "actual call belongs to another assertion function")
	}
	rootFunction := strings.Split(anchor.Function, "$")[0]
	rootTest := rootFunction[strings.LastIndex(rootFunction, ".")+1:]
	if c.Test != rootTest && !strings.HasPrefix(c.Test, rootTest+"/") {
		return auditResult("rejected", "subtest does not belong to the failed test function")
	}
	if issue := auditAssertionBinding(o, values, anchor, actual, last); issue != nil {
		return *issue
	}
	names, issue := auditReturnEdges(values, c.ReturnPath, op)
	if issue != nil {
		return *issue
	}
	if len(op.Frames) < len(names) {
		return auditResult("rejected", "caller stack is shorter than the claimed return path")
	}
	for i, name := range names {
		if name == "" {
			return auditResult("insufficient", "unsupported runtime function identity")
		}
		if op.Frames[i] != name {
			return auditResult("rejected", "runtime frames contradict the return path")
		}
	}
	if c.Numeric != nil {
		if len(conversions) != 1 || conversions[0].Numeric == nil || *conversions[0].Numeric != *c.Numeric || conversions[0].Function != op.Function || conversions[0].Slot != op.Slot {
			return auditResult("rejected", "numeric mechanism has no unique matching conversion event")
		}
		n := conversions[0].Numeric
		if len(n.Input) > 22 || len(n.Output) > 22 || len(n.Actual) > 22 {
			return auditResult("rejected", "primitive integer observation exceeds its representation bound")
		}
		if issue := auditIntegerCast(n); issue != nil {
			return *issue
		}
		a, aOK := new(big.Int).SetString(n.Input, 10)
		b, bOK := new(big.Int).SetString(n.Output, 10)
		if !aOK || !bOK || a.Cmp(b) == 0 || !n.Lossy || !n.MatchesActual || n.Output != n.Actual || n.InputType == "" || n.OutputType == "" || n.OutputType != n.ActualType {
			return auditResult("rejected", "conversion does not establish numeric loss and equality with actual")
		}
		if op.Value != "true" || last.Expected != "false" || last.Predicate == nil || !*last.Predicate || c.Actual != op.Value || c.Expected != last.Expected {
			return auditResult("rejected", "numeric evidence does not match the failed predicate")
		}
		ret := values[op.OperationID]
		if len(ret.Operands) == 0 {
			return auditResult("insufficient", "comparison return operand is absent")
		}
		comparison := values[ret.Operands[0]]
		if comparison.Callee != "reflect.DeepEqual" || len(comparison.Arguments) != 2 {
			return auditResult("rejected", "narrowing is not the returned reflection comparison")
		}
		unbox := values[comparison.Arguments[0]]
		if unbox.Callee != "(reflect.Value).Interface" || len(unbox.Arguments) != 1 {
			return auditResult("insufficient", "unboxing edge is absent")
		}
		if values[unbox.Arguments[0]].Callee != "(reflect.Value).Convert" {
			return auditResult("insufficient", "typed conversion edge is absent")
		}
	} else {
		if len(conversions) != 0 {
			return auditResult("rejected", "unclaimed conversion event in an error-return proof")
		}
		if op.ErrorPresent == nil || !*op.ErrorPresent || !c.ErrorPresent {
			return auditResult("rejected", "non-nil error observation is absent")
		}
		if !values[op.OperationID].ErrorReturn {
			return auditResult("rejected", "selected return does not return a dynamic error")
		}
	}
	return auditResult("consistent", "retained events, scalar return edges and mechanism evidence are internally consistent; source and upstream attribution still require independent review")
}

func auditReturnEdges(values map[string]programrepo.SSAValue, path []string, op diagnosis.ProbeValueObservation) ([]string, *ProofAudit) {
	fail := func(status, reason string) ([]string, *ProofAudit) { a := auditResult(status, reason); return nil, &a }
	if len(path) < 2 {
		return fail("insufficient", "return path is absent")
	}
	if len(path)%2 != 0 || path[0] != op.OperationID {
		return fail("rejected", "return path does not start at the producing operation")
	}
	ret, ok := values[path[0]]
	if !ok {
		return fail("insufficient", "selected return is absent")
	}
	if ret.Kind != "*ssa.Return" || ret.File != op.File || ret.Line != op.Line || ret.Function != op.Function {
		return fail("rejected", "selected return contradicts operation identity")
	}
	names := []string{auditRuntimeName(ret.Function)}
	function, slot := ret.Function, op.Slot-1
	if slot < 0 || slot >= len(ret.Operands) {
		return fail("rejected", "selected slot is outside the return")
	}
	for i := 1; i < len(path); i += 2 {
		call, ok := values[path[i]]
		if !ok {
			return fail("insufficient", "caller instruction is absent")
		}
		if call.Kind != "*ssa.Call" || call.Callee != function {
			return fail("rejected", "return path crosses a different callee")
		}
		names = append(names, auditRuntimeName(call.Function))
		if i == len(path)-1 {
			if slot != 0 || strings.HasPrefix(call.Type, "(") {
				return fail("rejected", "actual call is not the claimed scalar slot")
			}
			return names, nil
		}
		next, ok := values[path[i+1]]
		if !ok {
			return fail("insufficient", "wrapper return is absent")
		}
		if next.Kind != "*ssa.Return" || next.Function != call.Function || auditBlock(next.ID) != auditBlock(call.ID) {
			return fail("rejected", "wrapper return crosses a different function or block")
		}
		matches, newSlot, missing := 0, -1, false
		for j, id := range next.Operands {
			value, known := values[id]
			direct := id == call.ID && slot == 0 && !strings.HasPrefix(call.Type, "(")
			extract := known && value.Kind == "*ssa.Extract" && len(value.Operands) == 1 && value.Operands[0] == call.ID && value.TupleIndex == slot
			if direct || extract {
				matches++
				newSlot = j
			} else if !known {
				missing = true
			}
		}
		if matches != 1 {
			if missing {
				return fail("insufficient", "tuple extraction edge was not retained")
			}
			return fail("rejected", "wrapper does not uniquely return the producing slot")
		}
		function, slot = next.Function, newSlot
	}
	return fail("insufficient", "actual call is missing")
}

func auditBlock(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[:i]
	}
	return ""
}
func auditRuntimeName(id string) string {
	if strings.HasPrefix(id, "(*") {
		end := strings.Index(id, ").")
		if end < 0 {
			return ""
		}
		receiver := id[2:end]
		dot := strings.LastIndex(receiver, ".")
		if dot < 0 {
			return ""
		}
		id = fmt.Sprintf("%s.(*%s).%s", receiver[:dot], receiver[dot+1:], id[end+2:])
	} else if strings.HasPrefix(id, "(") {
		end := strings.Index(id, ").")
		if end < 0 {
			return ""
		}
		id = id[1:end] + "." + id[end+2:]
	}
	if strings.Count(id, "$") > 1 {
		return ""
	}
	return strings.ReplaceAll(id, "$", ".func")
}

// Recompute primitive Go integer conversion independently of engine flags.
// Named types and platform-sized destinations need additional type metadata.
func auditIntegerCast(n *diagnosis.NumericConversionEvidence) *ProofAudit {
	failure := func(status, reason string) *ProofAudit { r := auditResult(status, reason); return &r }
	types := map[string]struct {
		bits   uint
		signed bool
	}{"int8": {8, true}, "int16": {16, true}, "int32": {32, true}, "int64": {64, true}, "uint8": {8, false}, "uint16": {16, false}, "uint32": {32, false}, "uint64": {64, false}}
	target, ok := types[n.OutputType]
	if !ok {
		return failure("insufficient", "destination integer width is not independently available")
	}
	before, ok := new(big.Int).SetString(n.Input, 10)
	if !ok {
		return failure("rejected", "conversion input is not an integer")
	}
	after, ok := new(big.Int).SetString(n.Output, 10)
	if !ok {
		return failure("rejected", "conversion output is not an integer")
	}
	if source, known := types[n.InputType]; known {
		limit := new(big.Int).Lsh(big.NewInt(1), source.bits)
		min := new(big.Int)
		if source.signed {
			limit.Rsh(limit, 1)
			min.Neg(limit)
		}
		if before.Cmp(min) < 0 || before.Cmp(limit) >= 0 {
			return failure("rejected", "observed input is outside its recorded primitive type")
		}
	} else if n.InputType != "int" && n.InputType != "uint" {
		return failure("insufficient", "source integer type metadata is incomplete")
	}
	if n.InputType == "int" && !before.IsInt64() {
		return failure("rejected", "recorded int exceeds every supported Go word size")
	}
	if n.InputType == "uint" && !before.IsUint64() {
		return failure("rejected", "recorded uint exceeds every supported Go word size")
	}
	if n.InputType == "uint" && before.Sign() < 0 {
		return failure("rejected", "negative value recorded as uint")
	}
	modulus := new(big.Int).Lsh(big.NewInt(1), target.bits)
	converted := new(big.Int).Mod(before, modulus)
	if target.signed && converted.Bit(int(target.bits-1)) == 1 {
		converted.Sub(converted, modulus)
	}
	if converted.Cmp(after) != 0 {
		return failure("rejected", "observed output contradicts Go integer conversion arithmetic")
	}
	return nil
}

func auditAssertionBinding(o *diagnosis.RuntimeMechanismObservation, values map[string]programrepo.SSAValue, anchor, actual programrepo.SSAValue, end diagnosis.ProbeValueObservation) *ProofAudit {
	fail := func(status, reason string) *ProofAudit { a := auditResult(status, reason); return &a }
	switch o.SignatureKind {
	case "expected_actual":
		if (anchor.Callee != "github.com/stretchr/testify/assert.Equal" && anchor.Callee != "github.com/stretchr/testify/require.Equal") || len(anchor.Arguments) < 3 {
			return fail("rejected", "failed anchor is not the declared equality assertion contract")
		}
		produced, ok := auditUnwrapValue(values, anchor.Arguments[2])
		if !ok {
			return fail("insufficient", "assertion actual operand was not retained")
		}
		if produced.ID != actual.ID {
			return fail("rejected", "assertion actual does not consume the claimed producing call")
		}
		expected, ok := auditUnwrapValue(values, anchor.Arguments[1])
		if !ok {
			return fail("insufficient", "assertion expected operand was not retained")
		}
		if expected.Kind != "constant" {
			return fail("rejected", "declared expected operand is not a constant")
		}
		at := strings.LastIndex(expected.Text, ":")
		if at < 0 {
			return fail("insufficient", "constant representation is unavailable")
		}
		scalar := expected.Text[:at]
		if decoded, err := strconv.Unquote(scalar); err == nil {
			scalar = decoded
		}
		if scalar != end.Expected {
			return fail("rejected", "retained expected value contradicts the assertion operand")
		}
	case "source_predicate_and_failure_message":
		if anchor.Callee != "(*testing.common).Errorf" || actual.Type != "bool" || end.Predicate == nil || !*end.Predicate {
			return fail("rejected", "declared native predicate contract is inconsistent")
		}
		connected := false
		for _, id := range anchor.Conditions {
			v := values[id]
			if v.Kind == "*ssa.BinOp" && strings.Contains(v.Text, " != ") && len(v.Operands) == 2 && v.Operands[0] == actual.ID {
				connected = true
			}
		}
		if !connected {
			return fail("insufficient", "actual-to-failing-predicate dependency was not retained")
		}
	default:
		return fail("insufficient", "assertion contract version is unavailable")
	}
	return nil
}
func auditUnwrapValue(values map[string]programrepo.SSAValue, id string) (programrepo.SSAValue, bool) {
	for i := 0; i < 8; i++ {
		v, ok := values[id]
		if !ok {
			return v, false
		}
		if v.Kind != "*ssa.MakeInterface" && v.Kind != "*ssa.ChangeType" {
			return v, true
		}
		if len(v.Operands) != 1 {
			return v, false
		}
		id = v.Operands[0]
	}
	return programrepo.SSAValue{}, false
}
