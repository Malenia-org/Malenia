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
	"strings"

	programrepo "github.com/Malenia-org/ceaser/internal/repository"
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
	cast, input             ast.Expr
}

// numericCastSyntaxFor recognizes `return T(expr)` where expr is a stable
// identifier. The cast is the producer itself, so observing the expression
// before and the converted result after preserves one evaluation.
func numericCastSyntaxFor(stmt *ast.ReturnStmt) *numericComparisonSyntax {
	if len(stmt.Results) != 1 {
		return nil
	}
	cast, ok := stmt.Results[0].(*ast.CallExpr)
	if !ok || len(cast.Args) != 1 {
		return nil
	}
	selector, ok := cast.Fun.(*ast.Ident)
	if !ok {
		return nil
	}
	if !numericCastName(selector.Name) {
		return nil
	}
	input, ok := cast.Args[0].(*ast.Ident)
	if !ok || input.Name == "_" {
		return nil
	}
	return &numericComparisonSyntax{cast: cast, input: input}
}

func numericCastName(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return true
	}
	return false
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
				if s := numericComparisonSyntaxFor(ret); s != nil {
					syntax = s
				} else {
					syntax = numericCastSyntaxFor(ret)
				}
			}
		}
		return true
	})
	if syntax == nil {
		return false
	}
	if syntax.cast != nil {
		// The SSA instruction at the selected return must be the conversion of
		// the same source expression. Do not bind by line and column alone.
		for _, v := range g.Values {
			if v.Function != op.FunctionID || v.File != op.File || v.Line != op.Line || v.Kind != "*ssa.Convert" {
				continue
			}
			if strings.HasPrefix(v.Text, "convert ") && strings.Contains(v.Text, " <- ") {
				return true
			}
		}
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
	if s := numericCastSyntaxFor(stmt); s != nil {
		before, after, actual := fresh(), fresh(), fresh()
		captureBefore := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{before}, Rhs: []ast.Expr{s.input}}
		captureAfter := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{after}, Rhs: []ast.Expr{s.cast}}
		captureActual := &ast.AssignStmt{Tok: token.DEFINE, Lhs: []ast.Expr{actual}, Rhs: []ast.Expr{stmt.Results[0]}}
		metadata, _ := json.Marshal(ProbeValueObservation{OperationID: op.ID, File: op.File, Line: op.Line, Function: op.FunctionID, Slot: op.Slot})
		log := &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("ceaser_probe_numeric_conversion"), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(string(metadata))}, before, after, actual}}}
		return []ast.Stmt{captureBefore, captureAfter, captureActual, log}
	}
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
	log := &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("ceaser_probe_numeric_conversion"), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(string(metadata))}, before, after, syntax.actual}}}
	return []ast.Stmt{captureBefore, captureAfter, log}
}
