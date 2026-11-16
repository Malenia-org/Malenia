package diagnosis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/causent/malenia/internal/domain"
	"github.com/causent/malenia/internal/execution"
	"github.com/causent/malenia/internal/localization"
)

const (
	defaultGoColdPathAttempts = 3
	maxGoColdPathAttempts     = 20
	maxRetainedGoBinarySize   = int64(512 << 20)
	goColdBinaryContainerPath = "/workspace/.malenia/go-cold/race.test"
)

func (d *Diagnoser) runGoColdPath(ctx context.Context, headWorkspace, headRevision string, original Baseline) GoColdPath {
	result := GoColdPath{Status: "not_attempted", Verdict: ClassInconclusive}
	if d.cfg.ExecutorName != "docker" {
		result.Status = "sandbox_required"
		result.Limitations = []string{"focused Go verification requires the Docker executor"}
		return result
	}
	traces := parseRaceReports(failureText(original, d.cfg.ExternalEvidence))
	plan, err := planGoVerification(headWorkspace, traces, original)
	if err != nil {
		result.Status = "planning_failed"
		result.Limitations = []string{err.Error()}
		return result
	}
	result.Plan = plan
	result.OriginalSignature = firstRaceSignature(traces)
	attempts := d.cfg.GoColdPathAttempts
	if attempts <= 0 {
		attempts = defaultGoColdPathAttempts
	}
	if attempts > maxGoColdPathAttempts {
		attempts = maxGoColdPathAttempts
	}

	result.Head = d.verifyGoRevision(ctx, headWorkspace, headRevision, plan, result.OriginalSignature, attempts, "head")
	if d.cfg.BaseRef != "" && ctx.Err() == nil {
		baseRevision := strings.TrimSpace(gitOutput(d.cfg.RepoPath, "rev-parse", d.cfg.BaseRef))
		if baseRevision == "" {
			result.Limitations = append(result.Limitations, "base revision could not be resolved")
		} else if baseWorkspace, cleanup, workspaceErr := isolatedWorkspace(ctx, d.cfg.RepoPath, baseRevision); workspaceErr != nil {
			result.Limitations = append(result.Limitations, "base workspace: "+workspaceErr.Error())
		} else {
			basePlan, planErr := planGoVerification(baseWorkspace, traces, original)
			if planErr != nil {
				result.Limitations = append(result.Limitations, "base plan: "+planErr.Error())
			} else {
				base := d.verifyGoRevision(ctx, baseWorkspace, baseRevision, basePlan, result.OriginalSignature, attempts, "base")
				result.Base = &base
			}
			cleanup()
		}
	}
	result.Verdict = goColdPathVerdict(result.Head, result.Base)
	result.Status = "completed"
	if result.Head.Status != "completed" || result.Base != nil && result.Base.Status != "completed" {
		result.Status = "inconclusive"
	}
	if result.Base == nil {
		result.Limitations = append(result.Limitations, "base verification was not available; PR attribution remains unverified")
	}
	return result
}

func planGoVerification(workspace string, traces []RaceReport, baseline Baseline) (GoVerificationPlan, error) {
	var sourceFile, test string
	for _, trace := range traces {
		if test == "" {
			test = trace.Test
		}
		for _, stack := range [][]localizationFrame{framesAsLocal(trace.ReadStack), framesAsLocal(trace.WriteStack)} {
			for _, frame := range stack {
				if frame.File != "" && !filepath.IsAbs(frame.File) && strings.HasSuffix(frame.File, ".go") {
					sourceFile = filepath.FromSlash(frame.File)
					break
				}
			}
			if sourceFile != "" {
				break
			}
		}
		if sourceFile != "" {
			break
		}
	}
	if test == "" {
		for _, attempt := range baseline.Results {
			for _, observed := range parseFailureObservations(attempt.Stderr) {
				if strings.HasPrefix(observed.Test, "Test") {
					test = observed.Test
					break
				}
			}
		}
	}
	if sourceFile == "" {
		return GoVerificationPlan{}, fmt.Errorf("no repository-relative Go stack location was available to derive a package")
	}
	packageDir := filepath.Dir(filepath.Join(workspace, sourceFile))
	if !withinWorkspace(workspace, packageDir) {
		return GoVerificationPlan{}, fmt.Errorf("derived package escapes the disposable workspace")
	}
	if info, err := os.Stat(packageDir); err != nil || !info.IsDir() {
		return GoVerificationPlan{}, fmt.Errorf("derived package directory is unavailable at the requested revision: %s", filepath.ToSlash(sourceFile))
	}
	moduleDir, err := nearestGoModule(workspace, packageDir)
	if err != nil {
		return GoVerificationPlan{}, err
	}
	relPackage, err := filepath.Rel(moduleDir, packageDir)
	if err != nil || strings.HasPrefix(relPackage, "..") {
		return GoVerificationPlan{}, fmt.Errorf("derive package relative to Go module")
	}
	pkg := "."
	if relPackage != "." {
		pkg = "./" + filepath.ToSlash(relPackage)
	}
	moduleRel, _ := filepath.Rel(workspace, moduleDir)
	packageRel, _ := filepath.Rel(workspace, packageDir)
	return GoVerificationPlan{
		PackageDir: filepath.ToSlash(packageRel),
		ModuleDir:  filepath.ToSlash(moduleRel),
		Package:    pkg,
		Test:       test,
		RunPattern: goTestRunPattern(test),
	}, nil
}

// A tiny local frame avoids exporting localization internals through the planner.
type localizationFrame struct{ File string }

func framesAsLocal(frames []localization.Frame) []localizationFrame {
	result := make([]localizationFrame, 0, len(frames))
	for _, frame := range frames {
		result = append(result, localizationFrame{File: frame.File})
	}
	return result
}

func nearestGoModule(workspace, start string) (string, error) {
	for current := start; withinWorkspace(workspace, current); current = filepath.Dir(current) {
		if info, err := os.Stat(filepath.Join(current, "go.mod")); err == nil && !info.IsDir() {
			return current, nil
		}
		if current == workspace || filepath.Dir(current) == current {
			break
		}
	}
	return "", fmt.Errorf("no go.mod owns the observed package")
}

func withinWorkspace(workspace, candidate string) bool {
	rel, err := filepath.Rel(workspace, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func goTestRunPattern(test string) string {
	if test == "" {
		return ""
	}
	parts := strings.Split(test, "/")
	for i := range parts {
		parts[i] = "^" + regexp.QuoteMeta(parts[i]) + "$"
	}
	return strings.Join(parts, "/")
}

func (d *Diagnoser) verifyGoRevision(ctx context.Context, workspace, revision string, plan GoVerificationPlan, originalSignature string, attempts int, label string) GoRevisionVerification {
	result := GoRevisionVerification{Revision: revision, Status: "build_failed"}
	moduleDir := workspace
	if plan.ModuleDir != "." && plan.ModuleDir != "" {
		moduleDir = filepath.Join(workspace, filepath.FromSlash(plan.ModuleDir))
	}
	binaryHostPath := filepath.Join(workspace, ".malenia", "go-cold", "race.test")
	if err := os.MkdirAll(filepath.Dir(binaryHostPath), 0700); err != nil {
		result.EnvironmentGap = err.Error()
		return result
	}
	buildCommand := []string{"go", "test", "-race", "-c", "-o", goColdBinaryContainerPath, plan.Package}
	result.Build = d.executeAttempt(ctx, moduleDir, buildCommand, -1, label+"-build")
	if !result.Build.Passed() {
		result.EnvironmentGap = classifyGoBuildGap(result.Build)
		return result
	}
	info, err := os.Stat(binaryHostPath)
	if err != nil || !info.Mode().IsRegular() {
		result.EnvironmentGap = "test build succeeded but retained binary was not produced"
		return result
	}
	if info.Size() > maxRetainedGoBinarySize {
		result.EnvironmentGap = fmt.Sprintf("retained test binary exceeds %d byte limit", maxRetainedGoBinarySize)
		return result
	}
	result.BinaryDigest, _ = fileSHA256(binaryHostPath)
	result.BinaryBuildID = d.probeGoBuildID(ctx, moduleDir, label)
	if result.BinaryBuildID == "" {
		result.BinaryBuildID = goBuildID(binaryHostPath)
	}
	result.GoVersion = d.probeGoVersion(ctx, moduleDir, label)
	if descriptor, persistErr := d.persistGoBinary(binaryHostPath, revision, plan, result, label); persistErr != nil {
		result.EnvironmentGap = "persist retained binary: " + persistErr.Error()
		return result
	} else {
		result.Artifact = descriptor
		result.BinaryPath = descriptor.Path
	}

	runCommand := []string{goColdBinaryContainerPath}
	if plan.RunPattern != "" {
		runCommand = append(runCommand, "-test.run", plan.RunPattern)
	}
	runCommand = append(runCommand, "-test.count=1", "-test.v")
	for i := 0; i < attempts && ctx.Err() == nil; i++ {
		attempt := d.executeAttempt(ctx, moduleDir, runCommand, i, fmt.Sprintf("%s-run-%d", label, i+1))
		traces := parseRaceReports(attempt.Stderr)
		for traceIndex := range traces {
			traces[traceIndex].SourceRevision = revision
			traces[traceIndex].BuildID = result.BinaryBuildID
			traces[traceIndex].ReadInstruction = ResolveRaceFrameInstruction(binaryHostPath, firstRaceFrame(traces[traceIndex].ReadStack))
			traces[traceIndex].WriteInstruction = ResolveRaceFrameInstruction(binaryHostPath, firstRaceFrame(traces[traceIndex].WriteStack))
		}
		result.RaceReports = append(result.RaceReports, traces...)
		signature := firstRaceSignature(traces)
		matched := originalSignature != "" && signature == originalSignature
		testExecuted := plan.Test == "" || strings.Contains(attempt.Stderr, plan.Test) || signature != ""
		result.Attempts = append(result.Attempts, GoVerificationAttempt{Attempt: attempt, Signature: signature, SignatureMatch: matched, TestExecuted: testExecuted})
		if matched {
			result.Matched++
		}
		if attempt.Passed() {
			result.Passed++
		}
	}
	result.Status = "completed"
	return result
}

func (d *Diagnoser) executeAttempt(ctx context.Context, dir string, command []string, index int, instance string) Attempt {
	runCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()
	res, err := d.cfg.Executor.Run(runCtx, execution.Request{
		Command: command, Dir: dir, Timeout: d.cfg.Timeout, MaxOutput: 64 << 10,
		WorkspacePath: workspaceRootFor(dir), InstanceID: d.investigationID + "-" + instance,
	})
	attempt := attemptFromExecution(index, res, err)
	return attempt
}

func workspaceRootFor(dir string) string {
	current := dir
	for {
		if info, err := os.Stat(filepath.Join(current, ".git")); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir
		}
		current = parent
	}
}

func attemptFromExecution(index int, res domain.ExecutionResult, err error) Attempt {
	attempt := Attempt{
		Index: index, ExitCode: res.ExitCode, TimedOut: res.TimedOut,
		Executed: res.StartedAt != (time.Time{}) || res.TimedOut, Duration: res.Duration,
		StdoutDigest: digestString(res.Stdout), StderrDigest: digestString(res.Stderr),
		Stderr: bound(res.Stderr+"\n"+res.Stdout, MaxEvidenceExcerpt), FirstOutputAt: res.FirstOutputAt,
		StdoutBytes: res.StdoutBytes, StderrBytes: res.StderrBytes, CleanupComplete: res.CleanupComplete,
		Truncated: res.Truncated, TelemetryAvailable: res.TelemetryAvailable,
	}
	if err != nil {
		attempt.Error = err.Error()
	}
	return attempt
}

func firstRaceSignature(traces []RaceReport) string {
	if len(traces) == 0 {
		return ""
	}
	race := traces[0]
	read, write := firstRaceFrame(race.ReadStack), firstRaceFrame(race.WriteStack)
	return strings.Join([]string{race.ReadInstrumentation, frameSignature(read), race.WriteInstrumentation, frameSignature(write)}, "|")
}

func frameSignature(frame localization.Frame) string {
	identity := strings.TrimSuffix(frame.Function, "()")
	if identity == "" {
		identity = fmt.Sprint(frame.Line)
	}
	return filepath.ToSlash(frame.File) + ":" + identity
}

func goColdPathVerdict(head GoRevisionVerification, base *GoRevisionVerification) Classification {
	if head.Status != "completed" || len(head.Attempts) == 0 {
		return ClassInconclusive
	}
	if base == nil || base.Status != "completed" || len(base.Attempts) == 0 {
		if head.Matched == 0 {
			return ClassNotReproduced
		}
		return ClassInconclusive
	}
	if head.Matched > 0 && base.Matched > 0 {
		return ClassPreExisting
	}
	if head.Matched == len(head.Attempts) && base.Matched == 0 && base.Passed == len(base.Attempts) {
		return ClassRealRegression
	}
	if head.Matched == 0 && head.Passed == len(head.Attempts) {
		return ClassNotReproduced
	}
	return ClassFlaky
}

func goColdPathNextActions(result GoColdPath) []string {
	if result.Head.Status != "completed" {
		return []string{"resolve the reported sandbox or build environment gap and rerun focused verification"}
	}
	for _, race := range result.Head.RaceReports {
		if race.ReadInstruction.Status != InstructionResolved || race.WriteInstruction.Status != InstructionResolved {
			return []string{"retain symbols and DWARF for the generated binary and resolve both reported instruction offsets"}
		}
	}
	switch result.Verdict {
	case ClassRealRegression:
		return []string{"verify a candidate fix against the retained focused test and add a regression test"}
	case ClassPreExisting:
		return []string{"route the pre-existing race to the owning component without blocking this PR solely on attribution"}
	case ClassNotReproduced:
		return []string{"increase bounded attempts or align the sandbox toolchain and environment with the original CI execution"}
	default:
		return []string{"repeat the bounded base/head experiment until outcomes distinguish a revision effect from a shared flake"}
	}
}

func classifyGoBuildGap(attempt Attempt) string {
	text := strings.ToLower(attempt.Stderr + " " + attempt.Error)
	switch {
	case strings.Contains(text, "go: downloading"), strings.Contains(text, "module lookup disabled"), strings.Contains(text, "network is unreachable"):
		return "environment_mismatch: required Go modules were not available in the network-disabled sandbox"
	case attempt.TimedOut:
		return "environment_mismatch: focused test build timed out"
	default:
		return "focused race-enabled test binary could not be built"
	}
}

func (d *Diagnoser) probeGoVersion(ctx context.Context, dir, label string) string {
	attempt := d.executeAttempt(ctx, dir, []string{"go", "version"}, -1, label+"-go-version")
	return strings.TrimSpace(attempt.Stderr)
}

func (d *Diagnoser) probeGoBuildID(ctx context.Context, dir, label string) string {
	attempt := d.executeAttempt(ctx, dir, []string{"go", "tool", "buildid", goColdBinaryContainerPath}, -1, label+"-go-buildid")
	return strings.TrimSpace(attempt.Stderr)
}

func (d *Diagnoser) persistGoBinary(source, revision string, plan GoVerificationPlan, verification GoRevisionVerification, label string) (*ArtifactDescriptor, error) {
	if d.cfg.BundleDir == "" {
		return nil, fmt.Errorf("bundle directory is required for retained binaries")
	}
	targetDir := filepath.Join(d.cfg.BundleDir, "artifacts", "go")
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return nil, err
	}
	name := label + "-" + shortRevision(revision) + ".test"
	target := filepath.Join(targetDir, name)
	relativeTarget := filepath.ToSlash(filepath.Join("artifacts", "go", name))
	if err := copyBoundedFile(target, source, maxRetainedGoBinarySize); err != nil {
		return nil, err
	}
	digest, err := fileSHA256(target)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	descriptor := &ArtifactDescriptor{
		SchemaVersion: 1, ArtifactID: "go-race-binary-" + label + "-" + shortRevision(revision),
		Provider: "malenia-go-cold-path", SourceRevision: revision, Name: name, Path: relativeTarget,
		MediaType: "application/x-executable", Size: info.Size(), Digest: digest, StorageRef: target,
		Category: ArtifactRuntime, Kind: "race_test_binary", Package: plan.Package,
		GoVersion: verification.GoVersion, RaceEnabled: true, BinaryBuildID: verification.BinaryBuildID,
		Symbols: "embedded", DWARF: "embedded",
		TestCommand: []string{"go", "test", "-race", "-c", plan.Package},
	}
	data, _ := json.Marshal(descriptor)
	manifestPath := target + ".json"
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		return nil, err
	}
	return descriptor, nil
}

func copyBoundedFile(target, source string, limit int64) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".go-binary-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, copyErr := io.Copy(tmp, io.LimitReader(in, limit+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > limit {
		return fmt.Errorf("binary exceeds %d byte retention limit", limit)
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func goBuildID(path string) string {
	output, err := exec.Command("go", "tool", "buildid", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func shortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
