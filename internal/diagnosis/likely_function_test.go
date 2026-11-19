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
	if got.Primary.File != "pkg/handler/patch.go" || got.Primary.Function != "writeChunk" {
		t.Fatalf("observed stack should be the primary lead: %#v", got.Primary)
	}
	if got.Primary.Kind != "observed_stack" {
		t.Fatalf("observed provenance lost: %#v", got.Primary)
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
	report.CandidateReport = nil
	got = BuildLikelyFunction(Report{SSAAssertionTrace: report.SSAAssertionTrace})
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

// operandCorrelationFixture builds a report whose failed assertion takes a
// non-test function's return as an operand, so the function lies on the
// assertion's operand dependency path but not in any observed stack.
func operandCorrelationFixture() Report {
	shortlist := CandidateReport{Status: "available", Entries: []CandidateReportEntry{
		{File: "pkg/handler/patch.go", Function: "writeChunk", Line: 41, Kind: "observed_stack",
			Reason: "location observed in the failure evidence", EvidenceIDs: []string{"E-stack"}},
		{File: "mcp/tools.go", Function: "GetInt", Line: 255, Kind: "inferred_call",
			Reason: "candidate inferred from a resolved call relationship", EvidenceIDs: []string{"E-call"}},
	}}
	trace := &programrepo.SSATrace{
		Status:   "traced",
		AnchorID: "assert",
		Values: []programrepo.SSAValue{
			{ID: "assert", Kind: "*ssa.Call", File: "pkg/x_test.go", Function: "pkg.TestX",
				Callee: "github.com/stretchr/testify/assert.Equal", Arguments: []string{"t", "want", "got"}},
			{ID: "t", Kind: "*ssa.MakeInterface", File: "pkg/x_test.go", Function: "pkg.TestX", Operands: []string{"tp"}},
			{ID: "want", Kind: "*ssa.Const", File: "pkg/x_test.go", Function: "pkg.TestX"},
			{ID: "got", Kind: "*ssa.Call", File: "pkg/x_test.go", Function: "pkg.TestX", Operands: []string{"call"}},
			{ID: "call", Kind: "*ssa.Call", File: "core/provider.go", Line: 88,
				Function: "example.com/pkg.Provider.Compute", Callee: "example.com/pkg.Compute"},
		},
	}
	return Report{CandidateReport: &shortlist, SSAAssertionTrace: trace}
}

func TestLikelyFunctionPromotesOperandCorrelatedSSA(t *testing.T) {
	got := BuildLikelyFunction(operandCorrelationFixture())
	if got.Primary == nil {
		t.Fatalf("no primary: %#v", got)
	}
	if got.Primary.File != "core/provider.go" || got.Primary.Function != "Compute" {
		t.Fatalf("operand-correlated function not promoted: %#v", got.Primary)
	}
	if got.Primary.Kind != "inferred_source" {
		t.Fatalf("promotion changed the provenance kind: %#v", got.Primary)
	}
	if !strings.Contains(got.Primary.Reason, "operand dependency path") {
		t.Fatalf("operand rationale missing: %#v", got.Primary)
	}
	if got.Primary.Confidence != "low" {
		t.Fatalf("correlation must stay a low-confidence lead: %#v", got.Primary)
	}
	if len(got.Primary.EvidenceIDs) == 0 {
		t.Fatalf("promoted lead lost its evidence: %#v", got.Primary)
	}
	// The observed stack frame must be ordered behind the operand lead.
	if got.Alternates == nil || got.Alternates[0].Kind != "observed_stack" {
		t.Fatalf("observed frame not demoted behind the operand lead: %#v", got.Alternates)
	}
}

func TestLikelyFunctionFiltersFrameworkStackFrames(t *testing.T) {
	report := Report{
		FailureIncidents: []FailureIncident{
			{ID: "runtime", EvidenceIDs: []string{"E-runtime"}, ObservedLocations: []IncidentLocation{
				{File: "/usr/local/go/src/runtime/panic.go", Line: 884, Function: "runtime.gopanic", Provenance: "direct_stack_frame"},
			}},
			{ID: "app", EvidenceIDs: []string{"E-app"}, ObservedLocations: []IncidentLocation{
				{File: "pkg/handler/context.go", Line: 65, Function: "(*handler.H).newContext", Provenance: "direct_stack_frame"},
			}},
		},
	}
	shortlist := BuildCandidateReport(report)
	report.CandidateReport = &shortlist
	got := BuildLikelyFunction(report)
	if got.Primary == nil {
		t.Fatalf("no primary: %#v", got)
	}
	if got.Primary.Function == "runtime.gopanic" || strings.Contains(got.Primary.File, "/runtime/") {
		t.Fatalf("framework frame outranked application code: %#v", got.Primary)
	}
	if !strings.Contains(got.Primary.File, "context.go") {
		t.Fatalf("application frame not promoted: %#v", got.Primary)
	}
}

func TestOperandCorrelationIgnoresUnreachableValues(t *testing.T) {
	report := operandCorrelationFixture()
	// Add a non-test function that is NOT reachable from the assertion operands.
	report.SSAAssertionTrace.Values = append(report.SSAAssertionTrace.Values, programrepo.SSAValue{
		ID: "unrelated", Kind: "*ssa.Call", File: "side/unrelated.go", Line: 10,
		Function: "example.com/pkg.Unrelated", Callee: "example.com/pkg.UnrelatedFunc",
	})
	got := BuildLikelyFunction(report)
	if got.Primary == nil || got.Primary.File != "core/provider.go" {
		t.Fatalf("unreachable value altered the primary: %#v", got.Primary)
	}
	for _, lead := range append([]LikelyFunctionLead{*got.Primary}, got.Alternates...) {
		if lead.File == "side/unrelated.go" {
			t.Fatalf("unreachable function was admitted as a lead: %#v", lead)
		}
	}
}

func TestCandidateFunctionFromLocation(t *testing.T) {
	loc := domain.CodeLocation{SymbolIdentity: "go:mcp:Function:CallToolRequest.GetInt"}
	if got := candidateFunctionFromLocation(loc); got != "CallToolRequest.GetInt" {
		t.Fatalf("wrong function label: %q", got)
	}
}
