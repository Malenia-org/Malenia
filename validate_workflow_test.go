package caeser_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func readWorkflow(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestCIIntegrationTriggerIsValid(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/ci-integration.yml")
	if err != nil {
		t.Fatalf("read ci-integration.yml: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse ci-integration.yml: %v", err)
	}
	root := doc.Content[0]
	on := mapKey(root, "on")
	if on == nil {
		t.Fatal("ci-integration.yml has no on trigger")
	}
	workflowRun := mapKey(on, "workflow_run")
	if workflowRun == nil {
		t.Fatal("ci-integration.yml has no workflow_run trigger")
	}
	if mapKey(workflowRun, "if") != nil {
		t.Fatal("workflow_run must not contain if; conditions belong at job level")
	}
	job := mapKey(mapKey(root, "jobs"), "diagnose")
	if job == nil || mapKey(job, "if") == nil {
		t.Fatal("ci-integration.yml must filter failures with a job-level if")
	}
	if !strings.Contains(string(data), "pull_requests[0].base.sha") {
		t.Fatal("ci-integration.yml must pass the pull request base SHA")
	}
	if !strings.Contains(string(data), "issues: write") {
		t.Fatal("ci-integration.yml must grant comment publication permission")
	}
}

func TestReusableWorkflowContract(t *testing.T) {
	content := readWorkflow(t, ".github/workflows/ci-diagnose.yml")
	checks := map[string]bool{
		"workflow_call trigger":                        strings.Contains(content, "workflow_call"),
		"head_sha input":                               strings.Contains(content, "head_sha:"),
		"base_sha input":                               strings.Contains(content, "base_sha:"),
		"base_ref input":                               strings.Contains(content, "base_ref:"),
		"run_id input":                                 strings.Contains(content, "run_id:"),
		"checks out failing commit":                    strings.Contains(content, "github.event.workflow_run.head_sha"),
		"fetches failed logs":                          strings.Contains(content, "--log-failed"),
		"installs and verifies released binary":        strings.Contains(content, "caeser_linux_amd64") && strings.Contains(content, "SHA256SUMS"),
		"passes base revision":                         strings.Contains(content, "--base"),
		"passes docker executor":                       strings.Contains(content, "--executor docker"),
		"exposes focused Go cold path":                 strings.Contains(content, "go_cold_path:") && strings.Contains(content, "--go-cold-path"),
		"passes attempts":                              strings.Contains(content, "--attempts"),
		"uploads bundle":                               strings.Contains(content, "actions/upload-artifact"),
		"writes job summary":                           strings.Contains(content, "GITHUB_STEP_SUMMARY"),
		"comments on PR":                               strings.Contains(content, "actions/github-script@v7") && strings.Contains(content, "issues.createComment"),
		"comment includes structured evidence summary": strings.Contains(content, "failure_incidents") && strings.Contains(content, "comparison.base.passes") && strings.Contains(content, "findings"),
		"summary uses root cause contract":             strings.Contains(content, `.root_cause.status == "established"`) && strings.Contains(content, "Implementation cause unresolved."),
		"summary declares adapter depth":               strings.Contains(content, "adapter_capabilities") && strings.Contains(content, "Adapter support"),
		"summary does not use legacy claims":           !strings.Contains(content, "claims // []") && !strings.Contains(content, "Likely changed files"),
		"comment links workflow artifact":              strings.Contains(content, "GITHUB_SERVER_URL") && strings.Contains(content, "GITHUB_RUN_ID"),
		"comment avoids runner-local bundle path":      !strings.Contains(content, "Bundle path recorded by the runner"),
		"does not persist checkout credentials":        strings.Contains(content, "persist-credentials: false"),
		"passes original CI log":                       strings.Contains(content, "--ci-log"),
		"separate publication job":                     strings.Contains(content, "publish:"),
		"publication can write issue comments":         strings.Contains(content, "issues: write"),
		"does not expose gateway secret to diagnosis":  !strings.Contains(content[:strings.Index(content, "name: Optional model analysis")], "CAESER_GATEWAY_ENDPOINT: ${{ secrets"),
		"has separate analysis job":                    strings.Contains(content, "name: Optional model analysis") && strings.Contains(content, "caeser analyze"),
		"uploads raw diagnosis artifact":               strings.Contains(content, "caeser-diagnosis-raw-"),
		"passes final diagnosis artifact":              strings.Contains(content, "caeser-diagnosis-${{ github.run_id }}"),
		"validates release version":                    strings.Contains(content, "invalid caeser_version"),
		"validates release repository":                 strings.Contains(content, "invalid caeser_repository"),
		"redacts bearer tokens":                        strings.Contains(content, "bearer"),
		"redacts GitHub tokens":                        strings.Contains(content, "gh[pousr]_") && strings.Contains(content, "github_pat_"),
	}
	for check, passed := range checks {
		if !passed {
			t.Errorf("reusable workflow check failed: %s", check)
		}
	}
}

func TestManualDiagnosisWorkflow(t *testing.T) {
	content := readWorkflow(t, ".github/workflows/diagnosis.yml")
	checks := map[string]bool{
		"workflow_dispatch trigger": strings.Contains(content, "workflow_dispatch"),
		"failing_command input":     strings.Contains(content, "failing_command"),
		"attempts input":            strings.Contains(content, "attempts"),
		"docker_image input":        strings.Contains(content, "docker_image"),
		"uses reusable workflow":    strings.Contains(content, "./.github/workflows/ci-diagnose.yml"),
	}
	for check, passed := range checks {
		if !passed {
			t.Errorf("diagnosis workflow check failed: %s", check)
		}
	}
}

func TestPublicTrialCanUseSourceBuildAndVerifiedRepair(t *testing.T) {
	content := readWorkflow(t, ".github/workflows/try-public-pr.yml")
	for check, passed := range map[string]bool{
		"source build input":           strings.Contains(content, "source_build:"),
		"repair input":                 strings.Contains(content, "repair:"),
		"passes repair flag":           strings.Contains(content, "args+=(--repair)"),
		"renders repair result":        strings.Contains(content, "Repair and verification") && strings.Contains(content, ".repair.verification_status"),
		"accepts original Prow URL":    strings.Contains(content, "source_ci_url:") && strings.Contains(content, "SOURCE_CI_URL"),
		"uses extracted resolver":      strings.Contains(content, "resolve-source-evidence.sh"),
		"propagates resolver paths":    strings.Contains(content, "bounded_log_path") && strings.Contains(content, "EVIDENCE_ARTIFACT_DIR"),
		"uploads resolver diagnostics": strings.Contains(content, "source-evidence-status.txt"),
		"sandboxes public execution":   strings.Contains(content, "Public PR reproduction, cold verification, and repair require an explicit docker_image sandbox"),
		"gates base execution":         strings.Contains(content, "--artifact-base-execution"),
		"exposes Go cold path":         strings.Contains(content, "go_cold_path:") && strings.Contains(content, "--go-cold-path"),
		"opt-in closed PR testing":     strings.Contains(content, "allow_closed_pr:") && strings.Contains(content, "ALLOW_CLOSED_PR") && strings.Contains(content, "[ \"$ALLOW_CLOSED_PR\" = \"true\" ]"),
		"build once via cache":         strings.Contains(content, "actions/cache@v4") && strings.Contains(content, "caeser-${{ runner.os }}-${{ runner.arch }}-${{ github.sha }}"),
	} {
		if !passed {
			t.Errorf("public trial check failed: %s", check)
		}
	}
}

func TestControlledGoRegressionHarnessWorkflow(t *testing.T) {
	content := readWorkflow(t, ".github/workflows/controlled-go-regression.yml")
	for check, passed := range map[string]bool{
		"manual dispatch":          strings.Contains(content, "workflow_dispatch:"),
		"buggy base checkout":      strings.Contains(content, "checkout -q --detach \"$BASE_SHA\""),
		"test-only injection":      strings.Contains(content, "PR changed no recognizable test files"),
		"comment redaction":        strings.Contains(content, "grep -vE '^[[:space:]]*//'"),
		"produces failure log":     strings.Contains(content, "base-failure.log"),
		"artifact-first ci-log":    strings.Contains(content, "--ci-log \"$RUNNER_TEMP/base-failure.log\"") && strings.Contains(content, "--artifact-first"),
		"no source patch applied":  strings.Contains(content, "source changes are deliberately withheld"),
		"fixed head control":       strings.Contains(content, "Control - run regression test on fixed head"),
		"head clone url for forks": strings.Contains(content, "head_clone_url") && strings.Contains(content, "HEAD_CLONE_URL"),
		"build once via cache":     strings.Contains(content, "actions/cache@v4") && strings.Contains(content, "caeser-${{ runner.os }}-${{ runner.arch }}-${{ github.sha }}"),
		"timeout failure handled":  strings.Contains(content, "base run hung") && strings.Contains(content, "HARNESS OBSERVATION"),
		"uploads artifacts":        strings.Contains(content, "actions/upload-artifact@v4"),
	} {
		if !passed {
			t.Errorf("controlled Go regression harness check failed: %s", check)
		}
	}
}

func TestProwEvidenceResolverScriptExists(t *testing.T) {
	info, err := os.Stat(".github/scripts/resolve-source-evidence.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("Prow evidence resolver must be executable")
	}
}

func TestReleaseWorkflowPublishesBinary(t *testing.T) {
	content := readWorkflow(t, ".github/workflows/release.yml")
	checks := map[string]bool{
		"tag trigger":         strings.Contains(content, "tags:"),
		"builds caeser":       strings.Contains(content, "./cmd/caeser"),
		"stamps version":      strings.Contains(content, "-X main.version="),
		"publishes release":   strings.Contains(content, "action-gh-release"),
		"publishes checksums": strings.Contains(content, "SHA256SUMS"),
	}
	for check, passed := range checks {
		if !passed {
			t.Errorf("release workflow check failed: %s", check)
		}
	}
}

func TestRepoConfigExample(t *testing.T) {
	content := readWorkflow(t, ".caeser.yml")
	if !strings.Contains(content, "command:") {
		t.Fatal(".caeser.yml must document the command key")
	}
}

// mapKey returns the value node for key in a mapping node.
func mapKey(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
