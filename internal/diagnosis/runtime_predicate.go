package diagnosis

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	programrepo "github.com/causent/malenia/internal/repository"
)

// Native predicate support uses the tested value, not Errorf's diagnostic
// arguments. Initially admit only an adjacent scalar call assignment followed
// by `if actual != expected { t.Errorf(...) }` with one unconditional Errorf.
func planPredicateProbe(report *Report, snapshot programrepo.Snapshot, workspace string, op *probeOperation) *assertionProbePlan {
	g := snapshot.GoSSA
	if g == nil || report.SSAAssertionTrace == nil || op.Kind != "*ssa.Return" || op.Slot != 1 {
		return nil
	}
	anchor, ok := g.Values[report.SSAAssertionTrace.AnchorID]
	if !ok || anchor.Callee != "(*testing.common).Errorf" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(workspace, anchor.File))
	if err != nil {
		return nil
	}
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, anchor.File, data, 0)
	if err != nil {
		return nil
	}
	var actual *ast.CallExpr
	var condition *ast.IfStmt
	ast.Inspect(file, func(n ast.Node) bool {
		b, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, s := range b.List {
			branch, ok := s.(*ast.IfStmt)
			if !ok || i == 0 || branch.Init != nil || branch.Else != nil || len(branch.Body.List) != 1 {
				continue
			}
			stmt, ok := branch.Body.List[0].(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := stmt.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			pos := fs.Position(call.Lparen)
			if pos.Line != anchor.Line || pos.Column != anchor.Column {
				continue
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if _, ok := selector.X.(*ast.Ident); !ok {
				continue
			}
			cmp, ok := branch.Cond.(*ast.BinaryExpr)
			if !ok || cmp.Op != token.NEQ {
				continue
			}
			name, ok := cmp.X.(*ast.Ident)
			if !ok {
				continue
			}
			if !stablePredicateValue(cmp.Y) {
				continue
			}
			assignment, ok := b.List[i-1].(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
				continue
			}
			lhs, ok := assignment.Lhs[0].(*ast.Ident)
			if !ok || lhs.Name != name.Name {
				continue
			}
			producer, ok := assignment.Rhs[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			actual = producer
			condition = branch
		}
		return true
	})
	if actual == nil {
		return nil
	}
	pos := fs.Position(actual.Lparen)
	actualID := ""
	for _, v := range g.Values {
		if v.Function == anchor.Function && v.File == anchor.File && v.Line == pos.Line && v.Column == pos.Column && v.Kind == "*ssa.Call" && v.Type == "bool" {
			if actualID != "" {
				return nil
			}
			actualID = v.ID
		}
	}
	if actualID == "" {
		return nil
	}
	dependent := false
	for _, id := range anchor.Conditions {
		v := g.Values[id]
		if v.Kind == "*ssa.BinOp" && v.Line == fs.Position(condition.Pos()).Line && len(v.Operands) == 2 && v.Operands[0] == actualID {
			dependent = true
		}
	}
	if !dependent {
		return nil
	}
	path := transparentReturnPath(g, op, actualID)
	if len(path) == 0 {
		return nil
	}
	functions := []string{runtimeSSAFunction(op.FunctionID)}
	for _, id := range path[1:] {
		if v := g.Values[id]; v.Kind == "*ssa.Call" {
			functions = append(functions, runtimeSSAFunction(v.Function))
		}
	}
	for _, name := range functions {
		if name == "" {
			return nil
		}
	}
	return &assertionProbePlan{ID: anchor.ID, File: anchor.File, Line: anchor.Line, Column: anchor.Column, ActualCall: actualID, Path: path, Functions: functions, NativePredicate: true, ProducerLine: pos.Line, ProducerColumn: pos.Column}
}

func stablePredicateValue(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name != "_"
	case *ast.BasicLit:
		return true
	case *ast.SelectorExpr:
		return stablePredicateValue(v.X)
	}
	return false
}

func injectPredicateProbe(path string, plan *assertionProbePlan) error {
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
			s := fmt.Sprintf("__malenia_%s_%d", base, i)
			if !names[s] {
				names[s] = true
				return ast.NewIdent(s)
			}
		}
	}
	var target *ast.BlockStmt
	index := -1
	var branch *ast.IfStmt
	var assignment *ast.AssignStmt
	var tester ast.Expr
	ast.Inspect(file, func(n ast.Node) bool {
		b, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, s := range b.List {
			candidate, ok := s.(*ast.IfStmt)
			if !ok || i == 0 || len(candidate.Body.List) != 1 {
				continue
			}
			stmt, ok := candidate.Body.List[0].(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := stmt.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			pos := fs.Position(call.Lparen)
			if pos.Line == plan.Line && pos.Column == plan.Column {
				a, ok := b.List[i-1].(*ast.AssignStmt)
				if !ok {
					continue
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				target = b
				index = i
				branch = candidate
				assignment = a
				tester = sel.X
			}
		}
		return true
	})
	if target == nil || len(assignment.Lhs) != 1 {
		return fmt.Errorf("predicate's adjacent producer no longer matches")
	}
	if len(assignment.Rhs) != 1 {
		return fmt.Errorf("predicate producer changed")
	}
	producer, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok {
		return fmt.Errorf("predicate producer is not a call")
	}
	producerPos := fs.Position(producer.Lparen)
	if producerPos.Line != plan.ProducerLine || producerPos.Column != plan.ProducerColumn {
		return fmt.Errorf("predicate producer location changed")
	}
	cmp, ok := branch.Cond.(*ast.BinaryExpr)
	if !ok || cmp.Op != token.NEQ {
		return fmt.Errorf("unsupported predicate")
	}
	literal := func(s string) ast.Expr { return &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(s)} }
	testName := func() ast.Expr { return &ast.CallExpr{Fun: &ast.SelectorExpr{X: tester, Sel: ast.NewIdent("Name")}} }
	span, expected, predicate := fresh("assertion"), fresh("expected"), fresh("predicate")
	begin := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{span}, Rhs: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("malenia_probe_assertion_begin"), Args: []ast.Expr{literal(plan.ID), testName()}}}}
	expectedCapture := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{expected}, Rhs: []ast.Expr{cmp.Y}}
	cmp.Y = expected
	predicateCapture := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{predicate}, Rhs: []ast.Expr{cmp}}
	end := &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("malenia_probe_assertion_end"), Args: []ast.Expr{span, literal(plan.ID), testName(), assignment.Lhs[0], expected, predicate}}}
	branch.Cond = predicate
	replacement := []ast.Stmt{begin, assignment, expectedCapture, predicateCapture, end, branch}
	tail := append([]ast.Stmt(nil), target.List[index+1:]...)
	target.List = append(append(target.List[:index-1], replacement...), tail...)
	var out bytes.Buffer
	if err := format.Node(&out, fs, file); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0600)
}

func nativeFailureMessages(output, test, file string, line int) []string {
	pattern := regexp.MustCompile(`^(?:.*[/\\])?` + regexp.QuoteMeta(filepath.Base(file)) + `:(\d+):\s*(.*)$`)
	current := ""
	var messages []string
	for _, row := range strings.Split(output, "\n") {
		text := strings.TrimSpace(row)
		if strings.HasPrefix(text, "=== RUN") || strings.HasPrefix(text, "=== NAME") {
			f := strings.Fields(text)
			if len(f) == 3 {
				current = f[2]
			}
		}
		if strings.HasPrefix(text, "--- FAIL: ") {
			f := strings.Fields(strings.TrimPrefix(text, "--- FAIL: "))
			if len(f) > 0 {
				current = f[0]
			}
		}
		if current != test {
			continue
		}
		match := pattern.FindStringSubmatch(text)
		if len(match) != 3 {
			continue
		}
		n, _ := strconv.Atoi(match[1])
		if line > 0 && n != line {
			continue
		}
		if match[2] != "" {
			messages = append(messages, match[2])
		}
	}
	return messages
}

func samePredicateFailure(original, output string, plan *assertionProbePlan, test string) bool {
	before := nativeFailureMessages(original, test, plan.File, plan.Line)
	after := nativeFailureMessages(output, test, plan.File, 0)
	for _, a := range before {
		for _, b := range after {
			if a == b {
				return true
			}
		}
	}
	return false
}
