package diagnosis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	programrepo "github.com/causent/malenia/internal/repository"
)

// AssertionCorrelation proves a narrow synchronous, scalar return path. It
// does not establish PR attribution, arbitrary alias flow or concurrency order.
type AssertionCorrelation struct {
	Verified          bool     `json:"verified"`
	AssertionID       string   `json:"assertion_id"`
	Test              string   `json:"test"`
	Invocation        uint64   `json:"invocation"`
	Goroutine         string   `json:"goroutine"`
	OperationSequence uint64   `json:"operation_sequence"`
	ReturnPath        []string `json:"return_path"`
}

type assertionProbePlan struct {
	ID, File, Expected, ActualCall string
	Line, Column                   int
	Path                           []string
}

// transparentReturnPath admits only direct scalar returns (or an exact tuple
// extract) in the SAME basic block as the producing call. Loads, assignments
// through memory, conversions, later branches and deferred mutation abstain.
func transparentReturnPath(g *programrepo.GoSSA, op *probeOperation, actual string) []string {
	if g == nil || op.Kind != "*ssa.Return" || op.Slot < 1 {
		return nil
	}
	for _, id := range g.Functions[op.FunctionID].Instructions {
		if kind := g.Values[id].Kind; kind == "*ssa.Defer" || kind == "*ssa.Go" {
			return nil
		}
	}
	type step struct {
		function string
		slot     int
		path     []string
	}
	queue := []step{{op.FunctionID, op.Slot - 1, []string{op.ID}}}
	seen := map[string]bool{}
	block := func(id string) string {
		at := strings.LastIndex(id, ":")
		if at < 0 {
			return ""
		}
		return id[:at]
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		key := fmt.Sprintf("%s:%d", cur.function, cur.slot)
		if seen[key] || len(cur.path) > 18 {
			continue
		}
		seen[key] = true
		for _, call := range g.Values {
			if call.Kind != "*ssa.Call" || call.Callee != cur.function {
				continue
			}
			path := append(append([]string(nil), cur.path...), call.ID)
			if call.ID == actual && cur.slot == 0 {
				return path
			}
			caller, ok := g.Functions[call.Function]
			if !ok {
				continue
			}
			unsafe := false
			for _, id := range caller.Instructions {
				k := g.Values[id].Kind
				if k == "*ssa.Go" || k == "*ssa.Defer" {
					unsafe = true
				}
			}
			if unsafe {
				continue
			}
			for _, retID := range caller.ReturnInstructions {
				ret := g.Values[retID]
				if block(ret.ID) != block(call.ID) {
					continue
				}
				for slot, valueID := range ret.Operands {
					value := g.Values[valueID]
					matches := valueID == call.ID && cur.slot == 0 && !strings.HasPrefix(call.Type, "(")
					if value.Kind == "*ssa.Extract" && len(value.Operands) == 1 && value.Operands[0] == call.ID && value.TupleIndex == cur.slot {
						matches = true
					}
					if matches {
						queue = append(queue, step{call.Function, slot, append(append([]string(nil), path...), retID)})
					}
				}
			}
		}
	}
	return nil
}

func constantProbeArgument(g *programrepo.GoSSA, id string, want string) bool {
	for n := 0; n < 8; n++ {
		v, ok := g.Values[id]
		if !ok {
			return false
		}
		if v.Kind == "constant" {
			at := strings.LastIndex(v.Text, ":")
			if at < 0 {
				return false
			}
			value := v.Text[:at]
			if value == want {
				return true
			}
			if decoded, err := strconv.Unquote(value); err == nil {
				return decoded == want
			}
			return false
		}
		if len(v.Operands) != 1 || (v.Kind != "*ssa.MakeInterface" && v.Kind != "*ssa.ChangeType") {
			return false
		}
		id = v.Operands[0]
	}
	return false
}

func planAssertionProbe(report *Report, snapshot programrepo.Snapshot, workspace string, op *probeOperation) *assertionProbePlan {
	trace, g := report.SSAAssertionTrace, snapshot.GoSSA
	if trace == nil || g == nil || !op.ErrorReturn || op.Slot != 1 {
		return nil
	}
	anchor, ok := g.Values[trace.AnchorID]
	if !ok {
		return nil
	}
	// This is an assertion-library contract, not a repository/test-name rule.
	// Other assertion APIs remain observations until their argument contracts
	// and invocation boundaries have equally strong support.
	if anchor.Callee != "github.com/stretchr/testify/assert.Equal" && anchor.Callee != "github.com/stretchr/testify/require.Equal" {
		return nil
	}
	sig, ok := originalFailureSignature(report)
	if !ok || len(anchor.Arguments) < 3 || !constantProbeArgument(g, anchor.Arguments[1], sig.expected) {
		return nil
	}
	testingArg := g.Values[anchor.Arguments[0]]
	if testingArg.Kind == "*ssa.MakeInterface" && len(testingArg.Operands) == 1 {
		testingArg = g.Values[testingArg.Operands[0]]
	}
	if testingArg.Type != "*testing.T" {
		return nil
	}
	source, err := os.ReadFile(filepath.Join(workspace, anchor.File))
	if err != nil {
		return nil
	}
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, anchor.File, source, 0)
	if err != nil {
		return nil
	}
	var call *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pos := fs.Position(c.Lparen)
		if pos.Line == anchor.Line && pos.Column == anchor.Column {
			call = c
		}
		return true
	})
	if call == nil || len(call.Args) < 3 {
		return nil
	}
	if _, ok := call.Args[0].(*ast.Ident); !ok {
		return nil
	}
	actual, ok := call.Args[2].(*ast.CallExpr)
	if !ok {
		return nil
	}
	pos := fs.Position(actual.Lparen)
	var actualID string
	for _, v := range g.Values {
		if v.File == anchor.File && v.Function == anchor.Function && v.Line == pos.Line && v.Column == pos.Column && v.Kind == "*ssa.Call" {
			if actualID != "" {
				return nil
			}
			actualID = v.ID
		}
	}
	path := transparentReturnPath(g, op, actualID)
	if len(path) == 0 {
		return nil
	}
	return &assertionProbePlan{ID: anchor.ID, File: anchor.File, Expected: sig.expected, ActualCall: actualID, Line: anchor.Line, Column: anchor.Column, Path: path}
}

func injectAssertionProbe(path string, plan *assertionProbePlan) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, path, data, parser.ParseComments)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			names[id.Name] = true
		}
		return true
	})
	fresh := func(base string) *ast.Ident {
		for i := 0; ; i++ {
			name := fmt.Sprintf("__malenia_%s_%d", base, i)
			if !names[name] {
				names[name] = true
				return ast.NewIdent(name)
			}
		}
	}
	var block *ast.BlockStmt
	var index int
	var call *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		b, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range b.List {
			expr, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			c, ok := expr.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			pos := fs.Position(c.Lparen)
			if pos.Line == plan.Line && pos.Column == plan.Column {
				block = b
				index = i
				call = c
			}
		}
		return true
	})
	if call == nil || len(call.Args) < 3 {
		return fmt.Errorf("assertion is not a standalone supported call")
	}
	t, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return fmt.Errorf("testing argument is not a stable identifier")
	}
	// Expected must be constant (checked in SSA) and the function must be a
	// package selector, so moving actual after Begin does not reorder effects.
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return fmt.Errorf("unsupported assertion function expression")
	}
	if _, ok := selector.X.(*ast.Ident); !ok {
		return fmt.Errorf("assertion receiver may have side effects")
	}
	literal := func(s string) ast.Expr { return &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(s)} }
	testName := func() ast.Expr { return &ast.CallExpr{Fun: &ast.SelectorExpr{X: t, Sel: ast.NewIdent("Name")}} }
	span, actual := fresh("assertion"), fresh("actual")
	begin := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{span}, Rhs: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("malenia_probe_assertion_begin"), Args: []ast.Expr{literal(plan.ID), testName()}}}}
	capture := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{actual}, Rhs: []ast.Expr{call.Args[2]}}
	end := &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("malenia_probe_assertion_end"), Args: []ast.Expr{span, literal(plan.ID), testName(), actual, literal(plan.Expected)}}}
	call.Args[2] = actual
	replacement := []ast.Stmt{begin, capture, end, block.List[index]}
	tail := append([]ast.Stmt(nil), block.List[index+1:]...)
	block.List = append(append(block.List[:index], replacement...), tail...)
	var out bytes.Buffer
	if err := format.Node(&out, fs, file); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0600)
}

func signatureForNamedTest(output, test, file string, lineNumber int) (probeFailureSignature, bool) {
	offset := 0
	for _, line := range strings.SplitAfter(output, "\n") {
		end := offset + len(line)
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Test:" && fields[1] == test {
			prefix := output[:offset]
			if start := strings.LastIndex(prefix, "Error Trace:"); start >= 0 {
				prefix = prefix[start:]
			}
			if file != "" && !strings.Contains(prefix, fmt.Sprintf("%s:%d", filepath.Base(file), lineNumber)) {
				offset = end
				continue
			}
			if sig, ok := extractFailureSignature(prefix); ok {
				return sig, true
			}
		}
		offset = end
	}
	return probeFailureSignature{}, false
}

func correlateAssertion(output, original string, op *probeOperation, plan *assertionProbePlan) *AssertionCorrelation {
	if plan == nil {
		return nil
	}
	type span struct {
		begin      ProbeValueObservation
		operations []ProbeValueObservation
	}
	active := map[string]*span{}
	for _, line := range strings.Split(output, "\n") {
		payload, ok := strings.CutPrefix(line, runtimeProbeMarker+" ")
		if !ok {
			continue
		}
		var v ProbeValueObservation
		if json.Unmarshal([]byte(payload), &v) != nil || v.Goroutine == "" {
			continue
		}
		switch v.Event {
		case "assertion_begin":
			if active[v.Goroutine] != nil {
				active[v.Goroutine] = &span{}
				continue
			}
			if v.AssertionID == plan.ID && v.Invocation > 0 && v.Test != "" {
				active[v.Goroutine] = &span{begin: v}
			}
		case "operation":
			if s := active[v.Goroutine]; s != nil && v.OperationID == op.ID && v.File == op.File && v.Line == op.Line && v.Function == op.FunctionID && v.Slot == op.Slot {
				s.operations = append(s.operations, v)
			}
		case "assertion_end":
			s := active[v.Goroutine]
			delete(active, v.Goroutine)
			if s == nil || s.begin.Invocation != v.Invocation || s.begin.AssertionID != plan.ID || v.AssertionID != plan.ID || s.begin.Test != v.Test || len(s.operations) != 1 {
				continue
			}
			observed := s.operations[0]
			if observed.Sequence == 0 || observed.Value != v.Value || observed.ErrorPresent == nil || !*observed.ErrorPresent || v.Expected != plan.Expected {
				continue
			}
			old, oldOK := signatureForNamedTest(original, v.Test, plan.File, plan.Line)
			now, nowOK := signatureForNamedTest(output, v.Test, "", 0)
			if !oldOK || !nowOK || old != now || old.expected != v.Expected || old.actual != v.Value {
				continue
			}
			return &AssertionCorrelation{Verified: true, AssertionID: plan.ID, Test: v.Test, Invocation: v.Invocation, Goroutine: v.Goroutine, OperationSequence: observed.Sequence, ReturnPath: plan.Path}
		}
	}
	return nil
}
