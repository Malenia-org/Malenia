package diagnosis

import "testing"

func strictUnknown(reqs ...EvidenceRequirement) InvestigationUnknown {
	return InvestigationUnknown{ID: "shutdown-ordering", Requirements: reqs, Status: "unresolved"}
}

func TestStrictObjectiveRejectsUnrelatedRelationshipEvidence(t *testing.T) {
	u := strictUnknown(EvidenceRequirement{ClaimKind: "synchronization-ordering", Subject: "pkg.Cacher.stopWg", MinimumBasis: "SourceVerified", Ordering: "happens-before"})
	got := evaluateStrictObjective(u, []EvidenceItem{{ID: "rel", Kind: "source_code", ClaimKind: "source_code", Basis: "SourceVerified", DeclarationID: "watch_cache_test.go:immediateTicker.Stop"}}, "head")
	if got.Resolved || len(got.Missing) != 1 {
		t.Fatalf("unrelated evidence resolved objective: %+v", got)
	}
}

func TestStrictObjectiveRejectsInferredAndWrongSubjectEvidence(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "synchronization-ordering", Subject: "pkg.Cacher.stopWg", MinimumBasis: "SourceVerified", Ordering: "happens-before"}
	for _, ev := range []EvidenceItem{
		{ID: "inferred", ClaimKind: req.ClaimKind, Basis: "Inferred", DeclarationID: req.Subject, Ordering: req.Ordering},
		{ID: "wrong-subject", ClaimKind: req.ClaimKind, Basis: "SourceVerified", DeclarationID: "other.Cacher.stopWg", Ordering: req.Ordering},
	} {
		if result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{ev}, "head"); result.Resolved {
			t.Fatalf("invalid evidence resolved objective: %+v", result)
		}
	}
}

func TestStrictObjectiveRequiresRuntimeInstanceIdentity(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "same-runtime-instance", MinimumBasis: "RuntimeObserved", SameInstance: true}
	result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{{ID: "decl", ClaimKind: req.ClaimKind, Basis: "SourceVerified", DeclarationID: "pkg.Cacher.stopWg"}}, "head")
	if result.Resolved {
		t.Fatal("declaration identity satisfied runtime instance requirement")
	}
	result = evaluateStrictObjective(strictUnknown(req), []EvidenceItem{{ID: "instance", ClaimKind: req.ClaimKind, Basis: BasisRuntimeMapped, InstanceID: "instance-1", StaticMappingStatus: string(MappingProven), RelatedAccessIDs: []string{"read-1", "write-1"}}}, "head")
	if !result.Resolved {
		t.Fatalf("runtime instance evidence did not satisfy requirement: %+v", result)
	}
}

func TestSameAddressObservationCannotSatisfySameInstance(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "same-runtime-instance", MinimumBasis: BasisRuntimeMapped, SameInstance: true}
	result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{{ID: "overlap", ClaimKind: req.ClaimKind, Basis: BasisObserved, RuntimeAddress: "0x1", RelatedAccessIDs: []string{"read-1", "write-1"}}}, "head")
	if result.Resolved {
		t.Fatal("same-address observation satisfied static object identity")
	}
}

func TestDifferentObjectsOfSameTypeCannotBeCombined(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "same-runtime-instance", MinimumBasis: BasisRuntimeMapped, SameInstance: true}
	items := []EvidenceItem{
		{ID: "read-object", ClaimKind: req.ClaimKind, Basis: BasisRuntimeMapped, InstanceID: "obj-1", StaticMappingStatus: string(MappingProven), RelatedAccessIDs: []string{"read-1"}},
		{ID: "write-object", ClaimKind: req.ClaimKind, Basis: BasisRuntimeMapped, InstanceID: "obj-2", StaticMappingStatus: string(MappingProven), RelatedAccessIDs: []string{"write-1"}},
	}
	if result := evaluateStrictObjective(strictUnknown(req), items, "head"); result.Resolved {
		t.Fatal("different object identities were combined into a same-instance proof")
	}
}

func TestOrderingRequiresTypedEdgeBetweenBothAccesses(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "synchronization-ordering", MinimumBasis: BasisDataflowVerified, Ordering: "happens-before", PredecessorAccessID: "read-1", SuccessorAccessID: "write-1"}
	base := EvidenceItem{ID: "edge", ClaimKind: req.ClaimKind, Basis: BasisDataflowVerified, Ordering: req.Ordering, SynchronizationMechanism: "channel_close_recv", MemoryModel: "go", RelatedAccessIDs: []string{"read-1", "write-1"}}
	for _, item := range []EvidenceItem{
		base,
		{ID: "wrong", ClaimKind: req.ClaimKind, Basis: BasisDataflowVerified, Ordering: req.Ordering, SynchronizationMechanism: "channel_close_recv", MemoryModel: "go", RelatedAccessIDs: []string{"read-1", "write-1"}, PredecessorAccessID: "other", SuccessorAccessID: "write-1"},
	} {
		if result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{item}, "head"); result.Resolved {
			t.Fatalf("incomplete ordering edge resolved objective: %+v", result)
		}
	}
	valid := base
	valid.PredecessorAccessID = "read-1"
	valid.SuccessorAccessID = "write-1"
	if result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{valid}, "head"); !result.Resolved {
		t.Fatalf("typed edge did not resolve objective: %+v", result)
	}
}

func TestDescriptionCannotSatisfyTypedOrdering(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "synchronization-ordering", MinimumBasis: BasisDataflowVerified, Ordering: "happens-before"}
	item := EvidenceItem{ID: "prose", ClaimKind: req.ClaimKind, Basis: BasisTextualDiscovery, Detail: "verified happens-before: ordering established"}
	if result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{item}, "head"); result.Resolved {
		t.Fatal("description text satisfied typed ordering requirement")
	}
}

func TestRegexVariableDiscoveryCannotSatisfyDataflow(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "source variable trace", MinimumBasis: BasisDataflowVerified}
	item := EvidenceItem{ID: "regex", ClaimKind: req.ClaimKind, Basis: BasisTextualDiscovery, Detail: "shared stopCh appears on lines 10, 20, and 30"}
	if result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{item}, "head"); result.Resolved {
		t.Fatal("regex-discovered identifiers satisfied a dataflow requirement")
	}
}

func TestRaceObservationCannotSatisfySynchronizationRequirement(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "synchronization-ordering", MinimumBasis: "SourceVerified", Ordering: "happens-before"}
	result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{{ID: "race", ClaimKind: "RuntimeObservedRace", Basis: "RuntimeObserved", RunID: "run-1"}}, "head")
	if result.Resolved {
		t.Fatal("race observation substituted for ordering proof")
	}
}

func TestContradictoryEvidenceDoesNotResolvePositiveObjective(t *testing.T) {
	req := EvidenceRequirement{ClaimKind: "synchronization-ordering", MinimumBasis: "SourceVerified", Ordering: "happens-before"}
	result := evaluateStrictObjective(strictUnknown(req), []EvidenceItem{{ID: "contradiction", ClaimKind: "synchronization-absent", Basis: "SourceVerified"}}, "head")
	if result.Resolved || len(result.Missing) == 0 {
		t.Fatalf("contradictory evidence resolved positive objective: %+v", result)
	}
}

func TestLegacyEvidenceDoesNotSatisfyStrictObjective(t *testing.T) {
	u := strictUnknown(EvidenceRequirement{ClaimKind: "synchronization-ordering", MinimumBasis: "SourceVerified"})
	result := evaluateStrictObjective(u, []EvidenceItem{{ID: "legacy", Kind: "source synchronization", Detail: "WaitGroup Add and Wait"}}, "head")
	if result.Resolved {
		t.Fatal("legacy evidence satisfied new strict requirement")
	}
}
