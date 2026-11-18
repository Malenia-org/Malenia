package diagnosis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Malenia-org/ceaser/internal/domain"
	programrepo "github.com/Malenia-org/ceaser/internal/repository"
)

func probeValueKey(value programrepo.SSAValue) string {
	if value.Kind == "*ssa.Return" && value.TraceSlot != nil {
		return fmt.Sprintf("%s:slot:%d", value.ID, *value.TraceSlot)
	}
	return value.ID
}

func mixProbeOperations(ssa, source []*probeOperation) []*probeOperation {
	var out []*probeOperation
	seen := map[string]bool{}
	for i := 0; i < max(len(ssa), len(source)); i++ {
		for _, group := range [][]*probeOperation{ssa, source} {
			if i >= len(group) {
				continue
			}
			op := group[i]
			key := fmt.Sprintf("%s:%d:%d:%d", op.File, op.Line, op.Column, op.Slot)
			if !seen[key] {
				seen[key] = true
				out = append(out, op)
			}
		}
	}
	return out
}

// Prefer an exact repository path, then the longest unique suffix shared
// with indexed source. An ambiguous basename is left unresolved.
func probeSourcePath(raw string, snapshot programrepo.Snapshot) string {
	raw = path.Clean(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	files := map[string]bool{}
	for _, node := range snapshot.Nodes {
		if node.FilePath != "" {
			files[node.FilePath] = true
		}
	}
	if files[raw] {
		return raw
	}
	best, winner, ambiguous := 0, "", false
	parts := strings.Split(raw, "/")
	for file := range files {
		local := strings.Split(file, "/")
		score := 0
		for i, j := len(parts)-1, len(local)-1; i >= 0 && j >= 0 && parts[i] == local[j]; i, j = i-1, j-1 {
			score++
		}
		if score > best {
			best, winner, ambiguous = score, file, false
		} else if score > 0 && score == best {
			ambiguous = true
		}
	}
	if best == 0 || ambiguous {
		return ""
	}
	return winner
}

func probeCandidateRank(candidates []domain.CandidateLocation, file string, line int) int {
	for i, candidate := range candidates {
		loc := candidate.Location
		if loc.FilePath == file && line >= loc.StartLine && line <= max(loc.StartLine, loc.EndLine) {
			return i
		}
	}
	return len(candidates)
}

// selectSourceProbeOperation chooses returns covered by an existing profile
// or inside an implementation function identified by a stack frame.
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
	stackLines := map[string][]int{}
	add := func(file string, line int) {
		file = probeSourcePath(file, snapshot)
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
		file := probeSourcePath(frame.File, snapshot)
		if file != "" {
			stackLines[file] = append(stackLines[file], frame.Line)
		}
	}
	for _, item := range report.Evidence {
		if item.Kind != "coverage_report" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(item.Detail), "mode:") {
			blocks, err := parseGoCoverage(item.Detail)
			if err == nil {
				for _, block := range blocks {
					if block.count > 0 {
						// Bound expansion for malformed or very broad spans.
						for line := block.start; line <= block.end && line < block.start+2000; line++ {
							add(block.file, line)
						}
					}
				}
			}
			continue
		}
		if item.ArtifactFormat != "lcov" {
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
			anchored := false
			for _, line := range stackLines[file] {
				if line >= fset.Position(fn.Pos()).Line && line <= fset.Position(fn.End()).Line {
					anchored = true
					break
				}
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
				if (!anchored && !lines[file][pos.Line]) || nodeForFileLine(snapshot.Nodes, file, pos.Line) == nil {
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
