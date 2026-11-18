// proof-audit checks retained evidence without executing target code.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/causent/malenia/internal/diagnosis"
	"github.com/causent/malenia/internal/evalcorpus"
	"os"
	"sort"
)

func main() {
	index := flag.String("reports", "", "JSON map from case ID to sealed report path")
	flag.Parse()
	if err := run(*index); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(index string) error {
	data, err := os.ReadFile(index)
	if err != nil {
		return err
	}
	var paths map[string]string
	if err := json.Unmarshal(data, &paths); err != nil {
		return err
	}
	var ids []string
	for id := range paths {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type entry struct {
		CaseID string                `json:"case_id"`
		Audit  evalcorpus.ProofAudit `json:"audit"`
	}
	var entries []entry
	counts := map[string]int{}
	for _, id := range ids {
		data, err := os.ReadFile(paths[id])
		if err != nil {
			return err
		}
		var report diagnosis.Report
		if err := json.Unmarshal(data, &report); err != nil {
			return err
		}
		audit := evalcorpus.AuditMechanismProof(report)
		counts[audit.Status]++
		entries = append(entries, entry{id, audit})
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Scope  string         `json:"scope"`
		Counts map[string]int `json:"counts"`
		Cases  []entry        `json:"cases"`
	}{"Retained-proof consistency only; report authenticity, binary bytes, complete source verification and upstream ground truth require independent review.", counts, entries})
}
