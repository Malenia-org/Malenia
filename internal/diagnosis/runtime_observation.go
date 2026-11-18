package diagnosis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/causent/malenia/internal/domain"
	"github.com/causent/malenia/internal/execution"
	programrepo "github.com/causent/malenia/internal/repository"
)

// RuntimeMechanismObservation records a bounded, explicitly-enabled runtime
// observation of the operation feeding a failing assertion. It is new evidence
// from a controlled execution, never a re-interpretation of the original CI
// address offsets. A rebuilt binary is never correlated with original offsets.
type RuntimeMechanismObservation struct {
	Status         string                  `json:"status"` // observed, partial, unavailable, disabled
	Question       string                  `json:"question,omitempty"`
	OperationFile  string                  `json:"operation_file,omitempty"`
	OperationLine  int                     `json:"operation_line,omitempty"`
	OperationName  string                  `json:"operation_name,omitempty"`
	ObservedValues []string                `json:"observed_values,omitempty"`
	OperationID    string                  `json:"operation_id,omitempty"`
	Observations   []ProbeValueObservation `json:"observations,omitempty"`
	Correlation    *AssertionCorrelation   `json:"assertion_correlation,omitempty"`
	Mechanism      string                  `json:"mechanism,omitempty"`
	AssertionText  string                  `json:"assertion_text,omitempty"`
	// SignatureMatch means the rerun failed on the original expected/actual
	// pair. ValueMatched additionally requires an observed probe value to equal
	// the original failing value. SSADataflowVerified requires an exact SSA
	// value at the probed operation among the assertion's static dependencies.
	// These flags alone never prove invocation identity or a failure mechanism.
	SignatureMatch      bool     `json:"signature_match"`
	SignatureKind       string   `json:"signature_kind,omitempty"`
	ValueMatched        bool     `json:"value_matched"`
	SSADataflowVerified bool     `json:"ssa_dataflow_verified"`
	Command             []string `json:"command,omitempty"`
	ExecutionCommand    []string `json:"execution_command,omitempty"`
	Revision            string   `json:"revision,omitempty"`
	Toolchain           string   `json:"toolchain,omitempty"`
	// BinaryDigest is the SHA-256 of the exact binary produced the observation.
	BinaryDigest  string                `json:"binary_digest,omitempty"`
	TestName      string                `json:"test_name,omitempty"`
	TestFailed    bool                  `json:"test_failed"`
	EvidenceIDs   []string              `json:"evidence_ids,omitempty"`
	OutputExcerpt string                `json:"output_excerpt,omitempty"`
	Limitations   []string              `json:"limitations,omitempty"`
	NextActions   []string              `json:"next_actions,omitempty"`
	Provenance    string                `json:"provenance,omitempty"`
	Attempts      []RuntimeProbeAttempt `json:"attempts,omitempty"`
	StopReason    string                `json:"stop_reason,omitempty"`
	repeated      []runtimeProbeRepeat
	failureStage  string
	hypotheses    []string
}

const (
	runtimeProbeMarker = "MALENIA-RUNTIME-PROBE"
	runtimeProbeHelper = "malenia_runtime_probe_log"
)

// collectRuntimeMechanismObservation runs a bounded probe when explicitly
// enabled. It returns nil when disabled, unavailable, or when no specific
// operation/gap is identified. It records new evidence on the report but never
// claims a root cause itself: assessRootCause decides whether the observation
// is sufficient to establish a mechanism.
func (d *Diagnoser) collectRuntimeMechanismObservation(ctx context.Context, report *Report, workspace string, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) *RuntimeMechanismObservation {
	if !d.cfg.RuntimeObservation || d.cfg.Executor == nil || ctx.Err() != nil || workspace == "" {
		return nil
	}
	observation := d.probeRuntimeMechanism(ctx, report, workspace, snapshot, candidates)
	if observation != nil {
		report.MechanismObservation = observation
	}
	return observation
}

// probeRuntimeMechanism performs the actual bounded probe. It is separated
// from the recording wrapper so every call path records the observation once.
func (d *Diagnoser) probeRuntimeMechanism(ctx context.Context, report *Report, workspace string, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) *RuntimeMechanismObservation {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	operations := rankedProbeOperations(report, snapshot, candidates)
	if len(operations) < 5 {
		operations = append(operations, sourceProbeOperations(report, workspace, snapshot, candidates)...)
	}
	var best *RuntimeMechanismObservation
	bestRank := -1
	var attempts []RuntimeProbeAttempt
	seen := map[string]bool{}
	stop := "candidates_exhausted"
	for _, operation := range operations {
		if ctx.Err() != nil {
			stop = "case_deadline"
			break
		}
		key := fmt.Sprintf("%s:%d:%d:%d", operation.File, operation.Line, operation.Column, operation.Slot)
		if seen[key] {
			continue
		}
		if len(attempts) == 5 {
			stop = "candidate_limit"
			break
		}
		seen[key] = true
		started := time.Now()
		candidateCtx, done := context.WithTimeout(ctx, 60*time.Second)
		obs := d.runProbeCandidate(candidateCtx, report, workspace, snapshot, operation)
		expired := candidateCtx.Err() != nil
		done()
		attempt := summarizeProbeAttempt(report, operation, obs, time.Since(started), expired)
		attempts = append(attempts, attempt)
		if rank := probeObservationRank(obs, attempt); rank > bestRank {
			best, bestRank = obs, rank
		}
		if obs.Mechanism != "" && attempt.Correlated {
			stop = "mechanism_established"
			break
		}
	}
	if best == nil {
		best = &RuntimeMechanismObservation{Status: "unavailable", Limitations: []string{"no observable operation identified for a runtime value probe"}}
		if ctx.Err() != nil {
			stop = "case_deadline"
		} else {
			stop = "no_candidate"
		}
	}
	best.Attempts, best.StopReason = attempts, stop
	return best
}

func (d *Diagnoser) runProbeCandidate(ctx context.Context, report *Report, workspace string, snapshot programrepo.Snapshot, operation *probeOperation) *RuntimeMechanismObservation {
	probeDir, err := os.MkdirTemp("", "malenia-runtime-probe-")
	if err != nil {
		return &RuntimeMechanismObservation{Status: "unavailable", failureStage: "workspace", Limitations: []string{"probe workspace unavailable"}}
	}
	defer os.RemoveAll(probeDir)
	if err := copyProbeWorkspace(workspace, probeDir); err != nil {
		return &RuntimeMechanismObservation{Status: "unavailable", failureStage: "workspace", Limitations: []string{"probe workspace copy failed: " + err.Error()}}
	}

	operation.NumericComparison = numericComparisonSupported(snapshot.GoSSA, workspace, operation)
	label, _, err := injectOperationProbe(filepath.Join(probeDir, operation.File), operation)
	if err != nil {
		return &RuntimeMechanismObservation{
			Status:        "partial",
			failureStage:  "instrumentation",
			OperationFile: operation.File, OperationLine: operation.Line,
			Limitations: []string{"operation could not be instrumented: " + err.Error()},
			NextActions: []string{"choose a resolvable single assignment or return at the reported operation"},
		}
	}
	if err := writeProbeHelper(filepath.Join(probeDir, operation.File)); err != nil {
		return &RuntimeMechanismObservation{Status: "partial", failureStage: "instrumentation", Limitations: []string{"probe helper unavailable: " + err.Error()}}
	}
	assertionPlan := planAssertionProbe(report, snapshot, workspace, operation)
	if assertionPlan != nil {
		if err := injectAssertionProbe(filepath.Join(probeDir, assertionPlan.File), assertionPlan); err != nil {
			assertionPlan = nil
		} else if err := writeProbeHelper(filepath.Join(probeDir, assertionPlan.File)); err != nil {
			return &RuntimeMechanismObservation{Status: "partial", failureStage: "instrumentation", Limitations: []string{"assertion helper unavailable: " + err.Error()}}
		}
	}

	command := probeCommand(report, operation.File)
	obs := &RuntimeMechanismObservation{
		Status:        "partial",
		Question:      fmt.Sprintf("what value does %s:%d actually produce at the failing assertion?", operation.File, operation.Line),
		OperationFile: operation.File, OperationLine: operation.Line,
		OperationName: operation.Name, Command: command,
		OperationID: operation.ID,
		Revision:    firstNonEmpty(report.Environment.HeadCommit, gitOutput(probeDir, "rev-parse", "HEAD")),
		Toolchain:   report.Environment.GoCommand,
	}

	if assertionPlan != nil {
		switch {
		case operation.NumericComparison:
			obs.hypotheses = []string{"integer conversion changes the value and makes it equal the comparison operand", "the conversion does not produce that lossy equality"}
		case operation.ErrorReturn:
			obs.hypotheses = []string{"the wrong returned value accompanies a non-nil error", "the wrong returned value accompanies a nil error"}
		}
	}

	// Build one test binary from the instrumented source and execute THAT
	// binary, so the recorded digest identifies the binary which actually
	// produced the observation. No second, unrelated build is performed.
	runs, digest, execCommand, buildErr := d.probeBuildAndRun(ctx, probeDir, command)
	obs.BinaryDigest, obs.ExecutionCommand = digest, execCommand
	if buildErr != nil {
		obs.failureStage = "execution"
		if digest == "" {
			obs.failureStage = "build"
		}
		obs.Limitations = append(obs.Limitations, buildErr.Error())
	}
	if len(runs) == 0 {
		return obs
	}
	original := failureText(report.Baseline, report.Evidence)
	for _, run := range runs {
		output := run.Stdout + "\n" + run.Stderr
		obs.repeated = append(obs.repeated, runtimeProbeRepeat{
			output: output, failed: run.ExitCode != 0 && !run.TimedOut,
			values:      parseOperationValues(output, operation),
			correlation: correlateAssertion(output, original, operation, assertionPlan),
		})
	}
	res := runs[0]
	combined := res.Stdout + "\n" + res.Stderr
	obs.OutputExcerpt = bound(combined, MaxEvidenceExcerpt)
	obs.Observations = parseOperationValues(combined, operation)
	var values []string
	for _, observed := range obs.Observations {
		values = append(values, observed.Value)
	}
	obs.ObservedValues = values
	obs.AssertionText = probeAssertion(combined)
	obs.TestFailed = res.ExitCode != 0 || strings.Contains(combined, "--- FAIL") || strings.Contains(combined, "FAIL\t")

	// Gate A: the rerun must fail on the original expected/actual pair.
	observedSig, observedOK := extractFailureSignature(combined)
	originalSig, originalOK := originalFailureSignature(report)
	obs.SignatureMatch = obs.TestFailed && !res.TimedOut && observedOK && originalOK && observedSig == originalSig && len(values) > 0

	// Gate B: an observed probe value must actually equal the original failing
	// value. A matching assertion signature with an unrelated logged value is
	// not evidence that this operation produced the failure.
	obs.ValueMatched = observedOK && originalOK && probeValueMatchesFailure(values, originalSig.actual)

	// Gate C: typed SSA must place an exact value at the probed operation which
	// feeds the failing assertion. A file match or a substring of a function
	// name is not an operation-to-assertion dependency.
	obs.SSADataflowVerified = probeSSADataflowSupports(report, operation)
	obs.Correlation = obs.repeated[0].correlation
	obs.SignatureKind = "expected_actual"
	if assertionPlan != nil && assertionPlan.NativePredicate && obs.Correlation != nil && obs.Correlation.Verified {
		obs.SignatureKind = "source_predicate_and_failure_message"
		obs.SignatureMatch = obs.TestFailed && !res.TimedOut
		obs.ValueMatched = probeValueMatchesFailure(values, obs.Correlation.Actual)
		observedSig = probeFailureSignature{expected: obs.Correlation.Expected, actual: obs.Correlation.Actual}
	}

	if obs.SignatureMatch && obs.ValueMatched {
		obs.Status = "observed"
		obs.Provenance = "bounded isolated runtime probe of the checked-out revision; new observation, not original CI"
		ev := EvidenceItem{
			ID:            "E-runtime-probe-" + digestString(report.InvestigationID + operation.File + fmt.Sprint(operation.Line) + strings.Join(values, ","))[:12],
			Kind:          "runtime_mechanism_observation",
			ClaimKind:     "RuntimeObservation",
			Basis:         "RuntimeMapped",
			Source:        operation.File,
			DeclarationID: operation.Name,
			Detail: fmt.Sprintf("runtime probe at %s:%d\nquestion: %s\nobserved: %s\nassertion: %s\nrevision: %s\ntoolchain: %s\nexecuted binary digest: %s\nexecution command: %s\n%s",
				operation.File, operation.Line, obs.Question, strings.Join(values, ", "), obs.AssertionText, obs.Revision, obs.Toolchain, obs.BinaryDigest, strings.Join(execCommand, " "), label),
			Provenance: obs.Provenance,
			Revision:   obs.Revision,
			Digest:     digestString(combined),
		}
		report.Evidence = append(report.Evidence, ev)
		obs.EvidenceIDs = append(obs.EvidenceIDs, ev.ID)
		if stableProbeCorrelation(report, obs.repeated) && obs.Correlation != nil && obs.Correlation.Verified && obs.SSADataflowVerified && ((operation.ErrorReturn && obs.Correlation.ErrorPresent) || verifiedIntegerConversion(obs.Correlation.Numeric)) {
			if n := obs.Correlation.Numeric; verifiedIntegerConversion(n) {
				obs.Limitations = append(obs.Limitations, "mechanism verification covers the correlated integer comparison invocation; other failure episodes are not independently verified")
				obs.Mechanism = fmt.Sprintf("%s converts %s (%s) to %s (%s), losing numeric value; the converted value equals actual %s (%s), so equality returns true in %s where the failed predicate expected false", operation.Name, n.Input, n.InputType, n.Output, n.OutputType, n.Actual, n.ActualType, obs.Correlation.Test)
			} else {
				obs.Mechanism = fmt.Sprintf("%s returns %s alongside a non-nil error; exact scalar return slots propagate that value unchanged to %s, where the assertion expected %s", operation.Name, observedSig.actual, obs.Correlation.Test, observedSig.expected)
			}
			mechanism := ev
			mechanism.ID = "E-mechanism-" + digestString(obs.OperationID + obs.Correlation.AssertionID + obs.Revision)[:16]
			mechanism.Kind, mechanism.ClaimKind, mechanism.Basis = "failure_mechanism", "verified_failure_mechanism", BasisDataflowVerified
			mechanism.Detail = obs.Mechanism + "\n" + ev.Detail + fmt.Sprintf("\nassertion correlation: %+v", *obs.Correlation)
			report.Evidence = append(report.Evidence, mechanism)
			obs.EvidenceIDs = append(obs.EvidenceIDs, mechanism.ID)
			return obs
		}

		if !obs.SSADataflowVerified {
			obs.Limitations = append(obs.Limitations, "no exact SSA value at the probed operation feeds the assertion; the runtime values are an observation, not a verified mechanism")
			obs.NextActions = append(obs.NextActions, "identify the exact SSA value feeding the assertion and probe that operation")
		}
		// A static may-dependency plus an equal runtime value does not prove
		// that this invocation supplied the failed assertion, or explain the
		// violated invariant. Preserve the observation without minting proof.
		obs.Limitations = append(obs.Limitations, "operation observations are not bound to the failing assertion invocation; value equality and static dependencies do not establish a root cause")
		obs.NextActions = append(obs.NextActions, "verify the operation-to-assertion invocation and violated invariant before promoting this observation")
	} else {
		if !obs.SignatureMatch {
			obs.Limitations = append(obs.Limitations, "runtime values did not reproduce the original assertion signature")
		}
		if !obs.ValueMatched {
			obs.Limitations = append(obs.Limitations, "no observed probe value matched the original failing value")
		}
		obs.NextActions = append(obs.NextActions, "probe the specific argument/return whose value equals the original failure")
	}
	return obs
}

type probeOperation struct {
	File              string
	Line              int
	Column            int
	Name              string
	ID                string
	FunctionID        string
	Slot              int
	Kind              string
	ErrorReturn       bool
	NumericComparison bool
}

// mechanismEvidenceVerified requires the cited contract-aligned proof item.
// Static dependencies and equal values alone cannot produce this proof.
func mechanismEvidenceVerified(report Report, obs *RuntimeMechanismObservation) bool {
	if obs == nil || obs.Revision == "" || obs.Revision != report.Environment.HeadCommit {
		return false
	}
	cited := map[string]bool{}
	for _, id := range obs.EvidenceIDs {
		cited[id] = true
	}
	for _, item := range report.Evidence {
		if cited[item.ID] && item.ClaimKind == "verified_failure_mechanism" && item.Basis == BasisDataflowVerified &&
			filepath.ToSlash(item.Source) == filepath.ToSlash(obs.OperationFile) &&
			item.DeclarationID == obs.OperationName && item.Revision == obs.Revision && item.ID != "" {
			return true
		}
	}
	return false
}

// probeSSADataflowSupports reports whether typed SSA traced an exact value at
// the probed operation (file, line and enclosing function) among the value
// dependencies feeding the failed assertion. A matching file alone or a
// substring of a function name is not an operation-to-assertion dependency and
// is deliberately rejected.
func probeSSADataflowSupports(report *Report, op *probeOperation) bool {
	trace := report.SSAAssertionTrace
	if trace == nil || len(trace.Values) == 0 || op.File == "" || op.Line <= 0 || op.Name == "" {
		return false
	}
	for _, value := range trace.Values {
		if filepath.ToSlash(value.File) == filepath.ToSlash(op.File) &&
			value.Line == op.Line && value.Function == op.FunctionID && value.ID == op.ID && op.ID != "" {
			return true
		}
	}
	return false
}

// probeValueMatchesFailure reports whether one of the values observed by the
// probe equals the original failing value. Complete scalar values are compared
// so an embedded struct field or a different number cannot satisfy the gate.
func probeValueMatchesFailure(values []string, failingValue string) bool {
	if failingValue == "" {
		return false
	}
	for _, value := range values {
		if value == failingValue {
			return true
		}
	}
	return false
}

// observableSSA instructs which SSA operation kinds can be observed at runtime
// without formatting composite values or invoking arbitrary String/Error code.
// The probe helper renders only primitives, so a composite-producing operation
// is still eligible as long as the chosen result slot is a primitive/bool.
var observableSSA = map[string]bool{
	"*ssa.Call":       true,
	"*ssa.BinOp":      true,
	"*ssa.UnOp":       true,
	"*ssa.Convert":    true,
	"*ssa.Store":      true,
	"*ssa.FieldAddr":  true,
	"*ssa.IndexAddr":  true,
	"*ssa.Lookup":     true,
	"*ssa.Extract":    true,
	"*ssa.TypeAssert": true,
	"*ssa.Slice":      true,
	"*ssa.Return":     true,
}

// selectProbeOperation starts from the failed assertion's SSA dependencies and
// follows them into implementation code. It accepts any observable SSA
// operation, not only calls and comparisons, because the decisive dependency
// is often a store, field write, load or tuple slot. Exact SSA identity and
// source column are retained so the operation can be instrumented precisely.
func selectProbeOperation(report *Report, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) *probeOperation {
	operations := rankedProbeOperations(report, snapshot, candidates)
	if len(operations) == 0 {
		return nil
	}
	return operations[0]
}

func rankedProbeOperations(report *Report, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) []*probeOperation {
	if report.SSAAssertionTrace == nil {
		return nil
	}
	values := append([]programrepo.SSAValue(nil), report.SSAAssertionTrace.Values...)
	byID := map[string]programrepo.SSAValue{}
	for _, v := range values {
		byID[v.ID] = v
	}
	if snapshot.GoSSA != nil {
		for id, v := range snapshot.GoSSA.Values {
			byID[id] = v
		}
	}
	priority := func(v programrepo.SSAValue) int {
		if v.ErrorReturn {
			return 5
		}
		if v.Kind == "*ssa.Return" && v.TraceSlot != nil {
			if slot := *v.TraceSlot; slot >= 0 && slot < len(v.Operands) {
				switch byID[v.Operands[slot]].Kind {
				case "constant", "":
					return 1
				case "parameter", "free_variable":
					return 2
				case "*ssa.BinOp", "*ssa.UnOp", "*ssa.Convert", "*ssa.ChangeType":
					return 4
				default:
					return 3
				}
			}
			return 1
		}
		return 0
	}
	sort.SliceStable(values, func(i, j int) bool {
		p, q := priority(values[i]), priority(values[j])
		if p != q {
			return p > q
		}
		return probeCandidateRank(candidates, values[i].File, values[i].Line) <
			probeCandidateRank(candidates, values[j].File, values[j].Line)
	})
	var operations []*probeOperation
	for _, v := range values {
		if v.ID == "" || v.Function == "" || v.Line <= 0 || strings.HasSuffix(v.File, "_test.go") {
			continue
		}
		if !observableSSA[v.Kind] {
			continue
		}
		file, line, column, slot := v.File, v.Line, v.Column, 0
		if v.Kind == "*ssa.Return" {
			if v.TraceSlot == nil {
				continue
			}
			slot = *v.TraceSlot + 1
		}
		if v.Kind == "*ssa.Extract" {
			// An Extract is one result slot of a multi-result call. Bind to the
			// producing call's source location so the exact slot can be logged.
			slot = v.TupleIndex + 1 // probeOperation.Slot uses 1-based slots
			if len(v.Operands) > 0 {
				if producer, ok := byID[v.Operands[0]]; ok && producer.Line > 0 {
					file, line, column = producer.File, producer.Line, producer.Column
				}
			}
			if slot <= 0 {
				continue
			}
		}
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		// Without column-precise identity, several eligible operations on one
		// line cannot be told apart; do not silently pick one.
		if v.Column == 0 {
			peers := 0
			for _, other := range values {
				if other.ID != v.ID && other.File == file && other.Line == line && other.Function == v.Function && observableSSA[other.Kind] {
					peers++
				}
			}
			if peers > 0 {
				continue
			}
		}
		name := shortSSAFunctionName(v.Function)
		if node := nodeForFileLine(snapshot.Nodes, file, line); node != nil && node.Name != "" {
			name = node.Name
		}
		if name == "" {
			continue
		}
		operations = append(operations, &probeOperation{File: file, Line: line, Column: column, Name: name, ID: v.ID, FunctionID: v.Function, Slot: slot, Kind: v.Kind, ErrorReturn: v.ErrorReturn})
	}
	return operations
}

func shortSSAFunctionName(id string) string {
	if idx := strings.LastIndex(id, "."); idx >= 0 && idx+1 < len(id) {
		return strings.Trim(id[idx+1:], "()*")
	}
	return ""
}

func nodeForFileLine(nodes []domain.ProgramNode, file string, line int) *domain.ProgramNode {
	var best *domain.ProgramNode
	for i := range nodes {
		n := &nodes[i]
		if n.FilePath != file || n.StartLine > line || n.EndLine < line {
			continue
		}
		if best == nil || (n.EndLine-n.StartLine) < (best.EndLine-best.StartLine) {
			best = n
		}
	}
	return best
}

// copyProbeWorkspace makes a bounded copy of the checkout into a disposable
// directory. It excludes VCS and dependency caches; runtime execution remains
// gated by the executor policy and the explicit RuntimeObservation flag.
func copyProbeWorkspace(src, dst string) error {
	const maxFiles = 2000
	const maxBytes = 16 << 20
	files, bytesWritten := 0, int64(0)
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != src && (name == ".git" || name == ".malenia" || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.Rel(src, filepath.Join(filepath.Dir(path), link))
			if err != nil || filepath.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return fmt.Errorf("probe fixture symlink escapes workspace: %s", path)
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if files >= maxFiles {
				return fmt.Errorf("probe workspace copy budget exceeded")
			}
			files++
			return os.Symlink(link, target)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if files >= maxFiles || bytesWritten >= maxBytes {
			return fmt.Errorf("probe workspace copy budget exceeded")
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytesWritten+int64(len(data)) > maxBytes {
			return fmt.Errorf("probe workspace byte budget exceeded")
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		files++
		bytesWritten += int64(len(data))
		return nil
	})
}

// injectOperationProbe instruments exactly one SSA-identified expression at the
// selected source location. It supports assignments (including tuple result
// slots), returns and simple conditions. Expression evaluation count and order
// are preserved: values are captured into temporaries before logging, and
// short-circuit conditions are explicitly rejected.
func injectOperationProbe(path string, op *probeOperation) (label, funcName string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, parser.ParseComments)
	if err != nil {
		return "", "", err
	}
	var enclosing *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Body != nil && op.Line >= fset.Position(fn.Pos()).Line && op.Line <= fset.Position(fn.End()).Line {
			if op.Name != "" && op.Name != fn.Name.Name {
				continue
			}
			enclosing = fn
			break
		}
	}
	if enclosing == nil {
		return "", "", fmt.Errorf("no matching function encloses operation line %d", op.Line)
	}
	funcName = enclosing.Name.Name
	if op.ID == "" {
		return "", funcName, fmt.Errorf("missing operation identity")
	}

	// Collect candidate statements at the reported line. Column-precise SSA
	// identity lets us disambiguate several operations on the same line.
	type candidate struct {
		stmt   ast.Stmt
		list   *[]ast.Stmt
		expr   ast.Expr
		column int
		end    int
		nested bool
	}
	var candidates []candidate
	var visit func(*[]ast.Stmt)
	visit = func(list *[]ast.Stmt) {
		for _, stmt := range *list {
			pos := fset.Position(stmt.Pos())
			if pos.Line == op.Line {
				switch s := stmt.(type) {
				case *ast.AssignStmt:
					idx := op.Slot - 1
					if idx < 0 {
						idx = 0
					}
					if idx < len(s.Lhs) && len(s.Rhs) > 0 {
						// A multi-result call occupies one RHS expression; every
						// extracted slot maps back to that same source expression.
						rhsIdx := idx
						if rhsIdx >= len(s.Rhs) {
							rhsIdx = 0
						}
						candidates = append(candidates, candidate{stmt: stmt, list: list, expr: s.Rhs[rhsIdx], column: fset.Position(s.Rhs[rhsIdx].Pos()).Column, end: fset.Position(s.Rhs[rhsIdx].End()).Column})
					}
				case *ast.ReturnStmt:
					idx := op.Slot - 1
					if idx < 0 {
						idx = 0
					}
					if idx < len(s.Results) {
						candidates = append(candidates, candidate{stmt: stmt, list: list, expr: s.Results[idx], column: fset.Position(s.Results[idx].Pos()).Column, end: fset.Position(s.Results[idx].End()).Column})
					}
				case *ast.IfStmt:
					if fset.Position(s.Cond.Pos()).Line == op.Line {
						candidates = append(candidates, candidate{stmt: stmt, list: list, expr: s.Cond, column: fset.Position(s.Cond.Pos()).Column, end: fset.Position(s.Cond.End()).Column})
					}
				}
			}
			ast.Inspect(stmt, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false
				}
				switch child := n.(type) {
				case *ast.BlockStmt:
					visit(&child.List)
					return false
				case *ast.CaseClause:
					visit(&child.Body)
					return false
				case *ast.CommClause:
					visit(&child.Body)
					return false
				}
				return true
			})
		}
	}
	visit(&enclosing.Body.List)
	if op.Kind == "*ssa.Return" {
		var returns []candidate
		for _, c := range candidates {
			if _, ok := c.stmt.(*ast.ReturnStmt); ok && (op.Column == 0 || fset.Position(c.stmt.Pos()).Column == op.Column) {
				returns = append(returns, c)
			}
		}
		candidates = returns
	}
	if len(candidates) == 0 {
		return "", funcName, fmt.Errorf("operation line %d has 0 supported statements", op.Line)
	}
	// Record nested expressions too. SSA instruction positions often point at
	// an operator or call parenthesis inside a source expression; if such a
	// nested expression is the target we must abstain rather than log the
	// enclosing value under its identity.
	roots := append([]candidate(nil), candidates...)
	for _, root := range roots {
		ast.Inspect(root.expr, func(n ast.Node) bool {
			if n == root.expr {
				return true
			}
			if _, ok := n.(ast.Expr); ok {
				candidates = append(candidates, candidate{
					stmt: root.stmt, list: root.list, expr: n.(ast.Expr),
					column: fset.Position(n.Pos()).Column, end: fset.Position(n.End()).Column, nested: true,
				})
			}
			return true
		})
	}
	selected := roots[0]
	if op.Column > 0 && op.Kind != "*ssa.Return" {
		exact, ranged := -1, -1
		for i, c := range candidates {
			if c.column == op.Column {
				exact = i
				break
			}
			if c.column <= op.Column && op.Column <= c.end && ranged < 0 {
				ranged = i
			}
		}
		chosen := exact
		if chosen < 0 {
			chosen = ranged
		}
		if chosen < 0 {
			return "", funcName, fmt.Errorf("column %d does not identify an expression on line %d", op.Column, op.Line)
		}
		if candidates[chosen].nested {
			return "", funcName, fmt.Errorf("column %d identifies a nested expression that cannot be logged without changing evaluation order", op.Column)
		}
		selected = candidates[chosen]
	}
	if len(roots) > 1 && op.Column == 0 {
		return "", funcName, fmt.Errorf("operation line %d has %d ambiguous statements", op.Line, len(roots))
	}

	metadata, _ := json.Marshal(ProbeValueObservation{OperationID: op.ID, File: op.File, Line: op.Line, Function: op.FunctionID, Slot: op.Slot})
	logValue := func(value ast.Expr) ast.Stmt {
		return &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent(runtimeProbeHelper), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconvQuote(string(metadata))}, value}}}
	}
	usedNames := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			usedNames[id.Name] = true
		}
		return true
	})
	tempCounter := 0
	newTemp := func() *ast.Ident {
		for {
			name := fmt.Sprintf("__malenia_probe_v%d", tempCounter)
			tempCounter++
			if !usedNames[name] {
				usedNames[name] = true
				return ast.NewIdent(name)
			}
		}
	}

	var replacement []ast.Stmt
	switch stmt := selected.stmt.(type) {
	case *ast.AssignStmt:
		idx := op.Slot - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(stmt.Lhs) {
			return "", funcName, fmt.Errorf("result slot %d exceeds assignment width", op.Slot)
		}
		replacement = []ast.Stmt{stmt, logValue(stmt.Lhs[idx])}
	case *ast.ReturnStmt:
		if len(stmt.Results) == 0 {
			return "", funcName, fmt.Errorf("return has no results")
		}
		var numericPrelude []ast.Stmt
		if op.NumericComparison {
			numericPrelude = instrumentNumericComparison(stmt, op, newTemp)
		}
		var resultTypes []ast.Expr
		if enclosing.Type.Results == nil {
			return "", funcName, fmt.Errorf("return signature unavailable")
		}
		for _, field := range enclosing.Type.Results.List {
			n := len(field.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				resultTypes = append(resultTypes, field.Type)
			}
		}
		temps := make([]ast.Expr, len(resultTypes))
		assigns := make([]ast.Expr, len(resultTypes))
		var declarations []ast.Stmt
		for i := range resultTypes {
			temp := newTemp()
			temps[i] = temp
			assigns[i] = temp
			declarations = append(declarations, &ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{&ast.ValueSpec{Names: []*ast.Ident{temp}, Type: resultTypes[i]}}}})
		}
		// Capture all results in original order, then log the exact slot and
		// return the temporaries. Evaluation count and order are unchanged.
		capture := &ast.AssignStmt{Tok: token.ASSIGN, Lhs: temps, Rhs: append([]ast.Expr(nil), stmt.Results...)}
		idx := op.Slot - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(temps) {
			return "", funcName, fmt.Errorf("result slot %d exceeds return width", op.Slot)
		}
		stmt.Results = assigns
		log := logValue(temps[idx])
		if op.ErrorReturn {
			if len(resultTypes) < 2 {
				return "", funcName, fmt.Errorf("missing error result slot")
			}
			last, ok := resultTypes[len(resultTypes)-1].(*ast.Ident)
			if !ok || last.Name != "error" {
				return "", funcName, fmt.Errorf("error result type not verified")
			}
			call := log.(*ast.ExprStmt).X.(*ast.CallExpr)
			call.Args = append(call.Args, &ast.BinaryExpr{X: temps[len(temps)-1], Op: token.NEQ, Y: ast.NewIdent("nil")})
		}
		replacement = append(append(numericPrelude, declarations...), capture, log, stmt)
	case *ast.IfStmt:
		if binary, ok := stmt.Cond.(*ast.BinaryExpr); ok && (binary.Op == token.LAND || binary.Op == token.LOR) {
			return "", funcName, fmt.Errorf("short-circuit condition cannot be hoisted without changing behavior")
		}
		temp := newTemp()
		capture := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{temp}, Rhs: []ast.Expr{stmt.Cond}}
		stmt.Cond = temp
		replacement = []ast.Stmt{capture, logValue(temp), stmt}
		if stmt.Init != nil {
			// Preserve the initializer's scope across the condition, body and
			// else branch while keeping it hidden from following statements.
			init := stmt.Init
			stmt.Init = nil
			replacement = []ast.Stmt{&ast.BlockStmt{List: append([]ast.Stmt{init}, replacement...)}}
		}
	default:
		return "", funcName, fmt.Errorf("unsupported statement shape")
	}

	for i, stmt := range *selected.list {
		if stmt == selected.stmt {
			list := *selected.list
			*selected.list = append(append(append([]ast.Stmt(nil), list[:i]...), replacement...), list[i+1:]...)
			break
		}
	}
	var out bytes.Buffer
	if err := format.Node(&out, fset, file); err != nil {
		return "", funcName, err
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return "", funcName, err
	}
	return fmt.Sprintf("instrumented exact operation %s at %s:%d:%d slot %d", op.ID, op.File, op.Line, op.Column, op.Slot), funcName, nil
}

func writeProbeHelper(modifiedFile string) error {
	data, err := os.ReadFile(modifiedFile)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, modifiedFile, data, 0)
	if err != nil {
		return err
	}
	helper := "package " + file.Name.Name + `
import (
 "encoding/json"
 "fmt"
 "os"
 "reflect"
 "runtime"
 "strconv"
 "strings"
 "sync/atomic"
)
var maleniaProbeSequence uint64
func malenia_probe_scalar(value interface{}) (string,bool) {
 v:=reflect.ValueOf(value)
 if !v.IsValid(){return "",false}
 switch v.Kind(){
 case reflect.String:return v.String(),true
 case reflect.Bool:return strconv.FormatBool(v.Bool()),true
 case reflect.Int,reflect.Int8,reflect.Int16,reflect.Int32,reflect.Int64:return strconv.FormatInt(v.Int(),10),true
 case reflect.Uint,reflect.Uint8,reflect.Uint16,reflect.Uint32,reflect.Uint64:return strconv.FormatUint(v.Uint(),10),true
 case reflect.Float32,reflect.Float64:return strconv.FormatFloat(v.Float(),'g',-1,v.Type().Bits()),true
 }
 return "",false
}
func malenia_probe_goroutine() string {
 var data [64]byte
 n:=runtime.Stack(data[:],false)
 fields:=strings.Fields(string(data[:n]))
 if len(fields)<2 || fields[0]!="goroutine" {return ""}
 if _,err:=strconv.ParseUint(fields[1],10,64);err!=nil{return ""}
 return fields[1]
}
func malenia_probe_emit(event map[string]interface{}) {
 event["goroutine"]=malenia_probe_goroutine()
 data,err:=json.Marshal(event)
 if err==nil {fmt.Fprintln(os.Stderr,"MALENIA-RUNTIME-PROBE "+string(data))}
}
func malenia_runtime_probe_log(metadata string, value interface{}, errorPresent ...bool) {
 rendered,ok:=malenia_probe_scalar(value);if !ok{return}
 var event map[string]interface{}
 if json.Unmarshal([]byte(metadata),&event)!=nil{return}
 event["event"]="operation"
 pcs:=make([]uintptr,32)
 count:=runtime.Callers(2,pcs)
 frames:=runtime.CallersFrames(pcs[:count])
 var functions []string
 for { frame,more:=frames.Next();functions=append(functions,frame.Function);if !more{break} }
 event["frames"]=functions
 event["value"]=rendered
 event["sequence"]=atomic.AddUint64(&maleniaProbeSequence,1)
 if len(errorPresent)==1 {event["error_present"]=errorPresent[0]}
 malenia_probe_emit(event)
}
func malenia_probe_assertion_begin(assertion,test string) uint64 {
 id:=atomic.AddUint64(&maleniaProbeSequence,1)
 malenia_probe_emit(map[string]interface{}{"event":"assertion_begin","assertion_id":assertion,"invocation":id,"test":test})
 return id
}
func malenia_probe_assertion_end(id uint64,assertion,test string,actual,expected interface{},predicate ...bool) {
 rendered,ok:=malenia_probe_scalar(actual);if !ok{return}
 expectedValue,ok:=malenia_probe_scalar(expected);if !ok{return}
 event:=map[string]interface{}{"event":"assertion_end","assertion_id":assertion,"invocation":id,"test":test,"value":rendered,"expected":expectedValue}
 if len(predicate)==1 {event["predicate"]=predicate[0]}
 malenia_probe_emit(event)
}
func malenia_probe_numeric_conversion(metadata string,before,after reflect.Value,actual interface{}) {
 integer:=func(v reflect.Value)bool {return v.IsValid() && ((v.Kind()>=reflect.Int && v.Kind()<=reflect.Int64)||(v.Kind()>=reflect.Uint && v.Kind()<=reflect.Uint64))}
 original:=reflect.ValueOf(actual)
 if !integer(before)||!integer(after)||!integer(original)||!before.CanInterface()||!after.CanInterface(){return}
 input,ok:=malenia_probe_scalar(before.Interface());if !ok{return}
 output,ok:=malenia_probe_scalar(after.Interface());if !ok{return}
 actualValue,ok:=malenia_probe_scalar(actual);if !ok{return}
 var event map[string]interface{}
 if json.Unmarshal([]byte(metadata),&event)!=nil{return}
 event["event"]="conversion"
 event["numeric_conversion"]=map[string]interface{}{"input":input,"output":output,"actual":actualValue,"input_type":before.Type().String(),"output_type":after.Type().String(),"actual_type":original.Type().String(),"lossy":input!=output,"matches_actual":after.Type()==original.Type() && output==actualValue}
 malenia_probe_emit(event)
}
`

	// The helper must live in the same directory (and therefore package) as
	// the instrumented source, otherwise the injected calls will not compile.
	path := filepath.Join(filepath.Dir(modifiedFile), "malenia_runtime_probe_helper.go")
	if existing, err := os.ReadFile(path); err == nil {
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, existing, parser.PackageClauseOnly)
		if parseErr == nil && parsed.Name.Name == file.Name.Name {
			if bytes.Equal(existing, []byte(helper)) {
				return nil
			}
			return fmt.Errorf("probe helper source collision")
		}
	}
	if strings.HasSuffix(modifiedFile, "_test.go") {
		path = filepath.Join(filepath.Dir(modifiedFile), "malenia_runtime_probe_helper_test.go")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("probe helper source collision")
	}

	return os.WriteFile(path, []byte(helper), 0o644)
}

func strconvQuote(s string) string {
	return fmt.Sprintf("%q", s)
}

// probeBuildAndRun compiles the instrumented source into one test binary and
// executes that exact binary. The returned digest is therefore the identity of
// the binary which produced the observation, not an unrelated rebuild.
func (d *Diagnoser) probeBuildAndRun(ctx context.Context, dir string, command []string) ([]domain.ExecutionResult, string, []string, error) {
	buildFlags, testFlags, pkg := splitGoTestCommand(command)
	bin := filepath.Join(dir, "malenia-probe.test")
	buildCommand := append([]string{"go", "test", "-c", "-o", bin}, buildFlags...)
	buildCommand = append(buildCommand, pkg)

	buildCtx, buildCancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer buildCancel()
	buildResult, err := d.cfg.Executor.Run(buildCtx, execution.Request{
		Command:       buildCommand,
		Dir:           dir,
		Timeout:       d.cfg.Timeout,
		MaxOutput:     32 << 10,
		WorkspacePath: dir,
		InstanceID:    d.investigationID + "-runtime-probe-build",
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("probe build failed: %w", err)
	}
	if buildResult.ExitCode != 0 || buildResult.TimedOut {
		return nil, "", nil, fmt.Errorf("probe build failed: exit=%d timeout=%t; output: %s", buildResult.ExitCode, buildResult.TimedOut, bound(buildResult.Stdout+"\n"+buildResult.Stderr, MaxEvidenceExcerpt))
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		return nil, "", nil, fmt.Errorf("probe binary unavailable: %w", err)
	}
	sum := sha256.Sum256(data)

	// exec replaces the shell with the built binary, so the process producing
	// the output is the binary whose digest was just computed. The shell is
	// allowlisted only when runtime observation is explicitly enabled.
	execCommand := append([]string{"sh", "-c", `exec "$0" "$@"`, bin}, testFlags...)
	var runs []domain.ExecutionResult
	for run := 0; run < 3 && ctx.Err() == nil; run++ {
		res, err := d.cfg.Executor.Run(ctx, execution.Request{
			Command: execCommand, Dir: filepath.Join(dir, pkg), Timeout: d.cfg.Timeout,
			MaxOutput: 32 << 10, WorkspacePath: dir,
			InstanceID: fmt.Sprintf("%s-runtime-probe-%d", d.investigationID, run+1),
		})
		if err != nil {
			return runs, hex.EncodeToString(sum[:]), execCommand, fmt.Errorf("probe execution failed: %w", err)
		}
		runs = append(runs, res)
	}
	if len(runs) == 0 {
		return nil, hex.EncodeToString(sum[:]), execCommand, fmt.Errorf("probe deadline before execution")
	}
	return runs, hex.EncodeToString(sum[:]), execCommand, nil
}

// splitGoTestCommand separates a `go test` command into flags that affect the
// build, flags understood by the compiled test binary, and the package under
// test. Only the conventional go-test shape is interpreted; anything else is
// left untouched for the build so no experiment is silently redefined.
func splitGoTestCommand(command []string) (buildFlags, testFlags []string, pkg string) {
	pkg = "."
	for i := 1; i < len(command); i++ {
		arg := command[i]
		switch {
		case arg == "-run":
			if i+1 < len(command) {
				testFlags = append(testFlags, "-test.run="+command[i+1])
				i++
			}
		case strings.HasPrefix(arg, "-run="):
			testFlags = append(testFlags, "-test.run="+strings.TrimPrefix(arg, "-run="))
		case arg == "-v" || arg == "-test.v":
			testFlags = append(testFlags, "-test.v")
		case arg == "-count" || strings.HasPrefix(arg, "-count="):
			if arg == "-count" && i+1 < len(command) {
				i++
			}
		case arg == "-timeout":
			if i+1 < len(command) {
				testFlags = append(testFlags, "-test.timeout="+command[i+1])
				i++
			}
		case strings.HasPrefix(arg, "-test."):
			testFlags = append(testFlags, arg)
		case strings.HasPrefix(arg, "-"):
			if arg == "-c" || strings.HasPrefix(arg, "-o") {
				continue
			}
			if needsSeparateValue(arg) && i+1 < len(command) && !strings.Contains(arg, "=") {
				buildFlags = append(buildFlags, arg, command[i+1])
				i++
			} else {
				buildFlags = append(buildFlags, arg)
			}
		default:
			pkg = arg
		}
	}
	return buildFlags, testFlags, pkg
}

func needsSeparateValue(flag string) bool {
	switch flag {
	case "-tags", "-ldflags", "-gcflags", "-asmflags", "-mod", "-overlay", "-toolexec":
		return true
	}
	return false
}

func probeCommand(report *Report, operationFile string) []string {
	base := append([]string(nil), report.Command...)
	if len(base) == 0 {
		return []string{"go", "test", "-count=1", "-v"}
	}
	// Keep the configured command; add deterministic flags only when absent.
	if !hasCommandArg(base, "-count=1") {
		base = append(base, "-count=1")
	}
	if !hasCommandArg(base, "-v") {
		base = append(base, "-v")
	}
	return base
}

// ProbeValueObservation retains source/SSA identity and an event sequence;
// equal text from another operation is never pooled into this observation.
// Test is a stack-derived caller, not proof of the failing subtest/invocation.
type ProbeValueObservation struct {
	Event        string                     `json:"event,omitempty"`
	Goroutine    string                     `json:"goroutine,omitempty"`
	Invocation   uint64                     `json:"invocation,omitempty"`
	AssertionID  string                     `json:"assertion_id,omitempty"`
	Expected     string                     `json:"expected,omitempty"`
	ErrorPresent *bool                      `json:"error_present,omitempty"`
	Frames       []string                   `json:"frames,omitempty"`
	Predicate    *bool                      `json:"predicate,omitempty"`
	Numeric      *NumericConversionEvidence `json:"numeric_conversion,omitempty"`
	OperationID  string                     `json:"operation_id"`
	File         string                     `json:"file"`
	Line         int                        `json:"line"`
	Function     string                     `json:"function"`
	Slot         int                        `json:"slot"`
	Sequence     uint64                     `json:"sequence"`
	Test         string                     `json:"test,omitempty"`
	Value        string                     `json:"value"`
}

func parseOperationValues(output string, op *probeOperation) []ProbeValueObservation {
	var values []ProbeValueObservation
	seen := map[uint64]bool{}
	for _, line := range strings.Split(output, "\n") {
		payload, ok := strings.CutPrefix(line, runtimeProbeMarker+" ")
		if !ok {
			continue
		}
		var v ProbeValueObservation
		if json.Unmarshal([]byte(payload), &v) != nil || v.OperationID != op.ID || v.File != op.File || v.Line != op.Line || v.Function != op.FunctionID || v.Slot != op.Slot || v.Sequence == 0 || seen[v.Sequence] {
			continue
		}
		seen[v.Sequence] = true
		values = append(values, v)
		if len(values) >= 128 {
			break
		}
	}
	return values
}

func probeAssertion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Error Trace:") || strings.Contains(line, "expected:") || strings.Contains(line, "Not equal:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// failureSignature captures the structured expected/actual values of an
// assertion failure. It intentionally ignores addresses, offsets and line
// numbers, which are not stable across a rebuilt binary.
type probeFailureSignature struct {
	expected string
	actual   string
}

var (
	expectedRe = regexp.MustCompile(`(?i)expected\s*:\s*([^\s,\n"']+)`)
	actualRe   = regexp.MustCompile(`(?i)actual\s*:?\s*([^\s,\n"']+)`)
)

// gotWantRe covers assertion formats that report a got/want pair instead of
// testify's expected/actual text. Extraction stays generic: no repository,
// test, or case-specific literals are used.
var gotWantRe = regexp.MustCompile(`(?i)\bgot\s+([^\s,()]+)\s*\([^)]*\),\s*want\s+([^\s,()]+)`)

func extractFailureSignature(text string) (probeFailureSignature, bool) {
	exp := expectedRe.FindStringSubmatch(text)
	act := actualRe.FindStringSubmatch(text)
	if len(exp) >= 2 && len(act) >= 2 {
		return probeFailureSignature{expected: normalizeToken(exp[1]), actual: normalizeToken(act[1])}, true
	}
	if m := gotWantRe.FindStringSubmatch(text); len(m) >= 3 {
		return probeFailureSignature{expected: normalizeToken(m[2]), actual: normalizeToken(m[1])}, true
	}
	return probeFailureSignature{}, false
}

func normalizeToken(token string) string {
	return strings.Trim(strings.TrimSpace(token), "`'\"")
}

// originalFailureSignature extracts the expected/actual pair from the original
// CI evidence only. Without such a pair there is nothing to match against and
// no runtime observation may be promoted.
func originalFailureSignature(report *Report) (probeFailureSignature, bool) {
	var original strings.Builder
	for _, item := range report.Evidence {
		if item.Kind == "ci_log" || item.Kind == "original_ci_log" || item.Kind == "junit" {
			original.WriteString(item.Detail)
			original.WriteByte('\n')
		}
	}
	for _, res := range report.Baseline.Results {
		original.WriteString(res.Stderr)
		original.WriteByte('\n')
		original.WriteString(res.Error)
		original.WriteByte('\n')
	}
	return extractFailureSignature(original.String())
}

// signatureMatches returns true only when the observed output failed on the
// same expected/actual values recorded in the original CI evidence. Matching
// on the mere presence of "expected/actual" text would let unrelated failures
// be promoted to verified mechanisms, so the values themselves are compared.
func signatureMatches(observed string, report *Report) bool {
	observedSig, ok := extractFailureSignature(observed)
	if !ok {
		return false
	}
	originalSig, ok := originalFailureSignature(report)
	return ok && observedSig == originalSig
}

func hasCommandArg(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
