package diagnosis

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// SemanticOperationKind classifies the source operation.
type SemanticOperationKind string

const (
	OpUnknown    SemanticOperationKind = "UNKNOWN"
	OpRead       SemanticOperationKind = "READ"
	OpWrite      SemanticOperationKind = "WRITE"
	OpCall       SemanticOperationKind = "CALL"
	OpSpawn      SemanticOperationKind = "SPAWN"
	OpSyncLock   SemanticOperationKind = "SYNC_LOCK"
	OpSyncUnlock SemanticOperationKind = "SYNC_UNLOCK"
	OpSyncWait   SemanticOperationKind = "SYNC_WAIT"
	OpSyncSignal SemanticOperationKind = "SYNC_SIGNAL"
	OpChanSend   SemanticOperationKind = "CHAN_SEND"
	OpChanRecv   SemanticOperationKind = "CHAN_RECV"
	OpChanClose  SemanticOperationKind = "CHAN_CLOSE"
)

// AliasStatus represents the 3-state sound alias analysis conclusion.
type AliasStatus string

const (
	AliasMust    AliasStatus = "must_alias"     // Statically proven to refer to the exact same object
	AliasNo      AliasStatus = "must_not_alias" // Statically proven to refer to distinct, non-aliasing objects
	AliasUnknown AliasStatus = "unknown"        // Unproven; uncertainty must be preserved
)

// AliasResult holds the 3-state status and audit reason.
type AliasResult struct {
	Status AliasStatus `json:"status"`
	Reason string      `json:"reason"`
}

// RuntimeStaticMappingStatus represents whether a runtime memory address was proven
// to correspond to a specific static object/declaration.
type RuntimeStaticMappingStatus string

const (
	MappingProven          RuntimeStaticMappingStatus = "proven"
	MappingUnproven        RuntimeStaticMappingStatus = "unproven"
	MappingUnresolvedFrame RuntimeStaticMappingStatus = "unresolved_frame"
)

// RuntimeStaticCorrelation represents the triple distinguishing runtime address overlap,
// static object identity, and the proven mapping between them.
type RuntimeStaticCorrelation struct {
	RuntimeAddress        string                     `json:"runtime_address"`
	RuntimeAddressOverlap bool                       `json:"runtime_address_overlap"`
	StaticDeclarationID   string                     `json:"static_declaration_id,omitempty"`
	StaticInstanceID      string                     `json:"static_instance_id,omitempty"`
	MappingStatus         RuntimeStaticMappingStatus `json:"mapping_status"`
	MappingExplanation    string                     `json:"mapping_explanation"`
}

// SemanticOperation models a program operation with typed identities.
type SemanticOperation struct {
	ID               string                `json:"id"`
	Kind             SemanticOperationKind `json:"kind"`
	AccessType       string                `json:"access_type"` // read, write, call, spawn, sync
	FilePath         string                `json:"file_path"`
	Line             int                   `json:"line"`
	Column           int                   `json:"column,omitempty"`
	EnclosingSymbol  string                `json:"enclosing_symbol,omitempty"`
	ReceiverType     string                `json:"receiver_type,omitempty"`
	Expression       string                `json:"expression,omitempty"`
	SubjectExpr      string                `json:"subject_expr,omitempty"`
	FieldName        string                `json:"field_name,omitempty"`
	ObjectType       string                `json:"object_type,omitempty"`
	DeclarationID    string                `json:"declaration_id,omitempty"`
	AccessID         string                `json:"access_id,omitempty"`
	InstanceID       string                `json:"instance_id,omitempty"`
	IsExactMemory    bool                  `json:"is_exact_memory"`
	FrameRole        string                `json:"frame_role"`
	ResolutionStatus string                `json:"resolution_status"`
	ResolutionReason string                `json:"resolution_reason,omitempty"`
	CapturedVars     []string              `json:"captured_vars,omitempty"`
	DataFlowOrigin   string                `json:"data_flow_origin,omitempty"`
	EnclosingLock    string                `json:"enclosing_lock,omitempty"`
}

// ObjectInstance models allocation identity and reference propagation.
type ObjectInstance struct {
	ID             string   `json:"id"`
	AllocationSite string   `json:"allocation_site,omitempty"`
	TypeName       string   `json:"type_name,omitempty"`
	IsGlobal       bool     `json:"is_global"`
	RuntimeAddress string   `json:"runtime_address,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
}

// DataFlowStep records one hop in an interprocedural value trace.
type DataFlowStep struct {
	FromNode    string `json:"from_node"`
	ToNode      string `json:"to_node"`
	Kind        string `json:"kind"` // alloc, assign, pass_arg, return_val, field_access
	Description string `json:"description"`
}

// DataFlowTrace connects value flow from allocation/creation to accesses.
type DataFlowTrace struct {
	Subject string         `json:"subject"`
	Origin  string         `json:"origin"`
	Steps   []DataFlowStep `json:"steps"`
}

// SemanticHappensBefore records a synchronization or ordering relationship.
type SemanticHappensBefore struct {
	ID            string `json:"id"`
	PredecessorOp string `json:"predecessor_op"`
	SuccessorOp   string `json:"successor_op"`
	Mechanism     string `json:"mechanism"` // channel_close_recv, waitgroup_done_wait, spawn_entry, mutex_critical_section
	MemoryModel   string `json:"memory_model"`
	Verified      bool   `json:"verified"`
	Detail        string `json:"detail"`
}

// SynchronizationAnalysis models mutex protection, channel ordering, and WaitGroup lifecycle.
type SynchronizationAnalysis struct {
	MutexProtection   string                  `json:"mutex_protection"` // mutual_exclusion_verified, unprotected_write, unprotected_read, different_mutexes, none
	MutexIdent        string                  `json:"mutex_ident,omitempty"`
	WaitGroupOrdering string                  `json:"waitgroup_ordering"` // safe_spawn_precedes_wait, unsafe_async_add_concurrency, none
	ChannelOrdering   string                  `json:"channel_ordering"`   // verified_close_before_receive, none
	HappensBefore     []SemanticHappensBefore `json:"happens_before,omitempty"`
	MissingOrdering   []string                `json:"missing_ordering,omitempty"`
	Details           []string                `json:"details,omitempty"`
}

// SemanticIndex holds parsed program facts for object identity and data flow.
type SemanticIndex struct {
	Workspace        string
	Files            map[string]*ast.File
	FileSets         map[string]*token.FileSet
	Globals          map[string]ObjectInstance    // pkg.varName -> ObjectInstance
	TypeFields       map[string]map[string]string // TypeName -> fieldName -> fieldType
	LocalAllocations map[string]ObjectInstance    // file:line:col -> ObjectInstance
	VarInstances     map[string]string            // file:func:varName -> InstanceID
	ParamBindings    map[string]string            // file:func:paramName -> InstanceID
	Calls            []semanticCallEdge
	SpawnedFuncs     map[string]bool // functions invoked via `go fn()`
	WaitGroupAdds    map[string]bool // functions containing `wg.Add`
	WaitGroupWaits   map[string]bool // functions containing `wg.Wait`
}

type semanticCallEdge struct {
	CallerFunc string
	CalleeFunc string
	Args       []string
	File       string
	Line       int
	IsGoSpawn  bool
}

// ResolveSourceFrameOperation maps a runtime frame or target location to its
// actual source operation with precise classification of memory vs wrapper roles.
func ResolveSourceFrameOperation(workspace, file string, line int, accessType, frameRole, functionName string) SemanticOperation {
	op := SemanticOperation{
		FilePath:         file,
		Line:             line,
		AccessType:       accessType,
		FrameRole:        frameRole,
		ResolutionStatus: SourceResolutionUnresolved,
	}

	cleanFile := filepath.ToSlash(filepath.Clean(file))
	fullPath := filepath.Join(workspace, filepath.FromSlash(cleanFile))
	data, err := os.ReadFile(fullPath)
	if err != nil {
		op.ResolutionReason = fmt.Sprintf("source file %s could not be read in checked-out revision: %v", file, err)
		return op
	}

	lines := strings.Split(string(data), "\n")
	if line < 1 || line > len(lines) {
		op.ResolutionReason = fmt.Sprintf("line %d outside source bounds (%d lines)", line, len(lines))
		return op
	}
	lineText := strings.TrimSpace(lines[line-1])

	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, fullPath, data, parser.ParseComments)
	if err != nil {
		op.ResolutionReason = fmt.Sprintf("failed to parse Go source: %v", err)
		return op
	}

	// Classify frame role
	isWrapperFunction := strings.Contains(functionName, ".gowrap") ||
		strings.Contains(functionName, ".func1") ||
		strings.Contains(functionName, ".func2") ||
		frameRole == "goroutine_creation_or_wrapper"

	isGoroutineSpawn := strings.HasPrefix(lineText, "go ")
	if isWrapperFunction || isGoroutineSpawn {
		op.FrameRole = "goroutine_creation_or_wrapper"
	} else if op.FrameRole == "" {
		op.FrameRole = "enclosing_source_frame"
	}

	// Enclosing function and declaration search
	var enclosingFunc *ast.FuncDecl
	var enclosingFuncLit *ast.FuncLit
	ast.Inspect(astFile, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		startLine := fset.Position(n.Pos()).Line
		endLine := fset.Position(n.End()).Line
		if startLine <= line && line <= endLine {
			if fd, ok := n.(*ast.FuncDecl); ok {
				enclosingFunc = fd
			}
			if fl, ok := n.(*ast.FuncLit); ok {
				enclosingFuncLit = fl
			}
			return true
		}
		return false
	})

	if enclosingFunc != nil {
		op.EnclosingSymbol = enclosingFunc.Name.Name
		if enclosingFunc.Recv != nil && len(enclosingFunc.Recv.List) > 0 {
			op.ReceiverType = nodeString(fset, enclosingFunc.Recv.List[0].Type)
		}
		// Check mutex lock enclosing this line
		op.EnclosingLock = findEnclosingLock(fset, enclosingFunc.Body, line)
	} else if enclosingFuncLit != nil {
		op.EnclosingSymbol = "anonymous_func"
		op.EnclosingLock = findEnclosingLock(fset, enclosingFuncLit.Body, line)
	}

	// If it is strictly a goroutine spawn line, it is NOT an exact memory access
	if isGoroutineSpawn {
		op.Kind = OpSpawn
		op.IsExactMemory = false
		op.ResolutionStatus = SourceResolutionUnresolved
		op.ResolutionReason = "runtime frame is a goroutine creation or wrapper site; the reported line is not the memory access"
		return op
	}

	if isWrapperFunction {
		// Inspect whether this line is control flow or simple call dispatch
		if strings.HasPrefix(lineText, "if ") || strings.HasPrefix(lineText, "for ") ||
			strings.HasPrefix(lineText, "switch ") || strings.HasPrefix(lineText, "select ") {
			op.Kind = OpUnknown
			op.IsExactMemory = false
			op.ResolutionStatus = SourceResolutionUnresolved
			op.ResolutionReason = "runtime frame is a wrapper control-flow site; the reported line is not the memory access"
			return op
		}
	}

	// Find the finest statement/expression on this line
	var candidateAssign *ast.AssignStmt
	var candidateReturn *ast.ReturnStmt
	var candidateCall *ast.CallExpr
	var candidateSelector *ast.SelectorExpr
	var candidateIdent *ast.Ident
	var candidateUnary *ast.UnaryExpr

	ast.Inspect(astFile, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		start := fset.Position(n.Pos()).Line
		end := fset.Position(n.End()).Line
		if start > line || end < line {
			return true
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			candidateAssign = node
		case *ast.ReturnStmt:
			candidateReturn = node
		case *ast.CallExpr:
			candidateCall = node
		case *ast.SelectorExpr:
			candidateSelector = node
		case *ast.UnaryExpr:
			candidateUnary = node
		case *ast.Ident:
			if candidateIdent == nil {
				candidateIdent = node
			}
		}
		return true
	})

	// Exact memory resolution
	switch {
	case candidateAssign != nil:
		for _, lhs := range candidateAssign.Lhs {
			exprStr := nodeString(fset, lhs)
			if sel, ok := lhs.(*ast.SelectorExpr); ok {
				op.Kind = OpWrite
				op.IsExactMemory = true
				op.Expression = exprStr
				op.SubjectExpr = nodeString(fset, sel.X)
				op.FieldName = sel.Sel.Name
				op.ResolutionStatus = SourceResolutionExactAccess
				op.DeclarationID = op.EnclosingSymbol + "." + op.FieldName
				op.AccessID = fmt.Sprintf("access:write:%s:%d:%s", cleanFile, line, op.FieldName)
				return op
			}
			if ident, ok := lhs.(*ast.Ident); ok {
				op.Kind = OpWrite
				op.IsExactMemory = true
				op.Expression = exprStr
				op.SubjectExpr = ident.Name
				op.FieldName = ident.Name
				op.ResolutionStatus = SourceResolutionExactAccess
				op.DeclarationID = ident.Name
				op.AccessID = fmt.Sprintf("access:write:%s:%d:%s", cleanFile, line, ident.Name)
				return op
			}
		}

	case candidateReturn != nil:
		for _, res := range candidateReturn.Results {
			exprStr := nodeString(fset, res)
			if sel, ok := res.(*ast.SelectorExpr); ok {
				op.Kind = OpRead
				op.IsExactMemory = true
				op.Expression = exprStr
				op.SubjectExpr = nodeString(fset, sel.X)
				op.FieldName = sel.Sel.Name
				op.ResolutionStatus = SourceResolutionExactAccess
				op.DeclarationID = op.EnclosingSymbol + "." + op.FieldName
				op.AccessID = fmt.Sprintf("access:read:%s:%d:%s", cleanFile, line, op.FieldName)
				return op
			}
			if ident, ok := res.(*ast.Ident); ok {
				op.Kind = OpRead
				op.IsExactMemory = true
				op.Expression = exprStr
				op.SubjectExpr = ident.Name
				op.FieldName = ident.Name
				op.ResolutionStatus = SourceResolutionExactAccess
				op.DeclarationID = ident.Name
				op.AccessID = fmt.Sprintf("access:read:%s:%d:%s", cleanFile, line, ident.Name)
				return op
			}
		}

	case candidateCall != nil && !isWrapperFunction:
		op.Kind = OpCall
		op.Expression = nodeString(fset, candidateCall)
		op.IsExactMemory = false
		op.ResolutionStatus = SourceResolutionLineOnly
		op.ResolutionReason = "line contains a call or composite expression; no single concrete memory operation could be resolved"
		return op

	case candidateSelector != nil && !isWrapperFunction:
		op.Kind = OpRead
		if accessType == "write" {
			op.Kind = OpWrite
		}
		op.IsExactMemory = true
		op.Expression = nodeString(fset, candidateSelector)
		op.SubjectExpr = nodeString(fset, candidateSelector.X)
		op.FieldName = candidateSelector.Sel.Name
		op.ResolutionStatus = SourceResolutionExactAccess
		op.DeclarationID = op.EnclosingSymbol + "." + op.FieldName
		op.AccessID = fmt.Sprintf("access:%s:%s:%d:%s", strings.ToLower(string(op.Kind)), cleanFile, line, op.FieldName)
		return op

	case candidateUnary != nil && candidateUnary.Op == token.MUL && !isWrapperFunction:
		op.Kind = OpRead
		if accessType == "write" {
			op.Kind = OpWrite
		}
		op.IsExactMemory = true
		op.Expression = nodeString(fset, candidateUnary)
		op.SubjectExpr = nodeString(fset, candidateUnary.X)
		op.ResolutionStatus = SourceResolutionExactAccess
		op.DeclarationID = op.Expression
		op.AccessID = fmt.Sprintf("access:%s:%s:%d:deref", strings.ToLower(string(op.Kind)), cleanFile, line)
		return op

	case candidateIdent != nil && !isWrapperFunction && accessType != "":
		op.Kind = OpRead
		if accessType == "write" {
			op.Kind = OpWrite
		}
		op.IsExactMemory = true
		op.Expression = candidateIdent.Name
		op.SubjectExpr = candidateIdent.Name
		op.FieldName = candidateIdent.Name
		op.ResolutionStatus = SourceResolutionExactAccess
		op.DeclarationID = candidateIdent.Name
		op.AccessID = fmt.Sprintf("access:%s:%s:%d:%s", strings.ToLower(string(op.Kind)), cleanFile, line, candidateIdent.Name)
		return op
	}

	if isWrapperFunction {
		op.ResolutionReason = "runtime frame is a goroutine creation or wrapper site; the reported line is not the memory access"
	} else {
		op.ResolutionReason = "line contains a call or composite expression; no single concrete memory operation could be resolved"
	}
	return op
}

func findEnclosingLock(fset *token.FileSet, body *ast.BlockStmt, targetLine int) string {
	if body == nil {
		return ""
	}
	var lockVar string
	for _, stmt := range body.List {
		pos := fset.Position(stmt.Pos())
		if pos.Line >= targetLine {
			break
		}
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Lock" || sel.Sel.Name == "RLock") {
					lockVar = nodeString(fset, sel.X)
				}
			}
		}
	}
	return lockVar
}

// BuildSemanticIndex constructs object identity, alias mapping, def-use chains,
// and interprocedural parameter passing for Go files in the repository.
func BuildSemanticIndex(workspace string, files []string) (*SemanticIndex, error) {
	index := &SemanticIndex{
		Workspace:        workspace,
		Files:            map[string]*ast.File{},
		FileSets:         map[string]*token.FileSet{},
		Globals:          map[string]ObjectInstance{},
		TypeFields:       map[string]map[string]string{},
		LocalAllocations: map[string]ObjectInstance{},
		VarInstances:     map[string]string{},
		ParamBindings:    map[string]string{},
		SpawnedFuncs:     map[string]bool{},
		WaitGroupAdds:    map[string]bool{},
		WaitGroupWaits:   map[string]bool{},
	}

	for _, rel := range files {
		clean := filepath.ToSlash(filepath.Clean(rel))
		if filepath.Ext(clean) != ".go" {
			continue
		}
		full := filepath.Join(workspace, filepath.FromSlash(clean))
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, full, data, 0)
		if err != nil {
			continue
		}
		index.Files[clean] = parsed
		index.FileSets[clean] = fset

		pkg := parsed.Name.Name

		// Collect type definitions and package globals
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gen.Tok {
			case token.TYPE:
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					typeName := ts.Name.Name
					if index.TypeFields[typeName] == nil {
						index.TypeFields[typeName] = map[string]string{}
					}
					for _, field := range st.Fields.List {
						fieldType := nodeString(fset, field.Type)
						for _, name := range field.Names {
							index.TypeFields[typeName][name.Name] = fieldType
						}
					}
				}
			case token.VAR:
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						globalID := "instance:global:" + pkg + "." + name.Name
						index.Globals[name.Name] = ObjectInstance{
							ID:       globalID,
							TypeName: nodeString(fset, vs.Type),
							IsGlobal: true,
						}
						index.Globals[pkg+"."+name.Name] = index.Globals[name.Name]
					}
				}
			}
		}
	}

	// Second pass: extract allocations, def-use alias chains, calls, and goroutine spawns
	for clean, astFile := range index.Files {
		fset := index.FileSets[clean]
		for _, decl := range astFile.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			funcKey := clean + ":" + fn.Name.Name

			// Map parameters
			if fn.Type.Params != nil {
				for _, param := range fn.Type.Params.List {
					for _, name := range param.Names {
						paramKey := funcKey + ":" + name.Name
						paramInstanceID := "instance:param:" + paramKey
						index.ParamBindings[paramKey] = paramInstanceID
						index.VarInstances[paramKey] = paramInstanceID
					}
				}
			}

			// Inspect function body
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch stmt := n.(type) {
				case *ast.GoStmt:
					// Goroutine spawn site
					if call := stmt.Call; call != nil {
						switch f := call.Fun.(type) {
						case *ast.Ident:
							index.SpawnedFuncs[f.Name] = true
						case *ast.SelectorExpr:
							index.SpawnedFuncs[f.Sel.Name] = true
						}
					}

				case *ast.AssignStmt:
					for i, rhs := range stmt.Rhs {
						if i >= len(stmt.Lhs) {
							break
						}
						lhsIdent, ok := stmt.Lhs[i].(*ast.Ident)
						if !ok {
							continue
						}
						varKey := funcKey + ":" + lhsIdent.Name

						if isAllocationExpr(rhs) {
							pos := fset.Position(rhs.Pos())
							allocID := fmt.Sprintf("instance:alloc:%s:%d:%d", clean, pos.Line, pos.Column)
							inst := ObjectInstance{
								ID:             allocID,
								AllocationSite: fmt.Sprintf("%s:%d", clean, pos.Line),
								TypeName:       nodeString(fset, rhs),
							}
							index.LocalAllocations[allocID] = inst
							index.VarInstances[varKey] = allocID
						} else if rhsIdent, ok := rhs.(*ast.Ident); ok {
							rhsKey := funcKey + ":" + rhsIdent.Name
							if instID, ok := index.VarInstances[rhsKey]; ok {
								index.VarInstances[varKey] = instID
							}
						}
					}

				case *ast.CallExpr:
					calleeName := ""
					switch fun := stmt.Fun.(type) {
					case *ast.Ident:
						calleeName = fun.Name
					case *ast.SelectorExpr:
						calleeName = fun.Sel.Name
						if fun.Sel.Name == "Add" {
							index.WaitGroupAdds[fn.Name.Name] = true
						}
						if fun.Sel.Name == "Wait" {
							index.WaitGroupWaits[fn.Name.Name] = true
						}
					}
					var argNames []string
					for _, arg := range stmt.Args {
						argNames = append(argNames, nodeString(fset, arg))
					}
					pos := fset.Position(stmt.Pos())
					index.Calls = append(index.Calls, semanticCallEdge{
						CallerFunc: fn.Name.Name,
						CalleeFunc: calleeName,
						Args:       argNames,
						File:       clean,
						Line:       pos.Line,
					})
				}
				return true
			})
		}
	}

	// Interprocedural propagation: map caller arguments to callee parameters
	for _, call := range index.Calls {
		if call.CalleeFunc == "" || len(call.Args) == 0 {
			continue
		}
		for clean, astFile := range index.Files {
			for _, decl := range astFile.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != call.CalleeFunc || fn.Type.Params == nil {
					continue
				}
				calleeKey := clean + ":" + fn.Name.Name
				argIdx := 0
				for _, param := range fn.Type.Params.List {
					for _, name := range param.Names {
						if argIdx < len(call.Args) {
							argName := call.Args[argIdx]
							callerKey := call.File + ":" + call.CallerFunc + ":" + argName
							if instID, ok := index.VarInstances[callerKey]; ok {
								calleeParamKey := calleeKey + ":" + name.Name
								index.ParamBindings[calleeParamKey] = instID
								index.VarInstances[calleeParamKey] = instID
							}
							argIdx++
						}
					}
				}
			}
		}
	}

	return index, nil
}

func isAllocationExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			_, isComposite := e.X.(*ast.CompositeLit)
			return isComposite
		}
	case *ast.CallExpr:
		if ident, ok := e.Fun.(*ast.Ident); ok && (ident.Name == "new" || ident.Name == "make") {
			return true
		}
	case *ast.CompositeLit:
		return true
	}
	return false
}

// HasDistinctAllocations returns true if both operations are proven to operate
// on different, non-aliasing memory allocations.
func (idx *SemanticIndex) HasDistinctAllocations(opA, opB SemanticOperation) bool {
	if opA.InstanceID != "" && opB.InstanceID != "" &&
		strings.HasPrefix(opA.InstanceID, "instance:alloc:") &&
		strings.HasPrefix(opB.InstanceID, "instance:alloc:") &&
		opA.InstanceID != opB.InstanceID {
		return true
	}
	keyA := opA.FilePath + ":" + opA.EnclosingSymbol + ":" + opA.SubjectExpr
	keyB := opB.FilePath + ":" + opB.EnclosingSymbol + ":" + opB.SubjectExpr
	instA := idx.VarInstances[keyA]
	instB := idx.VarInstances[keyB]
	if instA != "" && instB != "" &&
		strings.HasPrefix(instA, "instance:alloc:") &&
		strings.HasPrefix(instB, "instance:alloc:") &&
		instA != instB {
		return true
	}
	return false
}

// CanAlias returns a sound 3-state alias determination:
// - AliasMust when proven to refer to the same object
// - AliasNo when proven to refer to distinct, non-aliasing objects
// - AliasUnknown when static analysis cannot prove either conclusion.
func (idx *SemanticIndex) CanAlias(opA, opB SemanticOperation) AliasResult {
	if !opA.IsExactMemory || !opB.IsExactMemory {
		return AliasResult{
			Status: AliasUnknown,
			Reason: "at least one operation is not an exact memory access",
		}
	}

	// Proven distinct allocations
	if idx.HasDistinctAllocations(opA, opB) {
		return AliasResult{
			Status: AliasNo,
			Reason: fmt.Sprintf("operations refer to distinct allocation instances (%s vs %s)", opA.InstanceID, opB.InstanceID),
		}
	}

	// Proven same global variable
	if opA.DeclarationID != "" && opA.DeclarationID == opB.DeclarationID {
		if glob, ok := idx.Globals[opA.DeclarationID]; ok && glob.IsGlobal {
			return AliasResult{
				Status: AliasMust,
				Reason: fmt.Sprintf("both operations access identical package global variable %s", opA.DeclarationID),
			}
		}
	}

	// Proven same local alias or parameter binding
	keyA := opA.FilePath + ":" + opA.EnclosingSymbol + ":" + opA.SubjectExpr
	keyB := opB.FilePath + ":" + opB.EnclosingSymbol + ":" + opB.SubjectExpr

	instA := idx.VarInstances[keyA]
	instB := idx.VarInstances[keyB]

	if instA != "" && instB != "" {
		if instA == instB {
			return AliasResult{
				Status: AliasMust,
				Reason: fmt.Sprintf("both operations bind to the same instance (%s)", instA),
			}
		}
		if strings.HasPrefix(instA, "instance:alloc:") && strings.HasPrefix(instB, "instance:alloc:") {
			return AliasResult{
				Status: AliasNo,
				Reason: fmt.Sprintf("operations bind to distinct allocation instances (%s vs %s)", instA, instB),
			}
		}
	}

	// Receiver and field match without proven instance binding: preserve uncertainty
	if opA.FieldName != "" && opA.FieldName == opB.FieldName {
		if opA.ReceiverType != "" && opA.ReceiverType == opB.ReceiverType {
			return AliasResult{
				Status: AliasUnknown,
				Reason: fmt.Sprintf("operations access field %s on receiver type %s, but calling instances are not proven to alias", opA.FieldName, opA.ReceiverType),
			}
		}
		if opA.ObjectType != "" && opA.ObjectType == opB.ObjectType {
			return AliasResult{
				Status: AliasUnknown,
				Reason: fmt.Sprintf("operations access field %s on object type %s, but calling instances are not proven to alias", opA.FieldName, opA.ObjectType),
			}
		}
	}

	return AliasResult{
		Status: AliasUnknown,
		Reason: "pointer/reference relationship cannot be statically proven; preserving uncertainty",
	}
}

// TraceDataFlow reconstructs the interprocedural value flow between operations.
func (idx *SemanticIndex) TraceDataFlow(allocVar, callerFunc, calleeFunc, paramName string) *DataFlowTrace {
	trace := &DataFlowTrace{
		Subject: paramName,
		Origin:  callerFunc,
	}

	trace.Steps = append(trace.Steps, DataFlowStep{
		FromNode:    allocVar,
		ToNode:      callerFunc,
		Kind:        "alloc",
		Description: fmt.Sprintf("variable %s allocated or created in %s", allocVar, callerFunc),
	})

	for _, call := range idx.Calls {
		if call.CallerFunc == callerFunc && call.CalleeFunc == calleeFunc {
			trace.Steps = append(trace.Steps, DataFlowStep{
				FromNode:    allocVar,
				ToNode:      fmt.Sprintf("%s(%s)", calleeFunc, paramName),
				Kind:        "pass_arg",
				Description: fmt.Sprintf("passed as argument %s to %s parameter %s at %s:%d", allocVar, calleeFunc, paramName, call.File, call.Line),
			})
			break
		}
	}

	trace.Steps = append(trace.Steps, DataFlowStep{
		FromNode:    paramName,
		ToNode:      calleeFunc,
		Kind:        "field_access",
		Description: fmt.Sprintf("accessed via parameter %s inside %s", paramName, calleeFunc),
	})

	return trace
}

// AnalyzeDetailedSynchronization inspects mutex protection, channel ordering, and WaitGroup lifecycle.
func (idx *SemanticIndex) AnalyzeDetailedSynchronization(opA, opB SemanticOperation, syncLines []string) SynchronizationAnalysis {
	result := SynchronizationAnalysis{
		MutexProtection:   "none",
		WaitGroupOrdering: "none",
		ChannelOrdering:   "none",
	}

	// 1. Mutex critical section analysis
	lockA := opA.EnclosingLock
	lockB := opB.EnclosingLock

	if lockA != "" && lockB != "" {
		if lockA == lockB {
			result.MutexProtection = "mutual_exclusion_verified"
			result.MutexIdent = lockA
			result.Details = append(result.Details, fmt.Sprintf("both accesses are enclosed within critical sections of mutex %s; concurrent execution is serialized", lockA))
			result.HappensBefore = append(result.HappensBefore, SemanticHappensBefore{
				ID:          "hb:mutex:" + lockA,
				Mechanism:   "mutex_critical_section",
				MemoryModel: "go",
				Verified:    false, // Mutual exclusion does not dictate caller execution order
				Detail:      fmt.Sprintf("mutex %s serializes access inside critical sections, but does not establish a fixed caller order", lockA),
			})
		} else {
			result.MutexProtection = "different_mutexes"
			result.MissingOrdering = append(result.MissingOrdering, fmt.Sprintf("access A is protected by mutex %s but access B is protected by different mutex %s", lockA, lockB))
		}
	} else if lockA != "" && lockB == "" && opB.IsExactMemory {
		result.MutexProtection = "unprotected_write"
		if opB.Kind == OpRead {
			result.MutexProtection = "unprotected_read"
		}
		result.MissingOrdering = append(result.MissingOrdering, fmt.Sprintf("access A is protected by mutex %s, but access B is unprotected", lockA))
	} else if lockB != "" && lockA == "" && opA.IsExactMemory {
		result.MutexProtection = "unprotected_write"
		if opA.Kind == OpRead {
			result.MutexProtection = "unprotected_read"
		}
		result.MissingOrdering = append(result.MissingOrdering, fmt.Sprintf("access B is protected by mutex %s, but access A is unprotected", lockB))
	}

	// Channel and WaitGroup lines are discovery only.  Their presence does not
	// identify the same channel/group, the relevant paths, or an edge between
	// these exact operations.  Typed edges may be supplied by a future runtime
	// or language adapter; this AST/text pass must not manufacture them.
	verifiedEdge := false
	for _, edge := range result.HappensBefore {
		verifiedEdge = verifiedEdge || edge.Verified
	}
	if !verifiedEdge {
		result.MissingOrdering = append(result.MissingOrdering, "no typed happens-before edge was established between the conflicting accesses")
	}

	return result
}

// CorrelateRuntimeAndStatic distinguishes:
// 1. Same runtime address (from detector overlap).
// 2. Same static object (from verified static alias analysis).
// 3. Proven mapping between runtime and static identities.
func CorrelateRuntimeAndStatic(report *Report, index *SemanticIndex) ([]EvidenceItem, []RuntimeStaticCorrelation) {
	var items []EvidenceItem
	var correlations []RuntimeStaticCorrelation
	if report == nil || len(report.RaceReports) == 0 {
		return items, correlations
	}

	for i, race := range report.RaceReports {
		if race.ReadAddress == "" || race.WriteAddress == "" || race.ReadAddress != race.WriteAddress {
			continue
		}

		corr := RuntimeStaticCorrelation{
			RuntimeAddress:        race.ReadAddress,
			RuntimeAddressOverlap: true,
			MappingStatus:         MappingUnproven,
		}

		// Check whether accesses are resolved or wrapper frames
		readResolved := race.ReadFrameRole != "goroutine_creation_or_wrapper"
		writeResolved := race.WriteFrameRole != "goroutine_creation_or_wrapper"

		if !readResolved || !writeResolved {
			corr.MappingStatus = MappingUnresolvedFrame
			corr.MappingExplanation = fmt.Sprintf("runtime address %s was observed, but at least one frame is a wrapper/creation site; static-to-runtime mapping remains unproven", race.ReadAddress)
		} else {
			corr.MappingExplanation = fmt.Sprintf("runtime address %s observed in both accesses; static declaration mapping requires verified access expressions", race.ReadAddress)
		}

		hash := sha256.Sum256([]byte(report.InvestigationID + "\x00" + race.ReadAddress + "\x00" + fmt.Sprint(i)))
		digest := hex.EncodeToString(hash[:])
		evID := "E-runtime-instance-" + digest[:12]

		detail := fmt.Sprintf("CI race detector observed identical runtime memory address %s accessed by read (goroutine %d) and write (goroutine %d); %s",
			race.ReadAddress, race.ReadGoroutineID, race.WriteGoroutineID, corr.MappingExplanation)

		item := EvidenceItem{
			ID:                  evID,
			Kind:                "runtime_memory_location_overlap",
			ClaimKind:           "RuntimeMemoryLocationOverlap",
			Basis:               BasisObserved,
			Revision:            report.Environment.HeadCommit,
			Detail:              detail,
			Source:              "Go race detector",
			Provenance:          "runtime memory address overlap verification",
			Digest:              digest,
			RunID:               report.InvestigationID,
			RelatedAccessIDs:    []string{},
			RuntimeAddress:      race.ReadAddress,
			StaticMappingStatus: "unproven",
		}

		items = append(items, item)
		correlations = append(correlations, corr)
	}

	return items, correlations
}

func nodeString(fset *token.FileSet, n ast.Node) string {
	if n == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, n); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}
