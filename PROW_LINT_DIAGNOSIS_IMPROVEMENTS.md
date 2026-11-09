# Prow Lint Failure Diagnosis Improvements

## Summary

Fixed diagnosis quality issues for Prow lint failures (kubernetes/kubernetes#143709):

1. ✅ Added distinct `lint_failure_observed` classification
2. ✅ Preserved Prow job identity, source URL, and log location
3. ✅ Extracted only actual lint ERROR lines, not all warnings
4. ✅ Maintained clear distinction between observed failure and regression causality
5. ✅ Added regression test with actual Prow log fixture

## Before vs After

### Classification

**Before:**
```json
{
  "classification": "test_failure_observed",
  "claim": "CI evidence shows that a test or step failed in the recorded execution. This does not establish that the current change introduced the failure."
}
```

**After:**
```json
{
  "classification": "lint_failure_observed",
  "claim": "CI evidence shows that a linter or static analysis check failed in the recorded execution. This does not establish that the current change introduced the failure."
}
```

### Job Identity

**Before:**
```json
{
  "identity": {}
}
```

**After:**
```json
{
  "identity": {
    "workflow": "kubernetes",
    "job": "verify.golangci-lint-pr-hints",
    "step": "verify-golangci-lint-pr-hints.sh"
  }
}
```

### Extracted Findings

**Before:**
```json
{
  "observed_locations": [
    {
      "name": "observed location",
      "state": "failed",
      "file": "pkg/kubelet/kuberuntime/kuberuntime_manager.go",
      "line": 2227
    },
    {
      "name": "observed location",
      "state": "failed",
      "file": "pkg/kubelet/kuberuntime/kuberuntime_manager_test.go",
      "line": 7620
    }
  ]
}
```

**After:**
```json
{
  "lint_failures": [
    {
      "name": "lint: ST1023",
      "state": "failed",
      "message": "should omit type *runtimeapi.PodSandboxConfig from declaration; it will be inferred from the right-hand side (staticcheck)",
      "file": "pkg/kubelet/kuberuntime/kuberuntime_manager.go",
      "line": 2227,
      "evidence_id": "ci-evidence-2fda950548ab"
    },
    {
      "name": "lint: require-error",
      "state": "failed",
      "message": "for error assertions use require (testifylint)",
      "file": "pkg/kubelet/kuberuntime/kuberuntime_manager_test.go",
      "line": 7620,
      "evidence_id": "ci-evidence-2fda950548ab"
    }
  ]
}
```

## Changes Made

### 1. New Classification Type

Added `ClassLintFailureObserved` constant to distinguish lint failures from test failures:

```go
const (
    ClassRealRegression      Classification = "real_regression"
    ClassFlaky               Classification = "flaky"
    ClassDependencyDrift     Classification = "dependency_or_toolchain_drift"
    ClassInfrastructure      Classification = "infrastructure_or_environment"
    ClassInconclusive        Classification = "inconclusive"
    ClassNotReproduced       Classification = "not_reproduced"
    ClassPreExisting         Classification = "pre_existing_failure"
    ClassTestFailureObserved Classification = "test_failure_observed"
    ClassLintFailureObserved Classification = "lint_failure_observed"  // NEW
)
```

### 2. Enhanced Evidence Provenance

Extended `EvidenceProvenance` to capture source URLs and log locations:

```go
type EvidenceProvenance struct {
    Provider    string    `json:"provider,omitempty"`
    URI         string    `json:"uri,omitempty"`
    SourceURL   string    `json:"source_url,omitempty"`   // NEW: Original CI job URL
    LogURL      string    `json:"log_url,omitempty"`      // NEW: Direct log URL
    Artifact    string    `json:"artifact,omitempty"`
    CollectedAt time.Time `json:"collected_at,omitempty"`
}
```

### 3. Prow Job Identity Extraction

Added functions to extract Prow job metadata from log headers:

```go
func extractProwIdentity(content string) CIIdentity {
    // Extracts job name from "+++ Running case: verify.golangci-lint-pr-hints"
    // Extracts step name from "Verifying verify-golangci-lint-pr-hints.sh"
    // Extracts workflow name from working directory
}
```

### 4. Lint-Specific Failure Extraction

Added pattern matching for lint ERROR lines:

```go
var lintErrorLine = regexp.MustCompile(`^ERROR:\s+([A-Za-z0-9_./-]+\.(?:go|rs|java|py|js|ts|c|cc|cpp|h)):(\d+)(?::(\d+))?:\s+([^:]+):\s+(.+)$`)

func extractLintFailures(content, evidenceID string, state ObservationState) []TestObservation {
    // Only extracts lines starting with "ERROR:", not all warnings
    // Returns TestObservation with:
    //   - name: "lint: {linter-name}" (e.g. "lint: ST1023")
    //   - message: full lint message
    //   - file, line: precise location
}
```

### 5. Classification Logic

Enhanced `classifyNormalizedEvidence` to detect lint failures:

```go
func classifyNormalizedEvidence(input NormalizedEvidence, baseline Baseline) Classification {
    class := Classify(baseline.Results)
    if class == ClassRealRegression {
        // Check if this is specifically a lint failure
        for _, exec := range input.Executions {
            for _, test := range exec.Tests {
                if strings.HasPrefix(test.Name, "lint:") {
                    return ClassLintFailureObserved  // NEW
                }
            }
        }
        return ClassTestFailureObserved
    }
    return class
}
```

### 6. Regression Test

Added comprehensive test `TestProwLintFailureClassificationAndTraceability` that validates:
- Correct classification as `lint_failure_observed`
- Prow job identity extraction
- Precise lint failure location extraction
- Provenance preservation
- No false causality claims

## Test Results

```bash
$ go test ./internal/diagnosis -run TestProwLintFailure -v
=== RUN   TestProwLintFailureClassificationAndTraceability
--- PASS: TestProwLintFailureClassificationAndTraceability (0.00s)
PASS
ok      github.com/causent/malenia/internal/diagnosis   0.293s
```

All existing tests continue to pass:
```bash
$ go test ./...
ok      github.com/causent/malenia/internal/diagnosis   3.745s
[all other packages pass]
```

## Developer Impact

### Traceable Evidence

Developers now see:
1. **Specific lint check that failed**: "lint: ST1023" vs generic "observed location"
2. **Exact error message**: Full staticcheck/testifylint explanation
3. **Job context**: Which Prow job/step failed
4. **Precise location**: File and line tied to the actual ERROR line

### Example Developer View

For the Kubernetes PR #143709 failure, Malenia now reports:

```
Classification: lint_failure_observed

Prow Job: kubernetes / verify.golangci-lint-pr-hints / verify-golangci-lint-pr-hints.sh

Lint Failures:
1. pkg/kubelet/kuberuntime/kuberuntime_manager.go:2227
   ST1023 (staticcheck): should omit type *runtimeapi.PodSandboxConfig from declaration
   
2. pkg/kubelet/kuberuntime/kuberuntime_manager_test.go:7620
   require-error (testifylint): for error assertions use require
```

### No False Causality

The claim correctly states:
> "CI evidence shows that a linter or static analysis check failed in the recorded execution. **This does not establish that the current change introduced the failure.**"

Without base/head comparison, Malenia avoids claiming the PR caused these lint failures.

## Files Changed

- `internal/diagnosis/diagnosis.go`: Added `ClassLintFailureObserved`
- `internal/diagnosis/evidence.go`: Enhanced provenance, added Prow identity extraction, lint failure parsing
- `internal/diagnosis/classify.go`: Added lint classification statement and confidence
- `internal/diagnosis/evidence_test.go`: Added regression test
- `testdata/prow-lint-failure/ci.log`: Real Prow log fixture

## Constraints Satisfied

✅ No new subsystems  
✅ No GitHub App work  
✅ No executor, cache, or indexing changes  
✅ No new milestones  
✅ No additional public workflow runs  
