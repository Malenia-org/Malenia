package diagnosis

import (
	"strings"
	"testing"
)

func TestObjectivePredicateRequiresEvidenceAndRevision(t *testing.T) {
	report := &Report{Environment: Environment{HeadCommit: "head"}, InvestigationObjective: &InvestigationObjective{Unknowns: []InvestigationUnknown{{
		ID: "happens-before", Question: "ordering", Predicate: "verified edge", RequiredKinds: []string{"source synchronization"}, Status: "unresolved", Revision: "head",
	}}}}
	report.Evidence = []EvidenceItem{{ID: "sync", Kind: "source synchronization", Detail: "WaitGroup Add and Wait were found; this is not proof of ordering", Revision: "head"}}
	evaluateInvestigationObjectives(report)
	if report.InvestigationObjective.Unknowns[0].Status == "resolved" {
		t.Fatal("primitive discovery incorrectly resolved synchronization objective")
	}
	report.InvestigationObjective.Unknowns[0].Requirements = []EvidenceRequirement{{ClaimKind: "synchronization-ordering", MinimumBasis: BasisDataflowVerified, Ordering: "happens-before"}}
	report.Evidence[0] = EvidenceItem{ID: "sync", ClaimKind: "synchronization-ordering", Basis: BasisDataflowVerified, Ordering: "happens-before", PredecessorAccessID: "read-1", SuccessorAccessID: "write-1", RelatedAccessIDs: []string{"read-1", "write-1"}, SynchronizationMechanism: "mutex_critical_section", MemoryModel: "go", Revision: "head"}
	evaluateInvestigationObjectives(report)
	if report.InvestigationObjective.Unknowns[0].Status != "resolved" {
		t.Fatalf("verified predicate was not resolved: %#v", report.InvestigationObjective.Unknowns[0])
	}
	report.InvestigationObjective.Unknowns[0].Status = "unresolved"
	report.InvestigationObjective.Unknowns[0].Revision = "other"
	evaluateInvestigationObjectives(report)
	if report.InvestigationObjective.Unknowns[0].Status == "resolved" || !strings.Contains(report.InvestigationObjective.Unknowns[0].Evaluation, "different source revision") {
		t.Fatalf("stale source evidence was accepted: %#v", report.InvestigationObjective.Unknowns[0])
	}
}

func TestExactMemoryDoesNotResolveFromIdentifierOverlap(t *testing.T) {
	report := &Report{Environment: Environment{HeadCommit: "head"}, InvestigationObjective: &InvestigationObjective{Unknowns: []InvestigationUnknown{{
		ID: "exact-memory", Question: "memory", Predicate: "same concrete object", RequiredKinds: []string{"source function", "source variable trace", "source references"}, Status: "unresolved", Revision: "head",
	}}}, Concurrency: &ConcurrencyModel{Accesses: []ConcurrencyAccess{{Expression: "go cacher.startBackgroundResync()", FrameRole: "goroutine_creation_or_wrapper", ExactMemory: false}, {Expression: "rawCacher.Stop()", FrameRole: "goroutine_creation_or_wrapper", ExactMemory: false}}}}
	report.Evidence = []EvidenceItem{
		{ID: "function", Kind: "source function", Detail: "enclosing function", Revision: "head"},
		{ID: "trace", Kind: "source variable trace", Detail: "cacher Stop startBackgroundResync", Revision: "head"},
		{ID: "refs", Kind: "source references", Detail: "cacher Stop startBackgroundResync", Revision: "head"},
	}
	evaluateInvestigationObjectives(report)
	if report.InvestigationObjective.Unknowns[0].Status == "resolved" {
		t.Fatal("identifier overlap and complete evidence categories incorrectly resolved exact memory")
	}
}
