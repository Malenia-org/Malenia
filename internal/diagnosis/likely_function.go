package diagnosis

import (
	"sort"
	"strings"

	"github.com/Malenia-org/ceaser/internal/domain"
	programrepo "github.com/Malenia-org/ceaser/internal/repository"
)

// operandCorrelationReason labels a lead found on the failed assertion's
// operand dependency path. Correlation is not causation: the lead stays a
// suspect with low confidence and does not change verification or promotion.
const operandCorrelationReason = "function lies on the failed assertion's operand dependency path"

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
	base := append([]CandidateReportEntry(nil), shortlist.Entries...)
	correlated, corrReason := ssaLeadFunctions(report.SSAAssertionTrace)

	type rankedEntry struct {
		entry     CandidateReportEntry
		corr      bool
		dist      int
		framework bool
		index     int
	}
	items := make([]rankedEntry, 0, len(base)+len(correlated))
	present := map[string]bool{}
	for i, e := range base {
		key := fileFunctionKey(e.File, e.Function)
		dist, isCorr := correlated[key]
		if key != "" {
			present[key] = true
		}
		items = append(items, rankedEntry{
			entry:     e,
			corr:      isCorr,
			dist:      dist,
			framework: isFrameworkFrame(e.File, e.Function),
			index:     i,
		})
	}

	// Promote operand-correlated dependency functions that the candidate tier
	// did not already surface. This is an investigation lead, not a cause:
	// correlation with the failed assertion's operand does not establish
	// causality, so the kind and the abstention rules are unchanged.
	if trace := report.SSAAssertionTrace; trace != nil {
		bestValue := map[string]programrepo.SSAValue{}
		for _, value := range trace.Values {
			if value.Line <= 0 || strings.HasSuffix(value.File, "_test.go") || value.Function == "" {
				continue
			}
			name := shortSSAFunctionName(value.Function)
			if name == "" {
				continue
			}
			key := fileFunctionKey(value.File, name)
			if _, ok := correlated[key]; !ok {
				continue
			}
			if current, ok := bestValue[key]; !ok || ssaValueRank(value) > ssaValueRank(current) {
				bestValue[key] = value
			}
		}
		for key, value := range bestValue {
			if present[key] {
				continue
			}
			name := shortSSAFunctionName(value.Function)
			ids := []string{}
			for _, item := range report.Evidence {
				if strings.Contains(item.Detail, value.ID) || strings.Contains(item.Detail, value.Text) {
					ids = append(ids, item.ID)
				}
			}
			if len(ids) == 0 {
				ids = []string{"ssa:" + value.ID}
			}
			items = append(items, rankedEntry{
				entry: CandidateReportEntry{
					File:        value.File,
					Function:    name,
					Line:        value.Line,
					Kind:        "inferred_source",
					Reason:      corrReason,
					EvidenceIDs: ids,
				},
				corr:  true,
				dist:  correlated[key],
				index: len(base) + 1000,
			})
		}
	}

	// Order by evidence strength, then preserve the candidate tier's own
	// evidence-ranked order. Operand-correlated leads first (by proximity to the
	// failed assertion), then non-framework locations, then everything else in
	// candidate order. No kind-bucket reordering that could bury a stronger
	// candidate beneath a weaker one.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].corr != items[j].corr {
			return items[i].corr
		}
		if items[i].corr && items[i].dist != items[j].dist {
			return items[i].dist < items[j].dist
		}
		if items[i].framework != items[j].framework {
			return !items[i].framework
		}
		return items[i].index < items[j].index
	})

	for _, it := range items {
		entry := it.entry
		if !likelyFunctionEntry(entry) {
			continue
		}
		reason := entry.Reason
		if it.corr {
			reason = corrReason
		}
		leads = append(leads, LikelyFunctionLead{
			File:        entry.File,
			Function:    entry.Function,
			Line:        entry.Line,
			Kind:        entry.Kind,
			Reason:      reason,
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

// ssaLeadFunctions selects the SSA-derived leads and the reason label to show.
// It prefers functions on the failed assertion's operand dependency path. When
// the trace has no usable assertion anchor, it falls back to the prior
// dependency-trace behavior so a lead is never lost.
func ssaLeadFunctions(trace *programrepo.SSATrace) (map[string]int, string) {
	if trace == nil || len(trace.Values) == 0 {
		return nil, ""
	}
	if correlated := operandCorrelatedFunctions(trace); len(correlated) > 0 {
		return correlated, operandCorrelationReason
	}
	out := map[string]int{}
	for _, value := range trace.Values {
		if value.Line <= 0 || strings.HasSuffix(value.File, "_test.go") || value.Function == "" {
			continue
		}
		name := shortSSAFunctionName(value.Function)
		if name == "" {
			continue
		}
		out[fileFunctionKey(value.File, name)] = 99
	}
	if len(out) == 0 {
		return nil, ""
	}
	return out, "function contains an SSA value in the failed assertion dependency trace"
}

// operandCorrelatedFunctions returns every non-test function reachable backwards
// from the failed assertion's arguments along SSA operand edges, keyed by
// file|function and valued by the shortest edge distance from the assertion.
// Distance is used only to order leads; it is not a probability or a cause.
func operandCorrelatedFunctions(trace *programrepo.SSATrace) map[string]int {
	if trace == nil || len(trace.Values) == 0 {
		return nil
	}
	byID := make(map[string]programrepo.SSAValue, len(trace.Values))
	for _, value := range trace.Values {
		byID[value.ID] = value
	}
	anchor, ok := byID[trace.AnchorID]
	if !ok {
		return nil
	}
	start := anchor.Arguments
	if len(start) == 0 {
		start = anchor.Operands
	}
	if len(start) == 0 {
		return nil
	}
	const maxDepth = 6
	dist := make(map[string]int, len(trace.Values))
	queue := make([]string, 0, len(trace.Values))
	for _, id := range start {
		if _, seen := dist[id]; !seen {
			dist[id] = 1
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		d := dist[id]
		if d >= maxDepth {
			continue
		}
		for _, operand := range byID[id].Operands {
			if _, seen := dist[operand]; !seen {
				dist[operand] = d + 1
				queue = append(queue, operand)
			}
		}
	}
	out := map[string]int{}
	for id, d := range dist {
		value, ok := byID[id]
		if !ok || value.File == "" || strings.HasSuffix(value.File, "_test.go") || value.Function == "" {
			continue
		}
		name := shortSSAFunctionName(value.Function)
		if name == "" {
			continue
		}
		key := fileFunctionKey(value.File, name)
		if current, ok := out[key]; !ok || d < current {
			out[key] = d
		}
	}
	return out
}

// ssaValueRank prefers concrete producing operations over bare loads and error
// returns when several values share a function, mirroring the previous tier.
func ssaValueRank(value programrepo.SSAValue) int {
	switch value.Kind {
	case "*ssa.Convert", "*ssa.BinOp", "*ssa.Call", "*ssa.Lookup", "*ssa.TypeAssert":
		return 3
	case "*ssa.Return":
		return 2
	default:
		return 1
	}
}

// fileFunctionKey normalizes a source location to file|shortFunction so a
// candidate-tier entry and an SSA value for the same function compare equal.
func fileFunctionKey(file, function string) string {
	file = filepathSlash(file)
	function = shortFunctionName(function)
	if file == "" || function == "" {
		return ""
	}
	return file + "|" + function
}

func shortFunctionName(function string) string {
	function = strings.TrimSpace(function)
	if function == "" {
		return ""
	}
	if i := strings.IndexAny(function, "["); i >= 0 {
		function = function[:i]
	}
	function = strings.Trim(function, "()*")
	if i := strings.LastIndex(function, "."); i >= 0 {
		return function[i+1:]
	}
	return function
}

// isFrameworkFrame reports locations in the runtime, testing harness, or
// dependency/toolchain caches. These are excluded from the leading positions so
// a wrapper or runtime frame cannot outrank application code.
func isFrameworkFrame(file, function string) bool {
	f := strings.TrimSpace(function)
	if strings.HasPrefix(f, "runtime.") || strings.HasPrefix(f, "testing.") ||
		strings.Contains(f, "testing.tRunner") {
		return true
	}
	file = filepathSlash(file)
	return strings.Contains(file, "/go/pkg/mod/") ||
		strings.Contains(file, "/toolchain@") ||
		strings.Contains(file, "/go/src/")
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
