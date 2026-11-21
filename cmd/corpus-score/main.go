// corpus-score evaluates saved reports without passing expected answers to a runner.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caeser-org/ceaser/internal/diagnosis"
	"github.com/caeser-org/ceaser/internal/evalcorpus"
)

func main() {
	root := flag.String("corpus", "evaluation/corpus", "corpus directory")
	index := flag.String("reports", "", "JSON map from case ID to saved report path")
	flag.Parse()
	if err := run(*root, *index); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root, index string) error {
	data, err := os.ReadFile(index)
	if err != nil {
		return err
	}
	paths := map[string]string{}
	if err := json.Unmarshal(data, &paths); err != nil {
		return err
	}
	_, cases, err := evalcorpus.Load(root)
	if err != nil {
		return err
	}
	var scores []evalcorpus.Score
	var missing []string
	var counts evalcorpus.Counts
	for _, c := range cases {
		if !strings.HasPrefix(c.Metadata.ID, "go-") {
			continue
		}
		path := paths[c.Metadata.ID]
		if path == "" {
			missing = append(missing, c.Metadata.ID)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var report diagnosis.Report
		if err := json.Unmarshal(data, &report); err != nil {
			return err
		}
		score, err := evalcorpus.ScoreCase(filepath.Join(root, "cases", c.Metadata.ID), report)
		if err != nil {
			return err
		}
		counts.Add(score)
		scores = append(scores, score)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Counts  evalcorpus.Counts  `json:"counts"`
		Scores  []evalcorpus.Score `json:"scores"`
		Missing []string           `json:"missing_reports"`
	}{counts, scores, missing})
}
