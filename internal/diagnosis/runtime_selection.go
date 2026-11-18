package diagnosis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/causent/malenia/internal/domain"
	programrepo "github.com/causent/malenia/internal/repository"
)

func probeCandidateRank(candidates []domain.CandidateLocation, file string, line int) int {
	for i, candidate := range candidates {
		loc := candidate.Location
		if loc.FilePath == file && line >= loc.StartLine && line <= max(loc.StartLine, loc.EndLine) {
			return i
		}
	}
	return len(candidates)
}

// selectSourceProbeOperation only chooses explicit return expressions at a
// location supported by a matching stack frame or an existing coverage report.
// The operation has a source identity and cannot satisfy the SSA proof gate.
func selectSourceProbeOperation(report *Report, workspace string, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) *probeOperation {
	operations := sourceProbeOperations(report, workspace, snapshot, candidates)
	if len(operations) == 0 {
		return nil
	}
	return operations[0]
}

func sourceProbeOperations(report *Report, workspace string, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) []*probeOperation {
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil
	}
	lines := map[string]map[int]bool{}
	add := func(file string, line int) {
		if file == "" || line <= 0 {
			return
		}
		if lines[file] == nil {
			lines[file] = map[int]bool{}
		}
		lines[file][line] = true
	}
	for _, frame := range architectureFrames(report.Baseline, report.FailureEpisodes, report.Evidence) {
		add(frame.File, frame.Line)
	}
	for _, item := range report.Evidence {
		if item.Kind != "coverage_report" || item.ArtifactFormat != "lcov" {
			continue
		}
		var file string
		for _, record := range strings.Split(item.Detail, "\n") {
			switch {
			case strings.HasPrefix(record, "SF:"):
				file = filepath.ToSlash(strings.TrimPrefix(record, "SF:"))
			case strings.HasPrefix(record, "DA:"):
				fields := strings.Split(strings.TrimPrefix(record, "DA:"), ",")
				if len(fields) >= 2 {
					line, _ := strconv.Atoi(fields[0])
					hits, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
					if hits > 0 {
						add(file, line)
					}
				}
			case record == "end_of_record":
				file = ""
			}
		}
	}
	var operations []*probeOperation
	files := make(map[string]bool)
	for _, candidate := range candidates {
		file := candidate.Location.FilePath
		if len(lines[file]) == 0 || files[file] || !filepath.IsLocal(file) ||
			filepath.Ext(file) != ".go" || strings.HasSuffix(file, "_test.go") {
			continue
		}
		files[file] = true
		path, err := filepath.EvalSymlinks(filepath.Join(root, file))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		stat, err := os.Stat(path)
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<20 {
			continue
		}
		fset := token.NewFileSet()
		source, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range source.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if len(operations) >= 5 {
					return false
				}
				if _, closure := n.(*ast.FuncLit); closure {
					return false
				}
				ret, ok := n.(*ast.ReturnStmt)
				if !ok || len(ret.Results) != 1 {
					return true
				}
				pos := fset.Position(ret.Pos())
				if !lines[file][pos.Line] || nodeForFileLine(snapshot.Nodes, file, pos.Line) == nil {
					return false
				}
				id := fmt.Sprintf("source:%s:%d:%d", file, pos.Line, pos.Column)
				operations = append(operations, &probeOperation{File: file, Line: pos.Line, Column: pos.Column,
					Name: fn.Name.Name, ID: id, FunctionID: id, Slot: 1, Kind: "*ssa.Return"})
				return false
			})
			if len(operations) >= 5 {
				return operations
			}
		}
	}
	return operations
}
