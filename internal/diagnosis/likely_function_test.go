package diagnosis

import (
	"strings"
	"testing"

	"github.com/Malenia-org/ceaser/internal/domain"
	programrepo "github.com/Malenia-org/ceaser/internal/repository"
)

func TestLikelyFunctionPrefersImplementationLeads(t *testing.T) {
	shortlist := BuildCandidateReport(candidateReportFixture())
	report := Report{CandidateReport: &shortlist}
	got := BuildLikelyFunction(report)
	if got.Status != "available" || got.Primary == nil {
		t.Fatalf("no likely function: %#v", got)
	}
	if got.Primary.File != "mcp/tools.go" || got.Primary.Function != "CallToolRequest.GetInt" {
		t.Fatalf("wrong primary lead: %#v", got.Primary)
	}
	if got.Primary.Kind != "inferred_call" || !strings.Contains(got.Primary.Reason, "call") {
		t.Fatalf("lead reason missing: %#v", got.Primary)
	}
	if len(got.Alternates) == 0 {
		t.Fatalf("expected alternates: %#v", got)
	}
	for _, lead := range append([]LikelyFunctionLead{*got.Primary}, got.Alternates...) {
		if strings.HasSuffix(lead.File, "_test.go") || lead.Function == "" {
			t.Fatalf("test or file-only lead admitted: %#v", lead)
		}
	}
	if !strings.Contains(strings.Join(got.Limitations, " "), "not verified") {
		t.Fatalf("lead is not marked as unverified: %#v", got)
	}
}

func TestLikelyFunctionPrefersSSADependencyEvidence(t *testing.T) {
	shortlist := BuildCandidateReport(candidateReportFixture())
	report := Report{
		CandidateReport: &shortlist,
		SSAAssertionTrace: &programrepo.SSATrace{Status: "traced", Values: []programrepo.SSAValue{
			{ID: "return-default", Kind: "*ssa.Return", File: "mcp/tools.go", Line: 262, Function: "example.com/mcp.CallToolRequest.GetInt", Text: "return defaultValue"},
		}},
	}
	got := BuildLikelyFunction(report)
	if got.Primary == nil || got.Primary.Function != "GetInt" || got.Primary.Confidence != "low" {
		t.Fatalf("SSA lead not preferred: %#v", got.Primary)
	}
	if len(got.Primary.EvidenceIDs) == 0 || !strings.HasPrefix(got.Primary.EvidenceIDs[0], "ssa:") {
		t.Fatalf("SSA evidence not retained: %#v", got.Primary)
	}
}

func TestLikelyFunctionWorksWithoutProbe(t *testing.T) {
	report := candidateReportFixture()
	report.MechanismObservation = &RuntimeMechanismObservation{Status: "unavailable"}
	report.CandidateReport = func() *CandidateReport {
		r := BuildCandidateReport(report)
		return &r
	}()
	got := BuildLikelyFunction(report)
	if got.Status != "available" || got.Primary == nil || got.Primary.Function == "" {
		t.Fatalf("likely function unavailable when probe stopped: %#v", got)
	}
}

func TestLikelyFunctionDoesNotPromoteRootCause(t *testing.T) {
	report := candidateReportFixture()
	report.MechanismObservation = &RuntimeMechanismObservation{
		Status:        "observed",
		OperationFile: "mcp/tools.go",
		OperationLine: 255,
		OperationName: "CallToolRequest.GetInt",
		Correlation:   &AssertionCorrelation{Verified: false},
	}
	shortlist := BuildCandidateReport(report)
	report.CandidateReport = &shortlist
	likely := BuildLikelyFunction(report)
	report.LikelyFunction = &likely
	if status := assessRootCause(report).Status; status == "established" || status == "confirmed" {
		t.Fatalf("likely tier promoted root cause: %s", status)
	}
}

func TestLikelyFunctionUnavailableWhenOnlyFileLeads(t *testing.T) {
	report := Report{
		Localization: Localization{LikelyFiles: []string{"pkg/a.go", "pkg/b.go"}},
		Evidence:     []EvidenceItem{{ID: "e1", Source: "pkg/a.go", Kind: "source_excerpt"}},
	}
	got := BuildLikelyFunction(report)
	if got.Status != "unavailable" || got.Primary != nil || len(got.Alternates) != 0 {
		t.Fatalf("file-only report produced a function lead: %#v", got)
	}
	if got.Reason == "" || len(got.Limitations) == 0 {
		t.Fatalf("unavailable tier missing typed reason: %#v", got)
	}
}

func TestCandidateFunctionFromLocation(t *testing.T) {
	loc := domain.CodeLocation{SymbolIdentity: "go:mcp:Function:CallToolRequest.GetInt"}
	if got := candidateFunctionFromLocation(loc); got != "CallToolRequest.GetInt" {
		t.Fatalf("wrong function label: %q", got)
	}
}
