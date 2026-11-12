package diagnosis

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/causent/malenia/internal/localization"
)

func TestResolveRaceReportInstructionsRejectsMismatchedBinaryIdentity(t *testing.T) {
	descriptor, err := json.Marshal(ArtifactDescriptor{BuildID: "different-build", SourceRevision: "rev-1"})
	if err != nil {
		t.Fatal(err)
	}
	report := RaceReport{BuildID: "prow-build", SourceRevision: "rev-1", ReadStack: []localization.Frame{{Function: "main.read()", PCOffsetPresent: true}}, WriteStack: []localization.Frame{{Function: "main.write()", PCOffsetPresent: true}}}
	ResolveRaceReportInstructions(&report, []EvidenceItem{{Kind: "race_test_binary", Source: "/tmp/not-the-binary", Detail: string(descriptor)}})
	if report.ReadInstruction.Status != InstructionUnresolved || report.WriteInstruction.Status != InstructionUnresolved {
		t.Fatalf("mismatched binary was used: read=%#v write=%#v", report.ReadInstruction, report.WriteInstruction)
	}
}

func TestResolveRaceFrameInstructionRequiresPreservedOffset(t *testing.T) {
	result := ResolveRaceFrameInstruction("missing", localization.Frame{Function: "main.read()"})
	if result.Status != InstructionUnresolved || result.Reason == "" {
		t.Fatalf("missing PC offset was not kept unresolved: %#v", result)
	}
	result = ResolveRaceFrameInstruction("missing", localization.Frame{Function: "main.read()", PCOffsetPresent: true})
	if result.Status != InstructionUnresolved || result.Reason == "" {
		t.Fatalf("missing exact binary was not kept unresolved: %#v", result)
	}
}

func TestResolveRaceFrameInstructionUsesExactBinaryDWARF(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Prow artifact path under test is a Linux ELF race binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go toolchain unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/racefixture\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

//go:noinline
func readValue(value *int) int { return *value }

func main() { value := 1; _ = readValue(&value) }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fixture.test")
	cmd := exec.Command("go", "build", "-race", "-gcflags=all=-N -l", "-o", binary, ".")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build race binary: %v\n%s", err, output)
	}
	result := ResolveRaceFrameInstruction(binary, localization.Frame{Function: "main.readValue()", PCOffset: 0, PCOffsetPresent: true})
	if result.Status != InstructionResolved {
		t.Fatalf("exact binary/DWARF resolution failed: %#v", result)
	}
	if result.InstructionAddr == 0 || result.SourceLine == 0 || result.SourceFile == "" {
		t.Fatalf("incomplete instruction resolution: %#v", result)
	}
}

func TestRetainedRaceBinaryEndToEndFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fixture targets the Linux ELF Prow artifact")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go toolchain unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/racefixture\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `package racefixture

import (
	"sync"
	"testing"
)

var shared int

func TestControlledRace(t *testing.T) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; _ = shared }()
	go func() { defer wg.Done(); <-start; shared = 1 }()
	close(start)
	wg.Wait()
}
`
	if err := os.WriteFile(filepath.Join(dir, "race_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "racefixture.test")
	build := exec.Command("go", "test", "-race", "-c", "-gcflags=all=-N -l", "-o", binary, ".")
	build.Dir = dir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build retained race binary: %v\n%s", err, output)
	}
	run := exec.Command(binary, "-test.run", "^TestControlledRace$")
	run.Dir = dir
	output, err := run.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "WARNING: DATA RACE") {
		t.Fatalf("fixture did not produce a race report: err=%v\n%s", err, output)
	}
	reports := parseRaceReports(string(output))
	if len(reports) != 1 || len(reports[0].ReadStack) == 0 || len(reports[0].WriteStack) == 0 {
		t.Fatalf("race report stacks not parsed independently: %#v\n%s", reports, output)
	}
	for _, frame := range append(reports[0].ReadStack, reports[0].WriteStack...) {
		if !frame.PCOffsetPresent {
			t.Fatalf("race PC offset missing: %#v", frame)
		}
	}
	buildID, err := exec.Command("go", "tool", "buildid", binary).Output()
	if err != nil {
		t.Fatalf("read Go build ID: %v", err)
	}
	reports[0].BuildID = "prow-fixture-build"
	reports[0].SourceRevision = "fixture-revision"
	descriptor, err := json.Marshal(ArtifactDescriptor{BuildID: reports[0].BuildID, SourceRevision: reports[0].SourceRevision, BinaryBuildID: strings.TrimSpace(string(buildID)), RaceEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ResolveRaceReportInstructions(&reports[0], []EvidenceItem{{Kind: "race_test_binary", Source: binary, Detail: string(descriptor)}})
	if reports[0].ReadInstruction.Status != InstructionResolved || reports[0].WriteInstruction.Status != InstructionResolved {
		t.Fatalf("matching retained binary did not resolve both stack frames: read=%#v write=%#v\n%s", reports[0].ReadInstruction, reports[0].WriteInstruction, output)
	}
	if reports[0].ReadInstruction.SourceLine == 0 || reports[0].WriteInstruction.SourceLine == 0 {
		t.Fatalf("resolved frames lack DWARF source lines: read=%#v write=%#v", reports[0].ReadInstruction, reports[0].WriteInstruction)
	}
}
