package diagnosis

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Malenia-org/ceaser/internal/domain"
)

// CandidateReport is a bounded shortlist of places to look next. It reuses
// ranked candidates, observed failure locations and the selected probe
// operation, but never upgrades any of them into a verified mechanism.
type CandidateReport struct {
	Status      string                 `json:"status"`
	Reason      string                 `json:"reason,omitempty"`
	ProbeStatus string                 `json:"probe_status"`
	Entries     []CandidateReportEntry `json:"entries,omitempty"`
	Limitations []string               `json:"limitations,omitempty"`
}

// CandidateReportEntry keeps the kind explicit so an observed stack frame can
// be told apart from an inferred call or source relationship.
type CandidateReportEntry struct {
	Rank        int      `json:"rank"`
	File        string   `json:"file"`
	Function    string   `json:"function,omitempty"`
	Line        int      `json:"line,omitempty"`
	Kind        string   `json:"kind"`
	Reason      string   `json:"reason"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

const candidateReportLimit = 5

// BuildCandidateReport produces the evidence-backed shortlist. It is derived
// from existing report fields and does not change classification, promotion,
// confidence, or any verification gate.
func BuildCandidateReport(report Report) CandidateReport {
	result := CandidateReport{Status: "unavailable", ProbeStatus: "disabled"}
	if obs := report.MechanismObservation; obs != nil {
		result.ProbeStatus = obs.Status
		if obs.Status != "observed" && obs.Status != "" {
			result.Limitations = append(result.Limitations,
				fmt.Sprintf("runtime observation %s is inconclusive; no mechanism is established", obs.Status))
		}
		if obs.Correlation == nil || !obs.Correlation.Verified {
			result.Limitations = append(result.Limitations,
				"probe values are not correlated with the failing invocation")
		}
	}

	type key struct {
		file, function string
		line           int
	}
	seen := map[key]bool{}
	entries := []CandidateReportEntry{}
	add := func(file, function string, line int, kind, reason string, ids []string) {
		file = strings.TrimSpace(file)
		if file == "" || len(entries) >= candidateReportLimit {
			return
		}
		k := key{file: filepathSlash(file), function: function, line: line}
		if seen[k] {
			return
		}
		seen[k] = true
		entries = append(entries, CandidateReportEntry{
			Rank:        len(entries) + 1,
			File:        filepathSlash(file),
			Function:    function,
			Line:        line,
			Kind:        kind,
			Reason:      reason,
			EvidenceIDs: boundedEvidenceIDs(ids, 3),
		})
	}

	// Observed stack locations come from failure evidence, not from inference.
	for _, incident := range report.FailureIncidents {
		for _, loc := range incident.ObservedLocations {
			add(loc.File, loc.Function, loc.Line, "observed_stack",
				"location observed in the failure evidence", incident.EvidenceIDs)
		}
	}

	// The selected probe operation is concrete, but inconclusive observations
	// remain only a lead. GetInt's line 255 fallback, for example, is not a
	// verified mechanism when its values are uncorrelated.
	if obs := report.MechanismObservation; obs != nil && obs.OperationFile != "" {
		reason := "selected probe operation; observation inconclusive"
		if obs.Status == "observed" && !obs.ValueMatched {
			reason = "selected probe operation; value did not match the failure"
		}
		if obs.Correlation == nil || !obs.Correlation.Verified {
			reason = "selected probe operation; values not correlated with the failing invocation"
		}
		add(obs.OperationFile, obs.OperationName, obs.OperationLine, "probe_operation", reason, obs.EvidenceIDs)
	}

	// Ranked candidates may be verified call edges or weaker source relations.
	for _, candidate := range report.Candidates {
		loc := candidate.Location
		kind, reason := inferredCandidateKind(loc.Provenance, loc.LocationType)
		add(loc.FilePath, candidateFunction(loc.SymbolIdentity), loc.StartLine, kind, reason, candidateIDs(candidate.SupportingEvidenceIDs))
	}

	// Localization stays useful when probing is disabled or cannot start.
	for _, file := range report.Localization.LikelyFiles {
		add(file, "", 0, "inferred_source", "ranked among localized likely files", evidenceForFile(report.Evidence, file))
	}

	if len(entries) == 0 {
		result.Reason = "no observed location, ranked candidate, or localized file was recorded"
		return result
	}
	result.Status = "available"
	result.Entries = entries
	return result
}

func inferredCandidateKind(provenance, locationType string) (kind, reason string) {
	p := strings.ToLower(provenance + " " + locationType)
	switch {
	case strings.Contains(p, "stack"), strings.Contains(p, "observed_frame"), strings.Contains(p, "enclosing_symbol"), strings.Contains(p, "frame"):
		return "observed_stack", "candidate derived from an observed stack location"
	case strings.Contains(p, "call"):
		return "inferred_call", "candidate inferred from a resolved call relationship"
	case strings.Contains(p, "ssa"), strings.Contains(p, "value"), strings.Contains(p, "dependency"):
		return "inferred_source", "candidate inferred from a static value dependency"
	default:
		return "inferred_source", "ranked source candidate"
	}
}

func candidateFunction(identity string) string {
	s := strings.TrimSpace(identity)
	if s == "" || strings.HasPrefix(s, "observed:") {
		return ""
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && i+1 < len(s) {
		tail := s[i+1:]
		if _, err := strconv.Atoi(tail); err != nil && !strings.Contains(tail, "(") {
			s = tail
		}
	}
	if i := strings.LastIndex(s, "."); i > 0 {
		method := s[i+1:]
		if j := strings.LastIndex(s[:i], "."); j >= 0 {
			s = s[j+1:i] + "." + method
		}
	}
	s = strings.TrimPrefix(s, "(*")
	s = strings.TrimSuffix(s, ")")
	return bound(s, 96)
}

func filepathSlash(file string) string {
	return strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")
}

func candidateIDs(ids []domain.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if v := strings.TrimSpace(string(id)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func boundedEvidenceIDs(ids []string, limit int) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func evidenceForFile(evidence []EvidenceItem, file string) []string {
	want := filepathSlash(file)
	var ids []string
	for _, item := range evidence {
		if filepathSlash(item.Source) == want {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
