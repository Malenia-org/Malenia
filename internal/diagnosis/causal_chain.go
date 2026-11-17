package diagnosis

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/causent/malenia/internal/domain"
	programrepo "github.com/causent/malenia/internal/repository"
)

// DeterministicCausalStep is one evidence-cited link in a source-grounded
// explanation of a deterministic (non-race) Go failure. Each step is a
// reported observation backed by source evidence, never a causal conclusion on
// its own. The chain as a whole may be labeled established only when every
// link is verified by retrieved source.
type DeterministicCausalStep struct {
	Kind       string `json:"kind"`
	Statement  string `json:"statement"`
	Symbol     string `json:"symbol,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	EvidenceID string `json:"evidence_id,omitempty"`
}

// DeterministicCausalChain is the generic, evidence-cited path from a failing
// Go test through the functions it calls into the implementation branch that
// produces the observed value.
type DeterministicCausalChain struct {
	Status      string                    `json:"status"`
	Steps       []DeterministicCausalStep `json:"steps"`
	EvidenceIDs []string                  `json:"evidence_ids,omitempty"`
	Limitations []string                  `json:"limitations,omitempty"`
	Operations  []ObservedSourceOperation `json:"operations,omitempty"`
}

// buildDeterministicCausalChain constructs a source-grounded chain for any
// deterministic Go failure. It is generic: it keys on program-graph structure
// (AST call edges, function symbols, retrieved source), never on repository,
// test, or symbol names. It writes the chain into the report and appends the
// source evidence it collects; it never claims a root cause itself.
func buildDeterministicCausalChain(report *Report, workspace string, snapshot programrepo.Snapshot, candidates []domain.CandidateLocation) {
	if report == nil || workspace == "" {
		return
	}
	if len(report.FailureIncidents) == 0 || report.FailureIncidents[0].Category == "go_data_race" {
		return
	}
	failingTests := map[string]bool{}
	for _, episode := range report.FailureEpisodes {
		if name := testNameFromIdentifier(episode.TestIdentifier); name != "" {
			failingTests[name] = true
		}
	}
	for _, incident := range report.FailureIncidents {
		for _, test := range incident.AffectedTests {
			if name := testNameFromIdentifier(test); name != "" {
				failingTests[name] = true
			}
		}
	}
	if len(failingTests) == 0 {
		return
	}

	nodesByID := map[string]domain.ProgramNode{}
	for _, node := range snapshot.Nodes {
		nodesByID[node.ID] = node
	}
	verifiedCallees := map[string][]domain.ProgramNode{}
	typedCallees := map[string][]domain.ProgramNode{}
	for _, rel := range snapshot.Relationships {
		if rel.Type != "CALLS" || rel.Metadata["provenance"] != "verified_static_call" {
			continue
		}
		target, ok := nodesByID[rel.TargetID]
		if !ok || (target.Type != "Function" && target.Type != "Method") {
			continue
		}
		verifiedCallees[rel.SourceID] = append(verifiedCallees[rel.SourceID], target)
		if rel.Metadata["resolution"] == "go_types" {
			typedCallees[rel.SourceID] = append(typedCallees[rel.SourceID], target)
		}
	}

	chain := &DeterministicCausalChain{Status: "unresolved"}
	for _, candidate := range candidates {
		nodeID := strings.TrimPrefix(candidate.ID, "candidate:")
		testNode, ok := nodesByID[nodeID]
		if !ok || testNode.Type != "Test" || !failingTests[testNode.Name] {
			continue
		}

		// Prefer a go/packages type-resolved callee of the failing test itself.
		// This is a verified edge (the test really calls that symbol), so no
		// test-name heuristic is needed. Fall back to the AST name-based path
		// only when type information was unavailable.
		var impl *domain.ProgramNode
		operations := assertionOperations(report, workspace, testNode, snapshot)
		if len(operations) > 0 {
			impl = &operations[0]
		}
		for i := range typedCallees[testNode.ID] {
			if impl != nil {
				break
			}
			c := &typedCallees[testNode.ID][i]
			if c.Type == "Test" || strings.HasSuffix(c.FilePath, "_test.go") {
				continue
			}
			impl = c
			break
		}
		if impl == nil {
			// Only graph edges belonging to this exact failing test are eligible.
			for i := range verifiedCallees[testNode.ID] {
				callee := &verifiedCallees[testNode.ID][i]
				if !strings.HasSuffix(callee.FilePath, "_test.go") {
					impl = callee
					break
				}
			}
		}
		if impl == nil {
			chain.Limitations = append(chain.Limitations, "no indexed implementation symbol matched a call in the failing test")
			break
		}

		testStep, testEvidence := sourceStep(report, workspace, "failing_test", testNode.FilePath, testNode.StartLine,
			fmt.Sprintf("source analysis selected %s as an assertion dependency or reachable-call candidate; path feasibility and causality remain unverified", impl.Name))
		if testEvidence != nil {
			report.Evidence = append(report.Evidence, *testEvidence)
			chain.EvidenceIDs = appendUniqueString(chain.EvidenceIDs, testEvidence.ID)
		}
		implStep, implEvidence := sourceStep(report, workspace, "called_function", impl.FilePath, impl.StartLine,
			fmt.Sprintf("%s is an indexed function on the selected assertion dependency or call path", impl.Identity))
		if implEvidence != nil {
			report.Evidence = append(report.Evidence, *implEvidence)
			chain.EvidenceIDs = appendUniqueString(chain.EvidenceIDs, implEvidence.ID)
		}
		if testEvidence != nil && implEvidence != nil {
			chain.Steps = append(chain.Steps, testStep, implStep)
		}
		for _, operation := range operations {
			chain.Operations = append(chain.Operations, collectSourceOperations(report, workspace, operation)...)
			step, evidence := sourceStep(report, workspace, "assertion_dependency", operation.FilePath, operation.StartLine,
				fmt.Sprintf("%s is a type-resolved call associated with values used by the failed assertion; syntactic may-dependency, not verified causality", operation.Identity))
			if evidence != nil {
				report.Evidence = append(report.Evidence, *evidence)
				chain.EvidenceIDs = appendUniqueString(chain.EvidenceIDs, evidence.ID)
				chain.Steps = append(chain.Steps, step)
			}
		}

		// Follow verified calls. A return statement alone cannot identify the
		// operation responsible for an observed value.
		callees := verifiedCallees[impl.ID]
		for _, operation := range operations {
			if operation.ID != impl.ID {
				callees = append(callees, verifiedCallees[operation.ID]...)
			}
		}
		if len(callees) == 0 {
			chain.Limitations = append(chain.Limitations, "the implementation has no AST-verified callee; its own source is the deepest verified step")
		}
		seenCallees := map[string]bool{}
		for _, callee := range callees {
			if seenCallees[callee.ID] {
				continue
			}
			seenCallees[callee.ID] = true
			statement := fmt.Sprintf("%s is called by %s through an AST-verified call edge", callee.Identity, impl.Identity)
			calleeStep, calleeEvidence := sourceStep(report, workspace, "implementation_branch", callee.FilePath, callee.StartLine, statement)
			if calleeEvidence != nil {
				report.Evidence = append(report.Evidence, *calleeEvidence)
				chain.EvidenceIDs = appendUniqueString(chain.EvidenceIDs, calleeEvidence.ID)
				chain.Steps = append(chain.Steps, calleeStep)
			}
		}

		if len(chain.Steps) >= 2 && len(chain.EvidenceIDs) >= 2 {
			chain.Status = "localized"
			chain.Limitations = append(chain.Limitations, "call reachability does not establish data flow to the failed assertion or a violated invariant")
		} else if len(chain.Steps) > 0 {
			chain.Status = "partial"
		}
		break
	}
	report.CausalChain = chain
}

// testNameFromIdentifier tolerates subtests ("TestFoo/1e1000") and Go package
// decorations.
func testNameFromIdentifier(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if idx := strings.Index(id, "/"); idx > 0 {
		id = id[:idx]
	}
	if idx := strings.LastIndex(id, "."); idx >= 0 && idx < len(id)-1 {
		id = id[idx+1:]
	}
	return id
}

// calledSymbolsFromSource parses a Go test file and returns the identifiers of
// functions/methods it calls, in source order, de-duplicated.
func calledSymbolsFromSource(workspace, relPath string) []string {
	if relPath == "" || strings.HasSuffix(relPath, "/") {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(relPath)))
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, data, 0)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		return true
	})
	return names
}

// valueBranchLine finds a return statement in the named Go function — the
// generic shape of a value-producing branch — and returns its line.
func valueBranchLine(workspace, relPath, funcName string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(relPath)))
	if err != nil {
		return 0, false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, data, 0)
	if err != nil {
		return 0, false
	}
	found := 0
	ast.Inspect(file, func(node ast.Node) bool {
		if found > 0 {
			return false
		}
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if found > 0 {
				return false
			}
			if ret, ok := n.(*ast.ReturnStmt); ok {
				found = fset.Position(ret.Pos()).Line
				return false
			}
			return true
		})
		return false
	})
	return found, found > 0
}

func sourceStep(report *Report, workspace, kind, file string, line int, statement string) (DeterministicCausalStep, *EvidenceItem) {
	detail := sourceExcerpt(workspace, file, line)
	if detail == "" {
		return DeterministicCausalStep{}, nil
	}
	step := DeterministicCausalStep{Kind: kind, Statement: statement, File: file, Line: line}
	if file != "" {
		if idx := strings.LastIndex(file, "/"); idx >= 0 {
			step.Symbol = file[idx+1:]
		}
	}
	sum := sha256.Sum256([]byte(report.InvestigationID + "\x00" + kind + "\x00" + file + "\x00" + fmt.Sprint(line) + "\x00" + detail))
	id := "E-causal-" + hex.EncodeToString(sum[:10])
	step.EvidenceID = id
	ev := &EvidenceItem{
		ID: id, Kind: "source_excerpt", Source: file, Digest: hex.EncodeToString(sum[:]),
		Detail:    detail,
		ClaimKind: "source-backed causal step", Basis: "SourceVerified",
		Provenance: "deterministic source inspection of checked-out revision",
		Revision:   report.Environment.HeadCommit,
	}
	return step, ev
}

func sourceExcerpt(workspace, file string, line int) string {
	if file == "" || filepath.IsAbs(file) || strings.HasPrefix(file, "..") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(file)))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	start, end := line-2, line+4
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "checked-out source %s\n", file)
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d| %s\n", i, lines[i-1])
	}
	return b.String()
}

// sortedCausalStepKinds keeps causal step order stable in reports.
func sortedCausalStepKinds(steps []DeterministicCausalStep) []string {
	kinds := make([]string, 0, len(steps))
	for _, step := range steps {
		kinds = append(kinds, step.Kind)
	}
	sort.Strings(kinds)
	return kinds
}
