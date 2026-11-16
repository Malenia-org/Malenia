package diagnosis

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/causent/malenia/internal/localization"
)

func TestPlanGoVerificationUsesOwningNestedModule(t *testing.T) {
	workspace := t.TempDir()
	packageDir := filepath.Join(workspace, "services", "api", "pkg", "cache")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "services", "api", "go.mod"), []byte("module example.com/api\n\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "cache.go"), []byte("package cache\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := planGoVerification(workspace, []RaceReport{{
		Test:      "TestCache/parallel writer",
		ReadStack: []localization.Frame{{File: "services/api/pkg/cache/cache.go", Line: 10}},
	}}, Baseline{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ModuleDir != "services/api" || plan.Package != "./pkg/cache" || plan.PackageDir != "services/api/pkg/cache" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if plan.RunPattern != `^TestCache$/^parallel writer$` {
		t.Fatalf("run pattern = %q", plan.RunPattern)
	}
}

func TestGoColdPathVerdictRequiresMatchingSignature(t *testing.T) {
	completed := func(matched, passed, attempts int) GoRevisionVerification {
		return GoRevisionVerification{Status: "completed", Matched: matched, Passed: passed, Attempts: make([]GoVerificationAttempt, attempts)}
	}
	head := completed(3, 0, 3)
	base := completed(0, 3, 3)
	if got := goColdPathVerdict(head, &base); got != ClassRealRegression {
		t.Fatalf("verdict = %s", got)
	}
	base = completed(1, 2, 3)
	if got := goColdPathVerdict(head, &base); got != ClassPreExisting {
		t.Fatalf("pre-existing verdict = %s", got)
	}
	head = completed(1, 2, 3)
	base = completed(0, 3, 3)
	if got := goColdPathVerdict(head, &base); got != ClassFlaky {
		t.Fatalf("mixed head verdict = %s", got)
	}
	head = completed(0, 3, 3)
	if got := goColdPathVerdict(head, &base); got != ClassNotReproduced {
		t.Fatalf("not reproduced verdict = %s", got)
	}
}

func TestPersistGoBinarySurvivesDisposableWorkspace(t *testing.T) {
	workspace := t.TempDir()
	bundle := t.TempDir()
	binary := filepath.Join(workspace, "race.test")
	if err := os.WriteFile(binary, []byte("binary evidence"), 0700); err != nil {
		t.Fatal(err)
	}
	d := &Diagnoser{cfg: Config{BundleDir: bundle}}
	descriptor, err := d.persistGoBinary(binary, "1234567890abcdef", GoVerificationPlan{Package: "./pkg/cache"}, GoRevisionVerification{GoVersion: "go1.25", BinaryBuildID: "build"}, "head")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(descriptor.StorageRef); err != nil {
		t.Fatalf("retained artifact disappeared with workspace: %v", err)
	}
	if descriptor.Digest == "" || descriptor.SourceRevision == "" || !descriptor.RaceEnabled {
		t.Fatalf("incomplete descriptor: %#v", descriptor)
	}
}
