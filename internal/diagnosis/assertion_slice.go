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
	"sort"
	"strconv"
	"strings"

	"github.com/caeser-org/ceaser/internal/domain"
	programrepo "github.com/caeser-org/ceaser/internal/repository"
)

type ObservedSourceOperation struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Expression string `json:"expression"`
	EvidenceID string `json:"evidence_id"`
}

// collectSourceOperations preserves expressions inside the selected function,
// rather than treating its first return as the cause of the assertion failure.
func collectSourceOperations(report *Report, workspace string, node domain.ProgramNode) []ObservedSourceOperation {
	data, err := os.ReadFile(filepath.Join(workspace, node.FilePath))
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, node.FilePath, data, 0)
	if err != nil {
		return nil
	}
	var result []ObservedSourceOperation
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != node.Name || fset.Position(fn.Pos()).Line != node.StartLine {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if len(result) >= 6 {
				return false
			}
			var expression ast.Node
			switch value := n.(type) {
			case *ast.ReturnStmt:
				expression = value
			case *ast.AssignStmt:
				expression = value
			case *ast.ExprStmt:
				if _, ok := value.X.(*ast.CallExpr); ok {
					expression = value.X
				}
			}
			if expression == nil {
				return true
			}
			var code bytes.Buffer
			if format.Node(&code, fset, expression) != nil {
				return false
			}
			line := fset.Position(expression.Pos()).Line
			_, evidence := sourceStep(report, workspace, "source_operation", node.FilePath, line, "")
			if evidence != nil {
				evidence.ClaimKind = "SourceOperationObservation"
				evidence.Detail += "\nParsed operation: " + code.String()
				evidence.Digest = digestString(evidence.Detail)
				evidence.ID = "E-operation-" + digestString(report.InvestigationID + node.ID + evidence.Detail)[:20]
				report.Evidence = append(report.Evidence, *evidence)
				result = append(result, ObservedSourceOperation{node.FilePath, line, code.String(), evidence.ID})
			}
			return false
		})
	}
	return result
}

// assertionOperations computes a bounded syntactic dependency slice. Its
// results are candidates, not data-flow proof: aliases, branches, interfaces,
// and asynchronous writes can make a syntactic dependency imprecise.
func assertionOperations(report *Report, workspace string, test domain.ProgramNode, snapshot programrepo.Snapshot) []domain.ProgramNode {
	data, err := os.ReadFile(filepath.Join(workspace, test.FilePath))
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, test.FilePath, data, 0)
	if err != nil {
		return nil
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == test.Name {
			fn = f
			break
		}
	}
	if fn == nil || fn.Body == nil {
		return nil
	}
	pattern := regexp.MustCompile(regexp.QuoteMeta(filepath.Base(test.FilePath)) + `:(\d+):`)
	var failedLine int
	for _, match := range pattern.FindAllStringSubmatch(failureText(report.Baseline, report.Evidence), -1) {
		line, _ := strconv.Atoi(match[1])
		if line >= fset.Position(fn.Pos()).Line && line <= fset.Position(fn.End()).Line {
			failedLine = line
			break
		}
	}
	if failedLine == 0 {
		return nil
	}
	if snapshot.GoSSA != nil {
		trace := snapshot.GoSSA.TraceAssertion(test.FilePath, failedLine)
		for _, value := range trace.Values {
			if len(trace.EvidenceIDs) >= 24 {
				break
			}
			if value.Line <= 0 || value.File == "" || value.Kind == "constant" {
				continue
			}
			_, evidence := sourceStep(report, workspace, "ssa_dependency", value.File, value.Line, "")
			if evidence == nil {
				continue
			}
			evidence.ClaimKind = "StaticValueDependencyObservation"
			evidence.Detail += "\nSSA instruction: " + value.Text + "\nFunction: " + value.Function + "\nDependency path only; branch feasibility and aliases are unverified."
			evidence.Digest = digestString(evidence.Detail)
			evidence.ID = "E-ssa-" + digestString(report.InvestigationID + value.ID + evidence.Detail)[:20]
			duplicate := false
			for _, id := range trace.EvidenceIDs {
				if id == evidence.ID {
					duplicate = true
					break
				}
			}
			if !duplicate {
				report.Evidence = append(report.Evidence, *evidence)
				trace.EvidenceIDs = append(trace.EvidenceIDs, evidence.ID)
			}
		}
		report.SSAAssertionTrace = &trace
		// Only function identities with actual operand paths in this trace
		// can influence ordering. Missing coverage leaves the syntactic path.
	}
	dependencies := map[string]bool{}
	addNames := func(node ast.Node) bool {
		changed := false
		ast.Inspect(node, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok && ident.Name != "_" && ident.Name != "t" && !dependencies[ident.Name] {
				dependencies[ident.Name] = true
				changed = true
			}
			return true
		})
		return changed
	}
	touches := func(node ast.Node) bool {
		found := false
		ast.Inspect(node, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok && dependencies[ident.Name] {
				found = true
			}
			return !found
		})
		return found
	}
	// Seed from the failing assertion's arguments and enclosing predicates.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if fset.Position(n.Pos()).Line > failedLine || fset.Position(n.End()).Line < failedLine {
			return false
		}
		switch value := n.(type) {
		case *ast.IfStmt:
			addNames(value.Cond)
		case *ast.ExprStmt:
			// Enclosing t.Run calls contain whole closures, not just the assertion.
			if fset.Position(value.Pos()).Line == failedLine {
				addNames(value.X)
			}
		}
		return true
	})
	if len(dependencies) == 0 {
		return nil
	}
	var assignments []*ast.AssignStmt
	var calls []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if fset.Position(n.Pos()).Line > failedLine {
			return false
		}
		switch value := n.(type) {
		case *ast.AssignStmt:
			assignments = append(assignments, value)
		case *ast.CallExpr:
			calls = append(calls, value)
		}
		return true
	})
	// Propagate over assignments in reverse order. The bidirectional object
	// association retains receivers connected to observed buffers/hooks, but
	// is explicitly a may-dependency rather than an alias proof.
	for pass := 0; pass < 8; pass++ {
		changed := false
		for i := len(assignments) - 1; i >= 0; i-- {
			if touches(assignments[i]) {
				changed = addNames(assignments[i]) || changed
			}
		}
		if !changed {
			break
		}
	}
	nodes := map[string]domain.ProgramNode{}
	for _, node := range snapshot.Nodes {
		nodes[node.ID] = node
	}
	type operation struct {
		node domain.ProgramNode
		line int
	}
	var operations []operation
	seen := map[string]bool{}
	for i := len(calls) - 1; i >= 0; i-- {
		call := calls[i]
		line := fset.Position(call.Pos()).Line
		if line > failedLine || !touches(call) {
			continue
		}
		for _, edge := range snapshot.Relationships {
			if edge.SourceID != test.ID || edge.Metadata["resolution"] != "go_types" || !strings.Contains(edge.Metadata["call_lines"], fmt.Sprintf(",%d,", line)) {
				continue
			}
			node, ok := nodes[edge.TargetID]
			if !ok || strings.HasSuffix(node.FilePath, "_test.go") || seen[node.ID] {
				continue
			}
			seen[node.ID] = true
			operations = append(operations, operation{node, line})
		}
	}
	sort.SliceStable(operations, func(i, j int) bool { return operations[i].line > operations[j].line })
	var result []domain.ProgramNode
	for _, op := range operations {
		if len(result) >= 8 {
			break
		}
		result = append(result, op.node)
	}
	if trace := report.SSAAssertionTrace; trace != nil && len(trace.Values) > 0 {
		byNode := map[string]domain.ProgramNode{}
		for _, node := range snapshot.Nodes {
			byNode[node.ID] = node
		}
		var precise []domain.ProgramNode
		used := map[string]bool{}
		for _, value := range trace.Values {
			fn, ok := snapshot.GoSSA.Functions[value.Function]
			if !ok || fn.NodeID == "" || fn.NodeID == test.ID || used[fn.NodeID] {
				continue
			}
			node := byNode[fn.NodeID]
			if node.ID == "" || strings.HasSuffix(node.FilePath, "_test.go") {
				continue
			}
			precise = append(precise, node)
			used[node.ID] = true
		}
		for _, node := range result {
			if !used[node.ID] {
				precise = append(precise, node)
				used[node.ID] = true
			}
		}
		if len(precise) > 8 {
			precise = precise[:8]
		}
		result = precise
	}
	return result
}
