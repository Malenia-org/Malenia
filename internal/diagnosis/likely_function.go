package diagnosis

import (
	"strings"

	"github.com/Malenia-org/ceaser/internal/domain"
	programrepo "github.com/Malenia-org/ceaser/internal/repository"
)

// LikelyFunction is the developer handoff tier. It names the function most
// likely to explain the failed expectation and the alternates worth checking.
// It is deliberately not a root cause or a verified mechanism.
type LikelyFunction struct {
	Status      string               `json:"status"`
	Reason      string               `json:"reason,omitempty"`
	Primary     *LikelyFunctionLead  `json:"primary,omitempty"`
	Alternates  []LikelyFunctionLead `json:"alternates,omitempty"`
	Limitations []string             `json:"limitations,omitempty"`
}

// LikelyFunctionLead is a ranked suspect with the reason and the evidence IDs
// that justify the ranking. The kind records whether the location was observed
// in execution evidence or inferred from source and static relationships.
type LikelyFunctionLead struct {
	File        string   `json:"file"`
	Function    string   `json:"function,omitempty"`
	Line        int      `json:"line,omitempty"`
	Kind        string   `json:"kind"`
	Reason      string   `json:"reason"`
	Confidence  string   `json:"confidence"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

// BuildLikelyFunction derives the shortlist from the candidate tier. It does
// not add new analysis, change promotion, or claim verification.
func BuildLikelyFunction(report Report) LikelyFunction {
	result := LikelyFunction{Status: "unavailable"}
	shortlist := report.CandidateReport
	if shortlist == nil {
		built := BuildCandidateReport(report)
		shortlist = &built
	}
	leads := []LikelyFunctionLead{}
	rank := func(e CandidateReportEntry) int {
		switch e.Kind {
		case "probe_operation":
			return 0
		case "inferred_source":
			if strings.Contains(e.Reason, "SSA value") {
				return 0
			}
			return 2
		case "inferred_call":
			return 1
		case "observed_stack":
			return 3
		default:
			return 4
		}
	}
	ordered := append([]CandidateReportEntry(nil), shortlist.Entries...)
	if report.SSAAssertionTrace != nil {
		best := map[string]programrepo.SSAValue{}
		valueRank := func(v programrepo.SSAValue) int {
			switch v.Kind {
			case "*ssa.Convert", "*ssa.BinOp", "*ssa.Call", "*ssa.Lookup", "*ssa.TypeAssert":
				return 3
			case "*ssa.Return":
				return 2
			default:
				return 1
			}
		}
		for _, value := range report.SSAAssertionTrace.Values {
			if value.Line <= 0 || strings.HasSuffix(value.File, "_test.go") || value.Function == "" {
				continue
			}
			if shortSSAFunctionName(value.Function) == "" {
				continue
			}
			key := value.File + "|" + value.Function
			if current, ok := best[key]; !ok || valueRank(value) > valueRank(current) {
				best[key] = value
			}
		}
		for _, value := range best {
			name := shortSSAFunctionName(value.Function)
			ids := []string{}
			for _, item := range report.Evidence {
				if strings.Contains(item.Detail, value.ID) || strings.Contains(item.Detail, value.Text) {
					ids = append(ids, item.ID)
				}
			}
			if len(ids) == 0 {
				ids = boundedEvidenceIDs([]string{"ssa:" + value.ID}, 1)
			}
			ordered = append(ordered, CandidateReportEntry{
				File:        value.File,
				Function:    name,
				Line:        value.Line,
				Kind:        "inferred_source",
				Reason:      "function contains an SSA value in the failed assertion dependency trace",
				EvidenceIDs: ids,
			})
		}
	}
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && rank(ordered[j]) < rank(ordered[j-1]); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	for _, entry := range ordered {
		if !likelyFunctionEntry(entry) {
			continue
		}
		leads = append(leads, LikelyFunctionLead{
			File:        entry.File,
			Function:    entry.Function,
			Line:        entry.Line,
			Kind:        entry.Kind,
			Reason:      entry.Reason,
			Confidence:  leadConfidence(entry),
			EvidenceIDs: boundedEvidenceIDs(entry.EvidenceIDs, 3),
		})
		if len(leads) == 4 {
			break
		}
	}
	if len(leads) == 0 {
		result.Reason = shortlist.Reason
		if result.Reason == "" {
			result.Reason = "no function-level lead was available"
		}
		result.Limitations = append(result.Limitations,
			"file-level localization alone is not sufficient to name a likely function")
		return result
	}
	result.Status = "available"
	primary := leads[0]
	result.Primary = &primary
	if len(leads) > 1 {
		result.Alternates = leads[1:]
	}
	result.Limitations = append(result.Limitations,
		"ranked suspects are investigation leads, not verified root causes")
	for _, limitation := range shortlist.Limitations {
		result.Limitations = append(result.Limitations, limitation)
	}
	return result
}

func leadConfidence(entry CandidateReportEntry) string {
	switch entry.Kind {
	case "observed_stack", "probe_operation":
		return "medium"
	case "inferred_call":
		if strings.Contains(entry.Reason, "resolved") || strings.Contains(entry.Reason, "value") {
			return "medium"
		}
		return "low"
	default:
		return "low"
	}
}

func likelyFunctionEntry(entry CandidateReportEntry) bool {
	file := strings.TrimSpace(entry.File)
	if file == "" || strings.HasSuffix(file, "_test.go") || strings.Contains(file, "/testdata/") {
		return false
	}
	if strings.TrimSpace(entry.Function) == "" {
		return false
	}
	switch entry.Kind {
	case "observed_stack", "inferred_call", "inferred_source", "probe_operation":
		return true
	default:
		return false
	}
}

// candidateFunctionFromLocation is used by tests and callers that need a
// function label from a CodeLocation without materializing the full tier.
func candidateFunctionFromLocation(loc domain.CodeLocation) string {
	return candidateFunction(loc.SymbolIdentity)
}
