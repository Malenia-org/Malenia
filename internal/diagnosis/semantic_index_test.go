package diagnosis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/causent/malenia/internal/localization"
	programrepo "github.com/causent/malenia/internal/repository"
)

func TestSemanticOperationResolution(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"cache.go": `package cache

type Cache struct {
	value int
}

func update(c *Cache) {
	c.value = 10
}

func read(c *Cache) int {
	return c.value
}
`,
	}
	commit(t, dir, "semantic cache source", files)

	lineOf := func(needle string) int {
		data, err := os.ReadFile(filepath.Join(dir, "cache.go"))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, needle) {
				return i + 1
			}
		}
		t.Fatalf("%s not found", needle)
		return 0
	}

	writeLine := lineOf("c.value = 10")
	opWrite := ResolveSourceFrameOperation(dir, "cache.go", writeLine, "write", "enclosing_source_frame", "update")
	if !opWrite.IsExactMemory || opWrite.Kind != OpWrite || opWrite.FieldName != "value" || opWrite.SubjectExpr != "c" {
		t.Fatalf("opWrite unresolved or incorrect: %#v", opWrite)
	}
	if opWrite.ResolutionStatus != SourceResolutionExactAccess {
		t.Fatalf("write resolution status = %s, want exact_access", opWrite.ResolutionStatus)
	}

	readLine := lineOf("return c.value")
	opRead := ResolveSourceFrameOperation(dir, "cache.go", readLine, "read", "enclosing_source_frame", "read")
	if !opRead.IsExactMemory || opRead.Kind != OpRead || opRead.FieldName != "value" || opRead.SubjectExpr != "c" {
		t.Fatalf("opRead unresolved or incorrect: %#v", opRead)
	}
	if opRead.ResolutionStatus != SourceResolutionExactAccess {
		t.Fatalf("read resolution status = %s, want exact_access", opRead.ResolutionStatus)
	}
}

func TestObjectIdentityDistinctVsAliased(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"instances.go": `package cache

type Cache struct {
	value int
}

func DistinctAllocations() {
	a := &Cache{}
	b := &Cache{}
	a.value = 10
	b.value = 20
}

func AliasedAllocations() {
	a := &Cache{}
	b := a
	a.value = 10
	b.value = 20
}

func UnknownExternalCaller(c1 *Cache, c2 *Cache) {
	c1.value = 10
	c2.value = 20
}
`,
	}
	commit(t, dir, "instances source", files)

	index, err := BuildSemanticIndex(dir, []string{"instances.go"})
	if err != nil {
		t.Fatal(err)
	}

	lineOf := func(needle string) int {
		data, _ := os.ReadFile(filepath.Join(dir, "instances.go"))
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, needle) {
				return i + 1
			}
		}
		t.Fatalf("%s not found", needle)
		return 0
	}

	// 1. Proven distinct allocations
	distinctA := lineOf("a.value = 10")
	opDistA := ResolveSourceFrameOperation(dir, "instances.go", distinctA, "write", "enclosing_source_frame", "DistinctAllocations")
	distinctB := lineOf("b.value = 20")
	opDistB := ResolveSourceFrameOperation(dir, "instances.go", distinctB, "write", "enclosing_source_frame", "DistinctAllocations")

	if !index.HasDistinctAllocations(opDistA, opDistB) {
		t.Fatalf("expected distinct allocations between a and b: %#v, %#v", opDistA, opDistB)
	}
	resDist := index.CanAlias(opDistA, opDistB)
	if resDist.Status != AliasNo {
		t.Fatalf("expected AliasNo for distinct allocations, got %s (%s)", resDist.Status, resDist.Reason)
	}

	// 2. Proven aliased allocations
	aliasA := lineOf("b := a") + 1
	opAliasA := ResolveSourceFrameOperation(dir, "instances.go", aliasA, "write", "enclosing_source_frame", "AliasedAllocations")
	aliasB := aliasA + 1
	opAliasB := ResolveSourceFrameOperation(dir, "instances.go", aliasB, "write", "enclosing_source_frame", "AliasedAllocations")

	if index.HasDistinctAllocations(opAliasA, opAliasB) {
		t.Fatalf("aliased pointers incorrectly flagged as distinct allocations: %#v, %#v", opAliasA, opAliasB)
	}
	resAlias := index.CanAlias(opAliasA, opAliasB)
	if resAlias.Status != AliasMust {
		t.Fatalf("expected AliasMust for aliased variables, got %s (%s)", resAlias.Status, resAlias.Reason)
	}

	// 3. Unknown external caller: conservative unknown preserved
	extA := lineOf("c1.value = 10")
	opExtA := ResolveSourceFrameOperation(dir, "instances.go", extA, "write", "enclosing_source_frame", "UnknownExternalCaller")
	extB := lineOf("c2.value = 20")
	opExtB := ResolveSourceFrameOperation(dir, "instances.go", extB, "write", "enclosing_source_frame", "UnknownExternalCaller")

	resExt := index.CanAlias(opExtA, opExtB)
	if resExt.Status != AliasUnknown {
		t.Fatalf("expected AliasUnknown for unproven parameters, got %s (%s)", resExt.Status, resExt.Reason)
	}
}

func TestGlobalVariableObjectIdentity(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"global.go": `package race

var shared int

func Read() int {
	return shared
}

func Write(v int) {
	shared = v
}
`,
	}
	commit(t, dir, "global source", files)

	index, err := BuildSemanticIndex(dir, []string{"global.go"})
	if err != nil {
		t.Fatal(err)
	}

	lineOf := func(needle string) int {
		data, _ := os.ReadFile(filepath.Join(dir, "global.go"))
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, needle) {
				return i + 1
			}
		}
		t.Fatalf("%s not found", needle)
		return 0
	}

	opRead := ResolveSourceFrameOperation(dir, "global.go", lineOf("return shared"), "read", "enclosing_source_frame", "Read")
	opWrite := ResolveSourceFrameOperation(dir, "global.go", lineOf("shared = v"), "write", "enclosing_source_frame", "Write")

	res := index.CanAlias(opRead, opWrite)
	if res.Status != AliasMust {
		t.Fatalf("global variable accesses were not recognized as must-alias: %s (%s)", res.Status, res.Reason)
	}
	if !strings.Contains(res.Reason, "global variable") {
		t.Fatalf("unexpected alias reason for global: %s", res.Reason)
	}
}

func TestDataFlowInterprocedural(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"process.go": `package cache

type Cache struct {
	value int
}

func createCache() *Cache {
	return &Cache{}
}

func update(c *Cache) {
	c.value = 10
}

func read(c *Cache) int {
	return c.value
}

func process() {
	x := createCache()
	update(x)
	read(x)
}
`,
	}
	commit(t, dir, "process source", files)

	index, err := BuildSemanticIndex(dir, []string{"process.go"})
	if err != nil {
		t.Fatal(err)
	}

	trace := index.TraceDataFlow("x", "process", "update", "c")
	if trace == nil || len(trace.Steps) < 3 {
		t.Fatalf("interprocedural data flow trace incomplete: %#v", trace)
	}
	if trace.Origin != "process" || trace.Subject != "c" {
		t.Fatalf("trace endpoints incorrect: origin=%s, subject=%s", trace.Origin, trace.Subject)
	}
}

func TestRuntimeToSourceWrapperResolution(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"cacher.go": `package race

type Cacher struct {
	rawCacher *RawCacher
}

func NewCacherFromConfig(featureEnabled bool) *Cacher {
	if featureEnabled {
		return &Cacher{}
	}
	return &Cacher{}
}
`,
	}
	commit(t, dir, "wrapper source", files)

	lineOf := func(needle string) int {
		data, _ := os.ReadFile(filepath.Join(dir, "cacher.go"))
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, needle) {
				return i + 1
			}
		}
		t.Fatalf("%s not found", needle)
		return 0
	}

	line := lineOf("if featureEnabled")
	op := ResolveSourceFrameOperation(dir, "cacher.go", line, "read", "goroutine_creation_or_wrapper", "race.NewCacherFromConfig.gowrap3()")

	if op.IsExactMemory {
		t.Fatalf("wrapper frame was resolved as exact memory: %#v", op)
	}
	if op.ResolutionStatus != SourceResolutionUnresolved {
		t.Fatalf("wrapper frame resolution status = %s, want %s", op.ResolutionStatus, SourceResolutionUnresolved)
	}
	if !strings.Contains(op.ResolutionReason, "wrapper") {
		t.Fatalf("wrapper reason missing: %s", op.ResolutionReason)
	}
}

func TestRuntimeStaticCorrelationDistinguishesAddressFromMapping(t *testing.T) {
	report := &Report{
		InvestigationID: "correlation-test",
		Environment:     Environment{HeadCommit: "head"},
		RaceReports: []RaceReport{{
			ReadAddress:      "0x00c0006135c0",
			WriteAddress:     "0x00c0006135c0",
			ReadGoroutineID:  32511,
			WriteGoroutineID: 31033,
			ReadFrameRole:    "goroutine_creation_or_wrapper", // Wrapper frame!
			WriteFrameRole:   "enclosing_source_frame",
		}},
		Concurrency: &ConcurrencyModel{
			Accesses: []ConcurrencyAccess{
				{Access: "read", File: "cacher.go", Line: 496, FrameRole: "goroutine_creation_or_wrapper", ExactMemory: false},
				{Access: "write", File: "setup.go", Line: 187, FrameRole: "enclosing_source_frame", ExactMemory: false},
			},
		},
	}

	index := &SemanticIndex{}
	items, correlations := CorrelateRuntimeAndStatic(report, index)
	if len(items) != 1 || len(correlations) != 1 {
		t.Fatalf("expected 1 item and correlation, got %d, %d", len(items), len(correlations))
	}

	corr := correlations[0]
	if !corr.RuntimeAddressOverlap || corr.RuntimeAddress != "0x00c0006135c0" {
		t.Fatalf("runtime address overlap was not recorded: %#v", corr)
	}
	// Because read frame is a wrapper, static mapping MUST be unresolved_frame, NOT proven!
	if corr.MappingStatus != MappingUnresolvedFrame {
		t.Fatalf("wrapper frame was incorrectly marked as proven mapping: %s", corr.MappingStatus)
	}
}

func TestDetailedSynchronizationAnalysis(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"sync.go": `package race

import "sync"

type S struct {
	mu     sync.Mutex
	stopWg sync.WaitGroup
	val    int
}

func (s *S) SafeRead() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.val
}

func (s *S) SafeWrite(v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.val = v
}

func (s *S) UnsafeWrite(v int) {
	s.val = v
}

func (s *S) StartBackground() {
	go s.runAsync()
}

func (s *S) runAsync() {
	s.stopWg.Add(1)
	defer s.stopWg.Done()
}

func (s *S) Stop() {
	s.stopWg.Wait()
}
`,
	}
	commit(t, dir, "sync source", files)

	index, err := BuildSemanticIndex(dir, []string{"sync.go"})
	if err != nil {
		t.Fatal(err)
	}

	lineOf := func(needle string) int {
		data, _ := os.ReadFile(filepath.Join(dir, "sync.go"))
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, needle) {
				return i + 1
			}
		}
		t.Fatalf("%s not found", needle)
		return 0
	}

	opSafeRead := ResolveSourceFrameOperation(dir, "sync.go", lineOf("return s.val"), "read", "enclosing_source_frame", "SafeRead")
	opSafeWrite := ResolveSourceFrameOperation(dir, "sync.go", lineOf("s.val = v"), "write", "enclosing_source_frame", "SafeWrite")
	opUnsafeWrite := ResolveSourceFrameOperation(dir, "sync.go", lineOf("func (s *S) UnsafeWrite")+1, "write", "enclosing_source_frame", "UnsafeWrite")

	// Both safe: mutual exclusion verified, but non-deterministic caller order
	resBothSafe := index.AnalyzeDetailedSynchronization(opSafeRead, opSafeWrite, nil)
	if resBothSafe.MutexProtection != "mutual_exclusion_verified" {
		t.Fatalf("expected mutual_exclusion_verified, got %s", resBothSafe.MutexProtection)
	}

	// One safe, one unsafe: unprotected write detected
	resUnsafe := index.AnalyzeDetailedSynchronization(opSafeRead, opUnsafeWrite, nil)
	if resUnsafe.MutexProtection != "unprotected_write" {
		t.Fatalf("expected unprotected_write, got %s", resUnsafe.MutexProtection)
	}

	// WaitGroup Add inside spawned function: unsafe async Add detected
	resWG := index.AnalyzeDetailedSynchronization(opSafeRead, opSafeWrite, []string{"s.stopWg.Add(1)", "s.stopWg.Wait()"})
	if resWG.WaitGroupOrdering != "unsafe_async_add_concurrency" {
		t.Fatalf("expected unsafe_async_add_concurrency, got %s", resWG.WaitGroupOrdering)
	}
}

func TestKubernetesRaceSemanticInvestigation(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"cacher.go": `package cacher

type Cacher struct {
	rawCacher *RawCacher
}

type RawCacher struct {
	stopCh chan struct{}
}

func NewCacherFromConfig(featureEnabled bool) *Cacher {
	if featureEnabled {
		return &Cacher{}
	}
	return &Cacher{rawCacher: &RawCacher{stopCh: make(chan struct{})}}
}
`,
		"setup.go": `package setup

type Cacher struct {
	rawCacher *RawCacher
}

type RawCacher struct {
	stopCh chan struct{}
}

func SetupWithCacher(c *Cacher) {
	go func() {
		c.rawCacher.Stop()
	}()
}

func (r *RawCacher) Stop() {
	close(r.stopCh)
}
`,
	}
	commit(t, dir, "kubernetes race source", files)

	snapshot, err := (programrepo.Analyzer{}).Analyze(context.Background(), dir, "local:test", "revision:test", "head")
	if err != nil {
		t.Fatal(err)
	}

	report := &Report{
		InvestigationID:  "k8s-race-test",
		Environment:      Environment{HeadCommit: "head"},
		FailureIncidents: []FailureIncident{{Category: "go_data_race"}},
		RaceReports: []RaceReport{{
			ReadStack:        []localization.Frame{{File: "cacher.go", Line: 12, Function: "cacher.NewCacherFromConfig.gowrap3()"}},
			ReadFrameRole:    "goroutine_creation_or_wrapper",
			ReadAddress:      "0x00c0006135c0",
			ReadGoroutineID:  32511,
			WriteStack:       []localization.Frame{{File: "setup.go", Line: 12, Function: "setup.SetupWithCacher.func1()"}},
			WriteFrameRole:   "goroutine_creation_or_wrapper",
			WriteAddress:     "0x00c0006135c0",
			WriteGoroutineID: 31033,
		}},
	}

	d := &Diagnoser{cfg: Config{RepoPath: dir}}
	if err := d.runSourceInvestigation(context.Background(), report, dir, snapshot, nil, false); err != nil {
		t.Fatal(err)
	}

	if report.Concurrency == nil || len(report.Concurrency.Accesses) != 2 {
		t.Fatalf("concurrency accesses missing: %#v", report.Concurrency)
	}

	// Wrapper frames must NOT be resolved as exact memory accesses
	for _, acc := range report.Concurrency.Accesses {
		if acc.ExactMemory || acc.Expression != "" {
			t.Fatalf("wrapper frame was resolved as exact memory access: %#v", acc)
		}
	}

	// Runtime address evidence was created
	foundRuntimeAddressEvidence := false
	for _, ev := range report.Evidence {
		if ev.ClaimKind == "same-runtime-instance" && ev.InstanceID == "instance:addr:0x00c0006135c0" {
			foundRuntimeAddressEvidence = true
		}
	}
	if !foundRuntimeAddressEvidence {
		t.Fatalf("same-runtime-instance evidence was not emitted: %#v", report.Evidence)
	}

	// Objective predicates must preserve unresolved status because memory access is not resolved
	if report.InvestigationObjective == nil {
		t.Fatal("investigation objective is nil")
	}
	for _, unknown := range report.InvestigationObjective.Unknowns {
		if unknown.ID == "exact-memory" && unknown.Status == "resolved" {
			t.Fatalf("exact-memory was resolved from wrapper frames: %#v", unknown)
		}
	}
}
