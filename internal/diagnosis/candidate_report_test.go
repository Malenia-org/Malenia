package diagnosis

import (
	"strings"
	"testing"

	"github.com/causent/malenia/internal/domain"
)

func candidateReportFixture() Report {
	return Report{
		FailureIncidents: []FailureIncident{{
			ID: "incident",
			ObservedLocations: []IncidentLocation{{
				File: "pkg/handler/patch.go", Line: 41, Function: "writeChunk",
				Provenance: "direct_stack_frame",
			}},
			EvidenceIDs: []string{"E-stack"},
		}},
		Localization: Localization{LikelyFiles: []string{"pkg/handler/context.go", "pkg/handler/unrouted.go"}},
		Evidence:     []EvidenceItem{{ID: "E-source", Source: "pkg/handler/context.go", Kind: "source_excerpt"}},
		Candidates: []domain.CandidateLocation{
			{Location: domain.CodeLocation{
				FilePath: "mcp/tools.go", SymbolIdentity: "go:pkg:Function:CallToolRequest.GetInt",
				StartLine: 255, LocationType: "Function", Provenance: "verified_call",
			}, SupportingEvidenceIDs: []domain.ID{"E-call", "E-ssa"}},
		},
	}
}

func TestCandidateReportSeparatesObservedFromInferred(t *testing.T) {
	got := BuildCandidateReport(candidateReportFixture())
	if got.Status != "available" || got.ProbeStatus != "disabled" || len(got.Entries) < 3 {
		t.Fatalf("shortlist not available: %#v", got)
	}
	kinds := map[string]CandidateReportEntry{}
	for _, entry := range got.Entries {
		kinds[entry.Kind] = entry
		if entry.Rank == 0 || entry.File == "" || entry.Reason == "" {
			t.Fatalf("incomplete entry: %#v", entry)
		}
	}
	observed, ok := kinds["observed_stack"]
	if !ok || observed.Line != 41 || observed.Function != "writeChunk" || !containsString(observed.EvidenceIDs, "E-stack") {
		t.Fatalf("observed stack location lost: %#v", observed)
	}
	inferred, ok := kinds["inferred_call"]
	if !ok || inferred.File != "mcp/tools.go" || inferred.Line != 255 || inferred.Function != "CallToolRequest.GetInt" {
		t.Fatalf("inferred call entry lost: %#v", inferred)
	}
	if !containsString(inferred.EvidenceIDs, "E-call") || !containsString(inferred.EvidenceIDs, "E-ssa") {
		t.Fatalf("supporting evidence not referenced: %#v", inferred)
	}
	var localized *CandidateReportEntry
	for i := range got.Entries {
		if got.Entries[i].File == "pkg/handler/context.go" {
			localized = &got.Entries[i]
		}
	}
	if localized == nil || !containsString(localized.EvidenceIDs, "E-source") {
		t.Fatalf("localized fallback lost evidence reference: %#v", localized)
	}
}

func TestCandidateReportUncorrelatedProbeStaysALead(t *testing.T) {
	report := candidateReportFixture()
	report.MechanismObservation = &RuntimeMechanismObservation{
		Status:         "observed",
		OperationFile:  "mcp/tools.go",
		OperationLine:  255,
		OperationName:  "CallToolRequest.GetInt",
		SignatureMatch: true,
		// Captured values are not bound to the failing subtest.
		Correlation: &AssertionCorrelation{Verified: false, Test: "TestCallToolRequest_IntegerOutOfRange"},
		Limitations: []string{"values not correlated"},
	}
	got := BuildCandidateReport(report)
	var probe *CandidateReportEntry
	for i := range got.Entries {
		if got.Entries[i].Kind == "probe_operation" {
			probe = &got.Entries[i]
		}
	}
	if probe == nil || probe.Line != 255 || !strings.Contains(probe.Reason, "not correlated") {
		t.Fatalf("uncorrelated probe not presented as a lead: %#v", got)
	}
	if got.ProbeStatus != "observed" || len(got.Limitations) == 0 {
		t.Fatalf("probe limitations missing: %#v", got)
	}
	// The candidate tier is presentation only: promotion requirements unchanged.
	report.FailureIncidents[0].EvidenceIDs = append(report.FailureIncidents[0].EvidenceIDs, got.Entries[0].EvidenceIDs...)
	if status := assessRootCause(report).Status; status == "established" || status == "confirmed" {
		t.Fatalf("candidate tier promoted root cause: %s", status)
	}
}

func TestCandidateReportFallbackWhenProbingStops(t *testing.T) {
	report := Report{
		Localization:         Localization{LikelyFiles: []string{"a.go", "b.go"}},
		MechanismObservation: &RuntimeMechanismObservation{Status: "unavailable"},
	}
	got := BuildCandidateReport(report)
	if got.Status != "available" || got.ProbeStatus != "unavailable" || len(got.Entries) != 2 {
		t.Fatalf("shortlist should survive unavailable probe: %#v", got)
	}
	for _, entry := range got.Entries {
		if entry.Kind != "inferred_source" || entry.File == "" {
			t.Fatalf("unexpected fallback entry: %#v", entry)
		}
	}
	empty := BuildCandidateReport(Report{})
	if empty.Status != "unavailable" || empty.Reason == "" || len(empty.Entries) != 0 {
		t.Fatalf("empty report should be typed unavailable: %#v", empty)
	}
}

func TestCandidateReportBoundedAndDeduplicated(t *testing.T) {
	report := Report{}
	for i := 0; i < 9; i++ {
		report.Candidates = append(report.Candidates, domain.CandidateLocation{
			Location: domain.CodeLocation{FilePath: "pkg/f.go", StartLine: i + 1, LocationType: "Function", Provenance: "reference"},
		})
	}
	report.Candidates = append(report.Candidates, domain.CandidateLocation{
		Location: domain.CodeLocation{FilePath: "pkg/f.go", StartLine: 1, LocationType: "Function", Provenance: "reference"},
	})
	got := BuildCandidateReport(report)
	if len(got.Entries) != candidateReportLimit {
		t.Fatalf("shortlist not bounded: %#v", got.Entries)
	}
	for i, entry := range got.Entries {
		if entry.Rank != i+1 {
			t.Fatalf("ranks not sequential: %#v", got.Entries)
		}
	}
	if len(boundedEvidenceIDs([]string{"a", "a", "b", "c", "d"}, 3)) != 3 {
		t.Fatal("evidence ids not deduplicated and bounded")
	}
}
