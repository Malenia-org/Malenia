package repository

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"time"

	"github.com/causent/malenia/internal/domain"
	"golang.org/x/tools/go/packages"
)

// buildTypedGoCalls adds type-resolved CALLS edges for Go sources. Unlike the
// name/reference heuristics in buildReferences, go/packages resolves the actual
// object a selector or identifier denotes — including cross-package receivers
// such as k.Float64() where k is an imported type. This makes the
// "test -> implementation" link in a deterministic causal chain a verified
// edge rather than a name-guess.
//
// The loader is bounded by a timeout; failures degrade to no extra edges (the
// existing AST and heuristic edges remain). It never changes nodes or claims
// causality.
func (a Analyzer) buildTypedGoCalls(ctx context.Context, root string, s *Snapshot) {
	if root == "" || len(s.Nodes) == 0 {
		return
	}
	loadCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Context: loadCtx,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Dir:   root,
		Fset:  fset,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil || len(pkgs) == 0 {
		return
	}

	byPos := map[string][]domain.ProgramNode{}
	byName := map[string][]domain.ProgramNode{}
	for _, node := range s.Nodes {
		if node.Type == "Function" || node.Type == "Method" || node.Type == "Test" {
			byName[node.Name] = append(byName[node.Name], node)
			byPos[declKey(node.FilePath, node.StartLine)] = append(byPos[declKey(node.FilePath, node.StartLine)], node)
		}
	}

	existing := map[string]bool{}
	for _, rel := range s.Relationships {
		existing[rel.ID] = true
	}

	relPath := func(pos token.Pos) (string, int) {
		p := fset.Position(pos)
		if !p.IsValid() || p.Filename == "" {
			return "", 0
		}
		rel, err := filepath.Rel(root, p.Filename)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", 0
		}
		return filepath.ToSlash(rel), p.Line
	}

	addEdgeFor := func(source, target domain.ProgramNode, obj types.Object, callLine int) {
		if target.ID == source.ID {
			return
		}
		id := edgeID(source.ID, "CALLS", target.ID)
		if existing[id] {
			for i := range s.Relationships {
				if s.Relationships[i].ID == id {
					metadata := s.Relationships[i].Metadata
					metadata["resolution"] = "go_types"
					metadata["call_lines"] += fmt.Sprintf(",%d,", callLine)
				}
			}
			return
		}
		existing[id] = true
		s.Relationships = append(s.Relationships, domain.Relationship{
			ID:         id,
			SourceType: source.Type,
			SourceID:   source.ID,
			TargetType: target.Type,
			TargetID:   target.ID,
			Type:       "CALLS",
			RevisionID: s.RevisionID,
			Metadata: map[string]string{
				"provenance":   "verified_static_call",
				"verification": "go/packages type-resolved call to " + obj.Name(),
				"resolution":   "go_types",
				"call_lines":   fmt.Sprintf(",%d,", callLine),
			},
		})
	}

	for _, pkg := range pkgs {
		if loadCtx.Err() != nil {
			return
		}
		info := pkg.TypesInfo
		if info == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				filePath, line := relPath(fn.Pos())
				var source *domain.ProgramNode
				// Prefer the node declared at this exact file+line.
				if candidates := byPos[declKey(filePath, line)]; len(candidates) > 0 {
					for i := range candidates {
						if candidates[i].Name == fn.Name.Name {
							source = &candidates[i]
							break
						}
					}
					if source == nil {
						source = &candidates[0]
					}
				}
				if source == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					obj := resolveCallObject(call, info)
					if obj == nil {
						return true
					}
					target := nodeForTypedObject(obj, fset, root, byPos, byName)
					if target != nil {
						addEdgeFor(*source, *target, obj, fset.Position(call.Pos()).Line)
					}
					return true
				})
			}
		}
	}
	a.buildGoSSA(loadCtx, root, pkgs, s)
}

// resolveCallObject returns the type-checked object a call denotes.
func resolveCallObject(call *ast.CallExpr, info *types.Info) types.Object {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return info.Uses[fun]
	case *ast.SelectorExpr:
		if sel := info.Selections[fun]; sel != nil {
			return sel.Obj()
		}
		return info.Uses[fun.Sel]
	default:
		return nil
	}
}

// nodeForTypedObject maps a type-resolved object to an existing graph node by
// its declaration file + line. Objects outside the indexed files remain
// unresolved, including external dependencies with colliding method names.
func nodeForTypedObject(obj types.Object, fset *token.FileSet, root string, byPos map[string][]domain.ProgramNode, byName map[string][]domain.ProgramNode) *domain.ProgramNode {
	pos := fset.Position(obj.Pos())
	if pos.IsValid() && pos.Filename != "" {
		if rel, err := filepath.Rel(root, pos.Filename); err == nil && !strings.HasPrefix(rel, "..") {
			if candidates := byPos[declKey(filepath.ToSlash(rel), pos.Line)]; len(candidates) > 0 {
				for i := range candidates {
					if candidates[i].Name == obj.Name() {
						return &candidates[i]
					}
				}
			}
		}
	}
	// External or unindexed declarations cannot be mapped by spelling alone.
	return nil
}

func declKey(file string, line int) string {
	return fmt.Sprintf("%s:%d", filepath.ToSlash(file), line)
}
