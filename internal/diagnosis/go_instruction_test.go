package diagnosis

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
