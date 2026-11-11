package diagnosis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/causent/malenia/internal/domain"
	"github.com/causent/malenia/internal/localization"
	"github.com/causent/malenia/internal/persistence"
	programrepo "github.com/causent/malenia/internal/repository"
)

func TestSourceInvestigationPersistsBoundedFactsAndHypothesis(t *testing.T) {
	dir := newRepo(t)
	commit(t, dir, "race source", map[string]string{
		"race.go": `package race

import "sync"

var shared int
var mu sync.Mutex

func Read() int {
	mu.Lock()
	defer mu.Unlock()
	return shared
}

func Write(value int) {
	mu.Lock()
	defer mu.Unlock()
	shared = value
}
`,
	})
	store, err := persistence.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	lineOf := func(needle string) int {
		data, readErr := os.ReadFile(filepath.Join(dir, "race.go"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for index, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, needle) {
				return index + 1
			}
		}
		t.Fatalf("%s not found", needle)
		return 0
	}
	defer store.Close()
	if err := store.SaveInvestigation(domain.Investigation{ID: "source-investigation-test", RepoPath: dir}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (programrepo.Analyzer{}).Analyze(context.Background(), dir, "local:test", "revision:test", "head")
	if err != nil {
		t.Fatal(err)
	}
	report := &Report{
		InvestigationID: "source-investigation-test",
		Environment:     Environment{HeadCommit: "head"},
		FailureIncidents: []FailureIncident{{
			Category: "go_data_race",
			ObservedLocations: []IncidentLocation{
				{Access: "read", File: "race.go", Line: lineOf("return shared"), Provenance: "direct_stack_frame"},
				{Access: "write", File: "race.go", Line: lineOf("shared = value"), Provenance: "direct_stack_frame"},
			},
		}},
		RaceReports: []RaceReport{{
			ReadStack:  []localization.Frame{{File: "race.go", Line: lineOf("return shared"), Function: "Read"}},
			WriteStack: []localization.Frame{{File: "race.go", Line: lineOf("shared = value"), Function: "Write"}},
		}},
	}
	d := &Diagnoser{cfg: Config{RepoPath: dir, Store: store}}
	if err := d.runSourceInvestigation(context.Background(), report, dir, snapshot, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(report.InvestigationActions) < 6 || len(report.Hypotheses) != 1 {
		t.Fatalf("source investigation was incomplete: actions=%d hypotheses=%#v", len(report.InvestigationActions), report.Hypotheses)
	}
	if report.InvestigationObjective == nil || report.InvestigationObjective.Status == "causal_review_required" || report.InvestigationObjective.StoppingReason == "" {
		t.Fatalf("investigation loop did not preserve unresolved objective: %#v", report.InvestigationObjective)
	}
	for _, action := range report.InvestigationActions {
		if len(action.UnknownIDs) == 0 || action.Result == "" {
			t.Fatalf("action was not selected/evaluated against an unknown: %#v", action)
		}
	}
	if report.Concurrency == nil || len(report.Concurrency.Accesses) != 2 || report.Concurrency.Accesses[0].Expression == "" || len(report.Concurrency.SharedState) != 1 || report.Concurrency.SharedState[0] != "shared" {
		t.Fatalf("semantic concurrency model is incomplete: %#v", report.Concurrency)
	}
	if len(report.Concurrency.HappensBefore) != 0 || len(report.Concurrency.MissingOrdering) == 0 {
		t.Fatalf("unsupported synchronization conclusion: %#v", report.Concurrency)
	}
	hypothesis := report.Hypotheses[0]
	if len(hypothesis.SupportingEvidenceIDs) == 0 || len(hypothesis.PredictedObservations) == 0 || len(hypothesis.MissingEvidence) == 0 {
		t.Fatalf("hypothesis is not evidence-backed and falsifiable: %#v", hypothesis)
	}
	fullFunction := false
	for _, evidence := range report.Evidence {
		if evidence.Kind == "source function" && strings.Contains(evidence.Detail, "return shared") && strings.Contains(evidence.Detail, "func Read") {
			fullFunction = true
		}
	}
	if !fullFunction {
		t.Fatalf("complete enclosing function was not recorded")
	}
	activities, err := store.ListActivities(domain.ID(report.InvestigationID))
	if err != nil || len(activities) != len(report.InvestigationActions) {
		t.Fatalf("persisted action ledger mismatch: activities=%d actions=%d err=%v", len(activities), len(report.InvestigationActions), err)
	}
}

func TestSourceInvestigationFollowsGoroutineOwnershipAndShutdownWait(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"race.go": `package race

import "sync"

type C struct {
	stopWg sync.WaitGroup
	stopCh chan struct{}
}






func New(c *C) {
	go c.startBackgroundResync()
}

func (c *C) startBackgroundResync() {
	c.stopWg.Add(1)
	go func() {
		defer c.stopWg.Done()
		<-c.stopCh
	}()
}

func (c *C) Stop() {
	close(c.stopCh)
	c.stopWg.Wait()
}
`,
		"setup.go": `package race

func Teardown(c *C) {
	c.Stop()
}
`,
	}
	commit(t, dir, "goroutine ownership source", files)
	lineOf := func(name, needle string) int {
		for index, line := range strings.Split(files[name], "\n") {
			if strings.Contains(line, needle) {
				return index + 1
			}
		}
		t.Fatalf("%s not found in %s", needle, name)
		return 0
	}
	store, err := persistence.Open(filepath.Join(t.TempDir(), "ownership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveInvestigation(domain.Investigation{ID: "ownership", RepoPath: dir}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (programrepo.Analyzer{}).Analyze(context.Background(), dir, "local:test", "revision:test", "head")
	if err != nil {
		t.Fatal(err)
	}
	report := &Report{
		InvestigationID:  "ownership",
		Environment:      Environment{HeadCommit: "head"},
		FailureIncidents: []FailureIncident{{Category: "go_data_race"}},
		RaceReports: []RaceReport{{
			ReadStack:  []localization.Frame{{File: "race.go", Line: lineOf("race.go", "go c.startBackgroundResync")}},
			WriteStack: []localization.Frame{{File: "setup.go", Line: lineOf("setup.go", "c.Stop()")}},
		}},
	}
	d := &Diagnoser{cfg: Config{RepoPath: dir, Store: store}}
	if err := d.runSourceInvestigation(context.Background(), report, dir, snapshot, nil, true); err != nil {
		t.Fatal(err)
	}
	if report.Concurrency == nil || len(report.Concurrency.SharedState) != 0 {
		t.Fatalf("wrapper/call-site overlap was incorrectly treated as shared memory: %#v", report.Concurrency)
	}
	if !containsSourceToken(report.Concurrency.Synchronization, "Add") || !containsSourceToken(report.Concurrency.Synchronization, "Wait") {
		t.Fatalf("WaitGroup operations were not reconstructed: %#v", report.Concurrency)
	}
	for _, missing := range report.Concurrency.MissingOrdering {
		if strings.Contains(missing, "Add/Wait") {
			return
		}
	}
	t.Fatalf("shutdown ordering was overstated: %#v", report.Concurrency)
}

func TestSourceInvestigationDoesNotResolveWrapperFramesToAccesses(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{
		"cacher.go": `package race

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
		"setup.go": `package race

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
	commit(t, dir, "wrapper frame source", files)
	lineOf := func(name, needle string) int {
		for index, line := range strings.Split(files[name], "\n") {
			if strings.Contains(line, needle) {
				return index + 1
			}
		}
		t.Fatalf("%s not found in %s", needle, name)
		return 0
	}
	store, err := persistence.Open(filepath.Join(t.TempDir(), "wrapper.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveInvestigation(domain.Investigation{ID: "wrapper-frame", RepoPath: dir}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (programrepo.Analyzer{}).Analyze(context.Background(), dir, "local:test", "revision:test", "head")
	if err != nil {
		t.Fatal(err)
	}
	report := &Report{
		InvestigationID:  "wrapper-frame",
		Environment:      Environment{HeadCommit: "head"},
		FailureIncidents: []FailureIncident{{Category: "go_data_race"}},
		RaceReports: []RaceReport{{
			ReadStack:      []localization.Frame{{File: "cacher.go", Line: lineOf("cacher.go", "if featureEnabled"), Function: "race.NewCacherFromConfig.gowrap3()"}},
			ReadFrameRole:  "goroutine_creation_or_wrapper",
			WriteStack:     []localization.Frame{{File: "setup.go", Line: lineOf("setup.go", "c.rawCacher.Stop()"), Function: "race.SetupWithCacher.func1()"}},
			WriteFrameRole: "goroutine_creation_or_wrapper",
		}},
	}
	d := &Diagnoser{cfg: Config{RepoPath: dir, Store: store}}
	if err := d.runSourceInvestigation(context.Background(), report, dir, snapshot, nil, true); err != nil {
		t.Fatal(err)
	}
	if report.Concurrency == nil || len(report.Concurrency.Accesses) != 2 {
		t.Fatalf("wrapper frames were not inspected: %#v", report.Concurrency)
	}
	for _, access := range report.Concurrency.Accesses {
		if access.Expression != "" || access.ExactMemory || len(access.StateIdentifiers) != 0 {
			t.Fatalf("wrapper frame was resolved to a memory access: %#v", access)
		}
	}
	for _, evidence := range report.Evidence {
		switch evidence.Kind {
		case "source variable trace", "source references":
			t.Fatalf("unresolved access produced identifier evidence: %#v", evidence)
		case "source function":
			if !sourceDetailUnresolved(evidence.Detail) {
				t.Fatalf("wrapper frame was presented as a resolved access: %#v", evidence)
			}
		}
	}
	if len(report.Hypotheses) != 0 {
		t.Fatalf("wrapper frames produced a shared-memory hypothesis: %#v", report.Hypotheses)
	}
	if report.InvestigationObjective == nil {
		t.Fatal("investigation objective was not recorded")
	}
	for _, unknown := range report.InvestigationObjective.Unknowns {
		if unknown.ID == "exact-memory" && unknown.Status == "resolved" {
			t.Fatalf("exact memory was resolved from wrapper frames: %#v", unknown)
		}
	}
}
