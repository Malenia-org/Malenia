package repository

import (
	"context"
	"fmt"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// GoSSA is revision-local static evidence. No program is executed to build it.
// It intentionally does not claim path feasibility or complete alias analysis.
type GoSSA struct {
	Functions   map[string]SSAFunction `json:"functions"`
	Values      map[string]SSAValue    `json:"values"`
	Limitations []string               `json:"limitations,omitempty"`
}

type SSAFunction struct {
	ID               string     `json:"id"`
	NodeID           string     `json:"node_id,omitempty"`
	File             string     `json:"file"`
	StartLine        int        `json:"start_line"`
	EndLine          int        `json:"end_line"`
	Params           []string   `json:"params,omitempty"`
	Returns          [][]string `json:"returns,omitempty"`
	ReturnConditions [][]string `json:"return_conditions,omitempty"`
	Instructions     []string   `json:"instructions"`
}

type SSAValue struct {
	ID         string   `json:"id"`
	Function   string   `json:"function"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Text       string   `json:"text"`
	Kind       string   `json:"kind"`
	Type       string   `json:"type,omitempty"`
	Operands   []string `json:"operands,omitempty"`
	Callee     string   `json:"callee,omitempty"`
	Arguments  []string `json:"arguments,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
	Stores     []string `json:"stores,omitempty"`
	TupleIndex int      `json:"tuple_index,omitempty"`
	Gap        string   `json:"gap,omitempty"`
}

func (a Analyzer) buildGoSSA(ctx context.Context, root string, pkgs []*packages.Package, snapshot *Snapshot) {
	graph := &GoSSA{Functions: map[string]SSAFunction{}, Values: map[string]SSAValue{}}
	snapshot.GoSSA = graph
	// Select only loaded packages containing indexed declarations. Package
	// loading is shared with typed-call resolution; dependencies are not built.
	indexed := map[string]bool{}
	for _, node := range snapshot.Nodes {
		if node.Language == "go" {
			indexed[filepath.ToSlash(node.FilePath)] = true
		}
	}
	var selected []*packages.Package
	for _, pkg := range pkgs {
		if ctx.Err() != nil {
			graph.Limitations = append(graph.Limitations, "SSA construction cancelled")
			return
		}
		if pkg.IllTyped {
			continue
		}
		matches := false
		for _, file := range pkg.CompiledGoFiles {
			if rel, err := filepath.Rel(root, file); err == nil && indexed[filepath.ToSlash(rel)] {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		// SSA package building has no interrupt API. Limit the input syntax
		// before entering it, then bound extraction and traversal separately.
		nodes := 0
		for _, file := range pkg.Syntax {
			nodes += int(file.End() - file.Pos())
		}
		if nodes > 2<<20 || len(selected) >= 16 {
			graph.Limitations = append(graph.Limitations, "SSA package input budget exceeded")
			continue
		}
		selected = append(selected, pkg)
	}
	if len(selected) == 0 {
		graph.Limitations = append(graph.Limitations, "no type-complete indexed Go package was available")
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			graph.Limitations = append(graph.Limitations, fmt.Sprintf("SSA builder unavailable: %v", recovered))
		}
	}()
	program, ssaPackages := ssautil.Packages(selected, ssa.InstantiateGenerics)
	for _, pkg := range ssaPackages {
		if ctx.Err() != nil {
			return
		}
		if pkg != nil {
			pkg.Build()
		}
	}
	functions := make([]*ssa.Function, 0)
	for fn := range ssautil.AllFunctions(program) {
		if fn != nil && len(fn.Blocks) > 0 {
			functions = append(functions, fn)
		}
	}
	sort.Slice(functions, func(i, j int) bool { return functions[i].String() < functions[j].String() })
	if len(a.TargetTests) > 0 {
		targets := map[string]bool{}
		for _, name := range a.TargetTests {
			targets[name] = true
		}
		var queue []*ssa.Function
		for _, fn := range functions {
			rootFn := fn
			for rootFn.Parent() != nil {
				rootFn = rootFn.Parent()
			}
			if targets[rootFn.Name()] {
				queue = append(queue, fn)
			}
		}
		var focused []*ssa.Function
		seen := map[*ssa.Function]bool{}
		for depth := 0; depth < 4 && len(queue) > 0; depth++ {
			var next []*ssa.Function
			for _, fn := range queue {
				if seen[fn] {
					continue
				}
				seen[fn] = true
				focused = append(focused, fn)
				for _, block := range fn.Blocks {
					for _, instruction := range block.Instrs {
						if call, ok := instruction.(ssa.CallInstruction); ok {
							if callee := call.Common().StaticCallee(); callee != nil && len(callee.Blocks) > 0 {
								next = append(next, callee)
							}
						}
					}
				}
			}
			queue = next
		}
		functions = focused
		if len(functions) == 0 {
			graph.Limitations = append(graph.Limitations, "reported failing test has no SSA body in loaded package coverage")
		}
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for _, fn := range functions {
		if ctx.Err() != nil || time.Now().After(deadline) || len(graph.Values) >= 20000 || len(graph.Functions) >= 256 {
			graph.Limitations = append(graph.Limitations, "SSA extraction budget reached")
			break
		}
		pos := program.Fset.Position(fn.Pos())
		rel, err := filepath.Rel(root, pos.Filename)
		if err != nil || !indexed[filepath.ToSlash(rel)] {
			continue
		}
		instructions := 0
		for _, block := range fn.Blocks {
			instructions += len(block.Instrs)
		}
		if instructions > 6000 || len(graph.Values)+instructions > 20000 {
			graph.Limitations = append(graph.Limitations, "SSA function instruction budget exceeded")
			continue
		}
		graph.captureFunction(fn, program.Fset, filepath.ToSlash(rel), snapshot)
	}
}

func (g *GoSSA) captureFunction(fn *ssa.Function, fset *token.FileSet, file string, snapshot *Snapshot) {
	function := SSAFunction{ID: fn.String(), File: file, StartLine: fset.Position(fn.Pos()).Line}
	if syntax := fn.Syntax(); syntax != nil {
		function.EndLine = fset.Position(syntax.End()).Line
	}
	for _, node := range snapshot.Nodes {
		if node.FilePath == file && node.StartLine == function.StartLine && node.Name == fn.Name() {
			function.NodeID = node.ID
			break
		}
	}
	ids := map[ssa.Value]string{}
	for i, param := range fn.Params {
		id := fmt.Sprintf("%s:param:%d", function.ID, i)
		ids[param] = id
		function.Params = append(function.Params, id)
		g.Values[id] = SSAValue{ID: id, Function: function.ID, File: file, Line: function.StartLine, Kind: "parameter", Text: param.Name(), Type: param.Type().String()}
	}
	for i, param := range fn.FreeVars {
		id := fmt.Sprintf("%s:free:%d", function.ID, i)
		ids[param] = id
		g.Values[id] = SSAValue{ID: id, Function: function.ID, File: file, Line: function.StartLine, Kind: "free_variable", Text: param.Name(), Gap: "closure capture requires binding to its creation context"}
	}
	instructionIDs := map[ssa.Instruction]string{}
	for _, block := range fn.Blocks {
		for i, instruction := range block.Instrs {
			id := fmt.Sprintf("%s:b%d:%d", function.ID, block.Index, i)
			instructionIDs[instruction] = id
			if value, ok := instruction.(ssa.Value); ok {
				ids[value] = id
			}
		}
	}
	valueID := func(value ssa.Value) string {
		if value == nil {
			return ""
		}
		if id := ids[value]; id != "" {
			return id
		}
		id := fmt.Sprintf("%s:leaf:%d", function.ID, len(ids))
		ids[value] = id
		kind := "external"
		gap := "unresolved external value"
		if _, ok := value.(*ssa.Const); ok {
			kind = "constant"
			gap = ""
		}
		if _, ok := value.(*ssa.Function); ok {
			kind = "function"
			gap = ""
		}
		g.Values[id] = SSAValue{ID: id, Function: function.ID, File: file, Text: boundedSSAText(value.String()), Kind: kind, Gap: gap, Type: boundedSSAText(value.Type().String())}
		return id
	}
	stores := map[ssa.Value][]ssa.Instruction{}
	for _, block := range fn.Blocks {
		for _, instruction := range block.Instrs {
			if store, ok := instruction.(*ssa.Store); ok {
				address := ssaAddressRoot(store.Addr)
				stores[address] = append(stores[address], instruction)
			}
		}
	}
	for _, block := range fn.Blocks {
		for _, instruction := range block.Instrs {
			id := instructionIDs[instruction]
			value := SSAValue{ID: id, Function: function.ID, File: file, Line: fset.Position(instruction.Pos()).Line, Text: boundedSSAText(instruction.String()), Kind: fmt.Sprintf("%T", instruction)}
			if v, ok := instruction.(ssa.Value); ok {
				value.Type = boundedSSAText(v.Type().String())
			}
			for _, operand := range instruction.Operands(nil) {
				if operand != nil && *operand != nil {
					value.Operands = append(value.Operands, valueID(*operand))
				}
			}
			// Dominating conditions are relevant constraints, not proof of which
			// branch executed. Phi alternatives are retained rather than selected.
			for dom := block.Idom(); dom != nil; dom = dom.Idom() {
				if len(dom.Instrs) > 0 {
					if branch, ok := dom.Instrs[len(dom.Instrs)-1].(*ssa.If); ok {
						value.Conditions = append(value.Conditions, valueID(branch.Cond))
					}
				}
			}
			switch inst := instruction.(type) {
			case *ssa.Alloc:
				for _, store := range stores[inst] {
					value.Stores = append(value.Stores, instructionIDs[store])
				}
				if len(value.Stores) > 0 {
					value.Gap = "allocation stores are possible dependencies; write ordering and field/index precision remain unresolved"
				}
			case *ssa.Call:
				for _, arg := range inst.Common().Args {
					value.Arguments = append(value.Arguments, valueID(arg))
				}
				if target := inst.Common().StaticCallee(); target != nil {
					value.Callee = target.String()
				} else {
					value.Gap = "dynamic/interface call has no unique static target"
				}
			case *ssa.UnOp:
				if inst.Op == token.MUL {
					for _, store := range stores[ssaAddressRoot(inst.X)] {
						if store.Pos() <= inst.Pos() || store.Pos() == token.NoPos {
							value.Stores = append(value.Stores, instructionIDs[store])
						}
					}
					value.Gap = "load dependencies use exact SSA addresses; aliases and call side effects are unresolved"
				}
			case *ssa.Phi:
				value.Gap = "multiple control-flow alternatives; branch feasibility is unresolved"
			case *ssa.Extract:
				value.TupleIndex = inst.Index
			case *ssa.Return:
				var results []string
				for _, v := range inst.Results {
					results = append(results, valueID(v))
				}
				function.Returns = append(function.Returns, results)
				function.ReturnConditions = append(function.ReturnConditions, value.Conditions)
			case *ssa.Go:
				value.Gap = "goroutine effects require runtime ordering evidence"
			}
			g.Values[id] = value
			function.Instructions = append(function.Instructions, id)
		}
	}
	g.Functions[function.ID] = function
}

func boundedSSAText(text string) string {
	if len(text) > 512 {
		return text[:512] + " [truncated]"
	}
	return text
}

func ssaAddressRoot(value ssa.Value) ssa.Value {
	for i := 0; i < 16; i++ {
		switch address := value.(type) {
		case *ssa.FieldAddr:
			value = address.X
		case *ssa.IndexAddr:
			value = address.X
		default:
			return value
		}
	}
	return value
}

type SSATrace struct {
	Status      string     `json:"status"`
	Values      []SSAValue `json:"values,omitempty"`
	Limitations []string   `json:"limitations,omitempty"`
	Visited     int        `json:"visited"`
	EvidenceIDs []string   `json:"evidence_ids,omitempty"`
}

// TraceAssertion follows only operands and dominating predicates feeding the
// reported assertion. It does not execute repository code or invoke a model.
func (g *GoSSA) TraceAssertion(file string, line int) SSATrace {
	result := SSATrace{Status: "unavailable"}
	if g == nil {
		return result
	}
	result.Limitations = append(result.Limitations, g.Limitations...)
	var anchor *SSAFunction
	for _, fn := range g.Functions {
		if fn.File == file && line >= fn.StartLine && line <= fn.EndLine && (anchor == nil || fn.EndLine-fn.StartLine < anchor.EndLine-anchor.StartLine) {
			copy := fn
			anchor = &copy
		}
	}
	if anchor == nil {
		result.Limitations = append(result.Limitations, "no captured SSA function covers the reported assertion location")
		return result
	}
	type work struct {
		id                 string
		depth, calls, slot int
		bindings           map[string]string
	}
	queue := []work{}
	for _, id := range anchor.Instructions {
		v := g.Values[id]
		if v.Line == line && v.Kind == "*ssa.Call" {
			queue = append(queue, work{id: id, slot: -1})
		}
	}
	if len(queue) == 0 {
		result.Limitations = append(result.Limitations, "no SSA call matched the reported assertion line")
		return result
	}
	result.Status = "traced"
	deadline := time.Now().Add(75 * time.Millisecond)
	seen := map[string]bool{}
	addGap := func(gap string) {
		for _, existing := range result.Limitations {
			if existing == gap {
				return
			}
		}
		result.Limitations = append(result.Limitations, gap)
	}
	for len(queue) > 0 {
		if result.Visited >= 96 || time.Now().After(deadline) {
			result.Status = "budget_exhausted"
			addGap("SSA trace budget reached")
			break
		}
		item := queue[0]
		queue = queue[1:]
		if item.depth > 16 {
			addGap("SSA value depth limit reached")
			continue
		}
		boundIDs := map[string]bool{}
		for bindingDepth := 0; bindingDepth < 16; bindingDepth++ {
			bound := item.bindings[item.id]
			if bound == "" || bound == item.id {
				break
			}
			if boundIDs[bound] {
				addGap("recursive parameter binding is unresolved")
				break
			}
			boundIDs[item.id] = true
			item.id = bound
		}
		key := fmt.Sprintf("%s:%d:%v", item.id, item.slot, item.bindings)
		if seen[key] {
			continue
		}
		seen[key] = true
		value, ok := g.Values[item.id]
		if !ok {
			addGap("value outside captured SSA coverage")
			continue
		}
		result.Visited++
		if value.Kind == "function" {
			continue
		}
		if strings.Contains(value.Type, "testing.T") || strings.Contains(value.Type, "testing.B") {
			continue
		}
		result.Values = append(result.Values, value)
		if value.Gap != "" {
			addGap(value.Gap)
		}
		push := func(id string) {
			queue = append(queue, work{id: id, depth: item.depth + 1, calls: item.calls, slot: -1, bindings: item.bindings})
		}
		for _, id := range value.Conditions {
			push(id)
		}
		for _, id := range value.Stores {
			push(id)
		}
		if value.Kind == "*ssa.Extract" && len(value.Operands) > 0 {
			queue = append(queue, work{id: value.Operands[0], depth: item.depth + 1, calls: item.calls, slot: value.TupleIndex, bindings: item.bindings})
			continue
		}
		if value.Kind == "*ssa.Call" && value.Callee != "" {
			callee, ok := g.Functions[value.Callee]
			if ok && item.calls < 3 {
				bindings := map[string]string{}
				for k, v := range item.bindings {
					bindings[k] = v
				}
				for i, param := range callee.Params {
					if i < len(value.Arguments) {
						bindings[param] = value.Arguments[i]
					}
				}
				for returnIndex, ret := range callee.Returns {
					if returnIndex < len(callee.ReturnConditions) {
						for _, condition := range callee.ReturnConditions[returnIndex] {
							queue = append(queue, work{id: condition, depth: item.depth + 1, calls: item.calls + 1, slot: -1, bindings: bindings})
						}
					}
					for i, id := range ret {
						if item.slot < 0 || item.slot == i {
							queue = append(queue, work{id: id, depth: item.depth + 1, calls: item.calls + 1, slot: -1, bindings: bindings})
						}
					}
				}
			} else if !ok {
				addGap("callee body unavailable; only argument dependencies were traced")
			} else {
				addGap("SSA call depth limit reached")
			}
		}
		for _, id := range value.Operands {
			push(id)
		}
	}
	return result
}
