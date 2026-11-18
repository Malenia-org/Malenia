package diagnosis

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"os"
	"path/filepath"
	"strconv"

	programrepo "github.com/causent/malenia/internal/repository"
)

// NumericConversionEvidence is a primitive projection, not a formatted object
// or a speculative interpretation of reflection's internal representation.
type NumericConversionEvidence struct {
	Input         string `json:"input"`
	Output        string `json:"output"`
	Actual        string `json:"actual"`
	InputType     string `json:"input_type"`
	OutputType    string `json:"output_type"`
	ActualType    string `json:"actual_type"`
	Lossy         bool   `json:"lossy"`
	MatchesActual bool   `json:"matches_actual"`
}

func integerConversionObserved(n *NumericConversionEvidence) bool {
	if n.InputType == "" || n.OutputType == "" || n.OutputType != n.ActualType {
		return false
	}
	for _, value := range []string{n.Input, n.Output, n.Actual} {
		if _, ok := new(big.Int).SetString(value, 10); !ok {
			return false
		}
	}
	return n.Lossy == (n.Input != n.Output) && n.MatchesActual == (n.Output == n.Actual)
}

func verifiedIntegerConversion(n *NumericConversionEvidence) bool {
	if n == nil || !n.Lossy || !n.MatchesActual || n.Input == n.Output || n.Output != n.Actual || n.InputType == "" || n.OutputType == "" || n.OutputType != n.ActualType {
		return false
	}
	a, ok := new(big.Int).SetString(n.Input, 10)
	if !ok {
		return false
	}
	b, ok := new(big.Int).SetString(n.Output, 10)
	return ok && a.Cmp(b) != 0
}

type numericComparisonSyntax struct {
	convert, unbox, compare *ast.CallExpr
	receiver, actual        *ast.Ident
}

func numericComparisonSyntaxFor(stmt *ast.ReturnStmt) *numericComparisonSyntax {
	if len(stmt.Results) != 1 {
		return nil
	}
	compare, ok := stmt.Results[0].(*ast.CallExpr)
	if !ok || len(compare.Args) != 2 {
		return nil
	}
	unbox, ok := compare.Args[0].(*ast.CallExpr)
	if !ok || len(unbox.Args) != 0 {
		return nil
	}
	selector, ok := unbox.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Interface" {
		return nil
	}
	convert, ok := selector.X.(*ast.CallExpr)
	if !ok || len(convert.Args) != 1 {
		return nil
	}
	conversion, ok := convert.Fun.(*ast.SelectorExpr)
	if !ok || conversion.Sel.Name != "Convert" {
		return nil
	}
	receiver, ok := conversion.X.(*ast.Ident)
	if !ok {
		return nil
	}
	if _, ok := convert.Args[0].(*ast.Ident); !ok {
		return nil
	}
	actual, ok := compare.Args[1].(*ast.Ident)
	if !ok {
		return nil
	}
	return &numericComparisonSyntax{convert: convert, unbox: unbox, compare: compare, receiver: receiver, actual: actual}
}

func numericComparisonSupported(g *programrepo.GoSSA, workspace string, op *probeOperation) bool {
	if g == nil || op.Kind != "*ssa.Return" || op.Slot != 1 {
		return false
	}
	data, err := os.ReadFile(filepath.Join(workspace, op.File))
	if err != nil {
		return false
	}
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, op.File, data, 0)
	if err != nil {
		return false
	}
	var syntax *numericComparisonSyntax
	ast.Inspect(file, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if ok {
			p := fs.Position(ret.Pos())
			if p.Line == op.Line && p.Column == op.Column {
				syntax = numericComparisonSyntaxFor(ret)
			}
		}
		return true
	})
	if syntax == nil {
		return false
	}
	for call, target := range map[*ast.CallExpr]string{syntax.compare: "reflect.DeepEqual", syntax.convert: "(reflect.Value).Convert", syntax.unbox: "(reflect.Value).Interface"} {
		p := fs.Position(call.Lparen)
		found := false
		for _, v := range g.Values {
			if v.Function == op.FunctionID && v.File == op.File && v.Line == p.Line && v.Column == p.Column && v.Callee == target {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func instrumentNumericComparison(stmt *ast.ReturnStmt, op *probeOperation, fresh func() *ast.Ident) []ast.Stmt {
	syntax := numericComparisonSyntaxFor(stmt)
	if syntax == nil {
		return nil
	}
	before, after := fresh(), fresh()
	captureBefore := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{before}, Rhs: []ast.Expr{syntax.receiver}}
	syntax.convert.Fun.(*ast.SelectorExpr).X = before
	captureAfter := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{after}, Rhs: []ast.Expr{syntax.convert}}
	syntax.unbox.Fun.(*ast.SelectorExpr).X = after
	metadata, _ := json.Marshal(ProbeValueObservation{OperationID: op.ID, File: op.File, Line: op.Line, Function: op.FunctionID, Slot: op.Slot})
	log := &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("malenia_probe_numeric_conversion"), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(string(metadata))}, before, after, syntax.actual}}}
	return []ast.Stmt{captureBefore, captureAfter, log}
}
