// Command likely-bench evaluates the likely-function ranking stage offline
// against the frozen saved reports, so a ranking change can be compared
// before/after with identical inputs.
//
// It does not re-diagnose: it loads each saved ceaser/malenia report, rebuilds
// the likely-function tier from the report's own candidate tier and SSA trace,
// and prints the result. This isolates ranking (the only thing under test);
// candidate discovery is held fixed.
//
// Usage:
//   go run ./cmd/likely-bench --artifacts evaluation/probe-rerun/artifacts --out /tmp/before.json
//
// This is an evaluation tool, not part of the shipping CLI.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Malenia-org/ceaser/internal/diagnosis"
)

type lead struct {
	File       string   `json:"file"`
	Function   string   `json:"function,omitempty"`
	Line       int      `json:"line,omitempty"`
	Kind       string   `json:"kind"`
	Reason     string   `json:"reason"`
	Confidence string   `json:"confidence"`
	Evidence   []string `json:"evidence_ids,omitempty"`
}

type caseResult struct {
	Case           string `json:"case"`
	Status         string `json:"status"`
	Primary        *lead  `json:"primary,omitempty"`
	Alternates     []lead `json:"alternates,omitempty"`
	Entries        []lead `json:"entries,omitempty"`
	EntryCount     int    `json:"entry_count"`
	ImplEntries    int    `json:"impl_entries"`
	ObservedLoc    int    `json:"observed_locations"`
	SSAStatus      string `json:"ssa_status"`
	Classification string `json:"classification"`
}

func main() {
	artifacts := flag.String("artifacts", "evaluation/probe-rerun/artifacts", "corpus artifacts dir")
	out := flag.String("out", "", "output JSON path (stdout if empty)")
	rebuild := flag.Bool("rebuild-candidate-report", false, "rebuild the candidate tier from the report's own fields instead of using the saved one")
	flag.Parse()

	var reports []string
	filepath.WalkDir(*artifacts, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "malenia-report.json" || d.Name() == "ceaser-report.json" {
			reports = append(reports, p)
		}
		return nil
	})
	sort.Strings(reports)

	results := []caseResult{}
	for _, p := range reports {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rep diagnosis.Report
		if err := json.Unmarshal(data, &rep); err != nil {
			fmt.Fprintf(os.Stderr, "unmarshal %s: %v\n", p, err)
			continue
		}
		caseID := caseFromPath(p)
		res := caseResult{Case: caseID, Classification: string(rep.Classification), SSAStatus: "none"}
		if rep.SSAAssertionTrace != nil {
			res.SSAStatus = rep.SSAAssertionTrace.Status
		}
		if *rebuild || rep.CandidateReport == nil {
			built := diagnosis.BuildCandidateReport(rep)
			rep.CandidateReport = &built
		}
		res.EntryCount = len(rep.CandidateReport.Entries)
		for _, e := range rep.CandidateReport.Entries {
			if !strings.HasSuffix(e.File, "_test.go") {
				res.ImplEntries++
			}
			res.Entries = append(res.Entries, lead{File: e.File, Function: e.Function, Line: e.Line, Kind: e.Kind, Reason: e.Reason, Confidence: ""})
		}
		for _, inc := range rep.FailureIncidents {
			res.ObservedLoc += len(inc.ObservedLocations)
		}

		lf := diagnosis.BuildLikelyFunction(rep)
		res.Status = lf.Status
		if lf.Primary != nil {
			res.Primary = toLead(*lf.Primary)
		}
		for _, a := range lf.Alternates {
			res.Alternates = append(res.Alternates, *toLead(a))
		}
		results = append(results, res)
	}

	enc, _ := json.MarshalIndent(results, "", "  ")
	if *out == "" {
		fmt.Println(string(enc))
		return
	}
	if err := os.WriteFile(*out, append(enc, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d cases -> %s\n", len(results), *out)
}

func toLead(l diagnosis.LikelyFunctionLead) *lead {
	return &lead{
		File: l.File, Function: l.Function, Line: l.Line, Kind: l.Kind,
		Reason: l.Reason, Confidence: l.Confidence, Evidence: l.EvidenceIDs,
	}
}

func caseFromPath(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, "go-u") {
			return part
		}
	}
	return filepath.Base(filepath.Dir(p))
}
