package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Malenia-org/ceaser/internal/diagnosis"
)

func runBrief(args []string) {
	fs := flag.NewFlagSet("brief", flag.ExitOnError)
	reportPath := fs.String("report", "", "diagnosis report JSON")
	prompt := fs.Bool("prompt", false, "include instructions for an external AI")
	fs.Parse(args)
	if *reportPath == "" {
		fmt.Fprintln(os.Stderr, "usage: ceaser brief --report report.json [--prompt]")
		os.Exit(2)
	}
	data, err := readBoundedFile(*reportPath, 64<<20)
	var report diagnosis.Report
	if err == nil {
		err = json.Unmarshal(data, &report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief:", err)
		os.Exit(1)
	}
	fmt.Print(diagnosis.InvestigationBrief(report, *prompt))
}
