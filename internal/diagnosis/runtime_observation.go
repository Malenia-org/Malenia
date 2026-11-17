package diagnosis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/causent/malenia/internal/domain"
	"github.com/causent/malenia/internal/execution"
	programrepo "github.com/causent/malenia/internal/repository"
)

// RuntimeMechanismObservation records a bounded, explicitly-enabled runtime
// observation of the operation feeding a failing assertion. It is new evidence
// from a controlled execution, never a re-interpretation of the original CI
// address offsets. A rebuilt binary is never correlated with original offsets.
type RuntimeMechanismObservation struct {
	Status         string   `json:"status"` // observed, partial, unavailable, disabled
	Question       string   `json:"question,omitempty"`
	OperationFile  string   `json:"operation_file,omitempty"`
	OperationLine  int      `json:"operation_line,omitempty"`
	OperationName  string   `json:"operation_name,omitempty"`
	ObservedValues []string `json:"observed_values,omitempty"`
	AssertionText  string   `json:"assertion_text,omitempty"`
	// SignatureMatch means the rerun failed on the original expected/actual
	// pair. ValueMatched additionally requires an observed probe value to equal
	// the original failing value. SSADataflowVerified requires an exact SSA
	// value at the probed operation feeding the assertion. All three are needed
	// before a mechanism may be claimed.
	SignatureMatch      bool     `json:"signature_match"`
	ValueMatched        bool     `json:"value_matched"`
	SSADataflowVerified bool     `json:"ssa_dataflow_verified"`
	Command             []string `json:"command,omitempty"`
	ExecutionCommand    []string `json:"execution_command,omitempty"`
	Revision            string   `json:"revision,omitempty"`
	Toolchain           string   `json:"toolchain,omitempty"`
	// BinaryDigest is the SHA-256 of the exact binary produced the observation.
	BinaryDigest  string   `json:"binary_digest,omitempty"`
	TestName      string   `json:"test_name,omitempty"`
	TestFailed    bool     `json:"test_failed"`
	EvidenceIDs   []string `json:"evidence_ids,omitempty"`
	OutputExcerpt string   `json:"output_excerpt,omitempty"`
	Limitations   []string `json:"limitations,omitempty"`
	NextActions   []string `json:"next_actions,omitempty"`
	Provenance    string   `json:"provenance,omitempty"`
}

const (
	runtimeProbeMarker = "MALENIA-RUNTIME-PROBE"
	runtimeProbeHelper = "malenia_runtime_probe_log"
	runtimeProbeBudget = 2 // at most two operations per diagnosis
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
	operation := selectProbeOperation(report, snapshot, candidates)
	if operation == nil {
		return &RuntimeMechanismObservation{
			Status:      "unavailable",
			Limitations: []string{"no specific operation or SSA gap identified for a runtime value probe"},
			NextActions: []string{"inspect the SSA trace gaps and supply the missing operation before probing"},
		}
	}

	probeDir, err := os.MkdirTemp("", "malenia-runtime-probe-")
	if err != nil {
		return &RuntimeMechanismObservation{Status: "unavailable", Limitations: []string{"probe workspace unavailable"}}
	}
	defer os.RemoveAll(probeDir)
	if err := copyProbeWorkspace(workspace, probeDir); err != nil {
		return &RuntimeMechanismObservation{Status: "unavailable", Limitations: []string{"probe workspace copy failed: " + err.Error()}}
	}

	label, funcName, err := injectValueProbe(filepath.Join(probeDir, operation.File), operation.Line)
	if funcName != "" {
		// Use the function actually instrumented, not a causal-chain symbol
		// (which is a file name) so provenance names the real declaration.
		operation.Name = funcName
	}
	if err != nil {
		return &RuntimeMechanismObservation{
			Status:        "partial",
			OperationFile: operation.File, OperationLine: operation.Line,
			Limitations: []string{"operation could not be instrumented: " + err.Error()},
			NextActions: []string{"choose a resolvable single assignment or return at the reported operation"},
		}
	}
	if err := writeProbeHelper(filepath.Join(probeDir, operation.File)); err != nil {
		return &RuntimeMechanismObservation{Status: "partial", Limitations: []string{"probe helper unavailable: " + err.Error()}}
	}

	command := probeCommand(report, operation.File)
	obs := &RuntimeMechanismObservation{
		Status:        "partial",
		Question:      fmt.Sprintf("what value does %s:%d actually produce at the failing assertion?", operation.File, operation.Line),
		OperationFile: operation.File, OperationLine: operation.Line,
		OperationName: operation.Name, Command: command,
		Revision:  firstNonEmpty(report.Environment.HeadCommit, gitOutput(probeDir, "rev-parse", "HEAD")),
		Toolchain: report.Environment.GoCommand,
	}

	// Build one test binary from the instrumented source and execute THAT
	// binary, so the recorded digest identifies the binary which actually
	// produced the observation. No second, unrelated build is performed.
	res, digest, execCommand, buildErr := d.probeBuildAndRun(ctx, probeDir, command)
	if buildErr != nil {
		obs.Limitations = append(obs.Limitations, "probe execution failed: "+buildErr.Error())
		return obs
	}
	obs.BinaryDigest = digest
	obs.ExecutionCommand = execCommand

	combined := res.Stdout + "\n" + res.Stderr
	obs.OutputExcerpt = bound(combined, MaxEvidenceExcerpt)
	values := parseProbeValues(combined)
	obs.ObservedValues = values
	obs.AssertionText = probeAssertion(combined)
	obs.TestFailed = res.ExitCode != 0 || strings.Contains(combined, "--- FAIL") || strings.Contains(combined, "FAIL\t")

	// Gate A: the rerun must fail on the original expected/actual pair.
	observedSig, observedOK := extractFailureSignature(combined)
	originalSig, originalOK := originalFailureSignature(report)
	obs.SignatureMatch = observedOK && originalOK && observedSig == originalSig && len(values) > 0

	// Gate B: an observed probe value must actually equal the original failing
	// value. A matching assertion signature with an unrelated logged value is
	// not evidence that this operation produced the failure.
	obs.ValueMatched = observedOK && originalOK && probeValueMatchesFailure(values, originalSig.actual)

	// Gate C: typed SSA must place an exact value at the probed operation which
	// feeds the failing assertion. A file match or a substring of a function
	// name is not an operation-to-assertion dependency.
	obs.SSADataflowVerified = probeSSADataflowSupports(report, operation)

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

		if obs.SSADataflowVerified {
			mech := EvidenceItem{
				ID:            "E-mechanism-" + digestString(report.InvestigationID + operation.File + operation.Name)[:12],
				Kind:          "failure_mechanism",
				ClaimKind:     "verified_failure_mechanism",
				Basis:         BasisDataflowVerified,
				Source:        operation.File,
				DeclarationID: operation.Name,
				Detail: fmt.Sprintf("SSA dataflow traced an exact value at %s:%d feeding the failed assertion; an isolated runtime probe observed %s, matching the original failing value, establishing the mechanism at %s.",
					operation.File, operation.Line, strings.Join(values, ", "), operation.Name),
				Provenance: obs.Provenance,
				Revision:   obs.Revision,
				Digest:     digestString(combined),
			}
			report.Evidence = append(report.Evidence, mech)
			obs.EvidenceIDs = append(obs.EvidenceIDs, mech.ID)
		} else {
			obs.Limitations = append(obs.Limitations, "no exact SSA value at the probed operation feeds the assertion; the runtime values are an observation, not a verified mechanism")
			obs.NextActions = append(obs.NextActions, "identify the exact SSA value feeding the assertion and probe that operation")
		}
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
	File string
	Line int
	Name string
}

// mechanismEvidenceVerified requires the report to carry the contract-aligned
// evidence item produced only when exact SSA dataflow and a matching runtime
// value were both observed. The assessment cannot manufacture this evidence.
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
			value.Line == op.Line && value.Function == op.Name {
			return true
		}
	}
	return false
}

// probeValueMatchesFailure reports whether one of the values observed by the
// probe equals the original failing value. Values are compared as whole tokens
// so an embedded struct field or a different number cannot satisfy the gate.
func probeValueMatchesFailure(values []string, failingValue string) bool {
	if failingValue == "" {
		return false
	}
	for _, value := range values {
		for _, token := range strings.Fields(value) {
			if normalizeToken(token) == failingValue {
				return true
			}
		}
	}
	return false
}

// selectProbeOperation chooses a concrete operation from the established
// causal chain or SSA gaps. It keys on structure (assertion line + verified
// call edges), never on repository, test, or case names.
func selectProbeOperation(report *Report, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) *probeOperation {
	if report.CausalChain == nil || len(report.CausalChain.Steps) == 0 {
		return nil
	}
	// Prefer the deepest implementation branch with a real source file.
	for i := len(report.CausalChain.Steps) - 1; i >= 0; i-- {
		step := report.CausalChain.Steps[i]
		if step.Kind != "implementation_branch" || step.File == "" || strings.HasSuffix(step.File, "_test.go") {
			continue
		}
		node := nodeForFileLine(snapshot.Nodes, step.File, step.Line)
		name := step.Symbol
		if node != nil && node.Name != "" {
			name = node.Name
		}
		if name == "" {
			continue
		}
		return &probeOperation{File: step.File, Line: step.Line, Name: name}
	}
	// Fallback to a non-test called_function step.
	for _, step := range report.CausalChain.Steps {
		if step.Kind == "called_function" && step.File != "" && !strings.HasSuffix(step.File, "_test.go") {
			return &probeOperation{File: step.File, Line: step.Line, Name: step.Symbol}
		}
	}
	return nil
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
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != src && (name == ".git" || name == ".malenia" || name == "node_modules" || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if files >= maxFiles || bytesWritten >= maxBytes {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".mod" && ext != ".sum" && ext != ".work" && ext != ".yml" && ext != ".yaml" {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || bytesWritten+int64(len(data)) > maxBytes {
			return nil
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

// injectValueProbe rewrites a Go source file in the disposable copy, adding a
// bounded log immediately after an assignment/return that calls a function at
// the target line. It returns a short label and the name of the enclosing
// function actually instrumented (used for evidence provenance).
func injectValueProbe(path string, line int) (label, funcName string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, 0)
	if err != nil {
		return "", "", err
	}
	// The reported line is often a function declaration or branch line, with
	// the value-producing statement a few lines later. Restrict instrumentation
	// to the function enclosing the reported line, then pick the first
	// call-assignment or return within it. This stays generic and bounded.
	var enclosing *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		start, end := fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line
		if line >= start && line <= end {
			enclosing = fn
			break
		}
	}
	if enclosing == nil {
		return "", "", fmt.Errorf("no function encloses line %d", line)
	}
	funcName = enclosing.Name.Name
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

	// Collect candidates before mutating the statement list.
	type injection struct {
		stmt   ast.Stmt
		before []ast.Stmt
		after  ast.Stmt
		line   int
	}
	var injections []injection
	resultCount := 0
	if enclosing.Type.Results != nil {
		for _, field := range enclosing.Type.Results.List {
			if len(field.Names) == 0 {
				resultCount++
			} else {
				resultCount += len(field.Names)
			}
		}
	}
	for _, stmt := range enclosing.Body.List {
		if len(injections) >= runtimeProbeBudget {
			break
		}
		label := &ast.BasicLit{Kind: token.STRING, Value: strconvQuote(fmt.Sprintf("%s:%d", filepath.Base(path), fset.Position(stmt.Pos()).Line))}
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if len(s.Rhs) == 0 {
				continue
			}
			if _, ok := s.Rhs[0].(*ast.CallExpr); !ok {
				continue
			}
			args := []ast.Expr{label}
			for _, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					args = append(args, id)
				}
			}
			if len(args) < 2 {
				continue
			}
			injections = append(injections, injection{
				stmt:  stmt,
				after: &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent(runtimeProbeHelper), Args: args}},
				line:  fset.Position(stmt.Pos()).Line,
			})
		case *ast.ReturnStmt:
			if len(s.Results) == 0 || resultCount == 0 {
				continue
			}
			// A lone multi-valued call may back several result slots.
			if len(s.Results) != resultCount && !(len(s.Results) == 1 && resultCount > 1) {
				continue
			}
			if len(s.Results) == 1 && resultCount > 1 {
				if _, ok := s.Results[0].(*ast.CallExpr); !ok {
					continue
				}
			}
			temps := make([]ast.Expr, resultCount)
			for i := range temps {
				temps[i] = newTemp()
			}
			// Evaluate the original expressions exactly once into temporaries,
			// log them, then return the temporaries. Logging the expressions in
			// place would evaluate side-effecting calls a second time.
			capture := &ast.AssignStmt{Tok: token.DEFINE, Lhs: temps, Rhs: append([]ast.Expr(nil), s.Results...)}
			logArgs := append([]ast.Expr{label}, temps...)
			logCall := &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent(runtimeProbeHelper), Args: logArgs}}
			s.Results = append([]ast.Expr(nil), temps...)
			injections = append(injections, injection{
				stmt:   stmt,
				before: []ast.Stmt{capture, logCall},
				line:   fset.Position(stmt.Pos()).Line,
			})
		}
	}
	if len(injections) == 0 {
		return "", funcName, fmt.Errorf("no call-assignment or return in the function enclosing line %d", line)
	}
	// Insert in reverse so earlier statement pointers stay valid.
	for i := len(injections) - 1; i >= 0; i-- {
		if len(injections[i].before) > 0 {
			insertBefore(file, injections[i].stmt, injections[i].before...)
		}
		if injections[i].after != nil {
			insertAfter(file, injections[i].stmt, injections[i].after)
		}
	}
	label = fmt.Sprintf("instrumented %d value(s) in function enclosing line %d (first at %d)", len(injections), line, injections[0].line)
	var out bytes.Buffer
	if err := format.Node(&out, fset, file); err != nil {
		return "", funcName, err
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return "", funcName, err
	}
	return label, funcName, nil
}

func insertBefore(file *ast.File, target ast.Stmt, stmts ...ast.Stmt) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for i, s := range fn.Body.List {
			if s == target {
				oldLen := len(fn.Body.List)
				fn.Body.List = append(fn.Body.List, make([]ast.Stmt, len(stmts))...)
				copy(fn.Body.List[i+len(stmts):], fn.Body.List[i:oldLen])
				copy(fn.Body.List[i:i+len(stmts)], stmts)
				return
			}
		}
	}
}

func insertAfter(file *ast.File, target ast.Stmt, stmt ast.Stmt) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for i, s := range fn.Body.List {
			if s == target {
				fn.Body.List = append(fn.Body.List, nil)
				copy(fn.Body.List[i+2:], fn.Body.List[i+1:])
				fn.Body.List[i+1] = stmt
				return
			}
		}
	}
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
	helper := fmt.Sprintf("package %s\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc %s(label string, values ...any) {\n\tout := \"%s \" + label\n\tfor _, v := range values {\n\t\tout += \" \" + fmt.Sprint(v)\n\t}\n\tfmt.Fprintln(os.Stderr, out)\n}\n", file.Name.Name, runtimeProbeHelper, runtimeProbeMarker)
	// The helper must live in the same directory (and therefore package) as
	// the instrumented source, otherwise the injected calls will not compile.
	path := filepath.Join(filepath.Dir(modifiedFile), "malenia_runtime_probe_helper.go")
	// Avoid overwriting an existing helper with a different package.
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), "package "+file.Name.Name) {
			return fmt.Errorf("probe helper package collision")
		}
	}
	return os.WriteFile(path, []byte(helper), 0o644)
}

func strconvQuote(s string) string {
	return fmt.Sprintf("%q", s)
}

// probeBuildAndRun compiles the instrumented source into one test binary and
// executes that exact binary. The returned digest is therefore the identity of
// the binary which produced the observation, not an unrelated rebuild.
func (d *Diagnoser) probeBuildAndRun(ctx context.Context, dir string, command []string) (domain.ExecutionResult, string, []string, error) {
	buildFlags, testFlags, pkg := splitGoTestCommand(command)
	bin := filepath.Join(dir, "malenia-probe.test")
	buildCommand := append([]string{"go", "test", "-c", "-o", bin}, buildFlags...)
	buildCommand = append(buildCommand, pkg)

	buildCtx, buildCancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer buildCancel()
	if _, err := d.cfg.Executor.Run(buildCtx, execution.Request{
		Command:       buildCommand,
		Dir:           dir,
		Timeout:       d.cfg.Timeout,
		MaxOutput:     32 << 10,
		WorkspacePath: dir,
		InstanceID:    d.investigationID + "-runtime-probe-build",
	}); err != nil {
		return domain.ExecutionResult{}, "", nil, fmt.Errorf("probe build failed: %w", err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		return domain.ExecutionResult{}, "", nil, fmt.Errorf("probe binary unavailable: %w", err)
	}
	sum := sha256.Sum256(data)

	// exec replaces the shell with the built binary, so the process producing
	// the output is the binary whose digest was just computed. The shell is
	// allowlisted only when runtime observation is explicitly enabled.
	execCommand := append([]string{"sh", "-c", `exec "$0" "$@"`, bin}, testFlags...)
	runCtx, runCancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer runCancel()
	res, err := d.cfg.Executor.Run(runCtx, execution.Request{
		Command:       execCommand,
		Dir:           filepath.Join(dir, filepath.Dir(pkg)),
		Timeout:       d.cfg.Timeout,
		MaxOutput:     32 << 10,
		WorkspacePath: dir,
		InstanceID:    d.investigationID + "-runtime-probe",
	})
	if err != nil {
		return domain.ExecutionResult{}, "", nil, fmt.Errorf("probe execution failed: %w", err)
	}
	return res, hex.EncodeToString(sum[:]), execCommand, nil
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

var probeValueLine = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(runtimeProbeMarker) + ` ([^:\n]+:\d+)(.*)$`)

func parseProbeValues(output string) []string {
	var values []string
	seen := map[string]bool{}
	for _, m := range probeValueLine.FindAllStringSubmatch(output, -1) {
		v := strings.TrimSpace(m[2])
		if v != "" && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	sort.Strings(values)
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

func extractFailureSignature(text string) (probeFailureSignature, bool) {
	exp := expectedRe.FindStringSubmatch(text)
	act := actualRe.FindStringSubmatch(text)
	if len(exp) < 2 || len(act) < 2 {
		return probeFailureSignature{}, false
	}
	return probeFailureSignature{expected: normalizeToken(exp[1]), actual: normalizeToken(act[1])}, true
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
