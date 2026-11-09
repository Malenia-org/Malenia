# Final Implementation - Prow Lint Failure Diagnosis

## Three Inconsistencies Fixed

### 1. ✅ Provenance Path Clarified

**Issue**: Report showed `provider: offline` and local URI, but test claimed Prow URLs would be present.

**Root Cause**: CLI mode reads local files and cannot populate remote URLs. Only workflow integration (`.github/workflows/try-public-pr.yml`) has access to Prow/GitHub APIs to populate `SourceURL` and `LogURL`.

**Fix**:
- Explicitly documented in test: "In workflow mode, SourceURL and LogURL would be populated from GitHub/Prow APIs. In CLI/offline mode, only URI (local path) is available."
- Test now validates `provider: offline` and `uri: testdata/...` (matches actual output)
- Test explicitly checks that `SourceURL` is empty in offline mode

**Result**: Test expectations match actual CLI behavior. Workflow integration path preserved for future use.

### 2. ✅ Identity Inheritance Fixed

**Issue**: Each `TestObservation` had empty `identity: {}` even though parent `CIExecution` had Prow identity.

**Fix**:
- Modified `extractLintFailures()` to accept `identity` parameter
- Populate `TestObservation.Identity` from parent execution
- Updated fallback path to also populate identity
- Added test validation that each finding has identity populated

**Result**:
```json
{
  "identity": {
    "workflow": "kubernetes",
    "job": "verify.golangci-lint-pr-hints",
    "step": "verify-golangci-lint-pr-hints.sh"
  },
  "name": "lint: ST1023",
  "file": "pkg/kubelet/kuberuntime/kuberuntime_manager.go",
  "line": 2227,
  "log_line": 222
}
```

Each finding now has complete provenance.

### 3. ✅ Irrelevant Uncertainty Removed + Confidence Clarified

**Issues**:
- Report included bisect/git uncertainties irrelevant to evidence-only mode
- No distinction between claim confidence (0.45) and overall confidence (0.35)
- Missing guidance that lint-hints findings may be acceptable

**Fixes**:
1. **Conditional uncertainty**: Only add bisect uncertainties when `bisect_status` doesn't contain "deferred"
2. **Lint-hints guidance**: Added uncertainty: "lint-hints checks may report findings that maintainers consider acceptable to merge; consult project guidelines before assuming all findings must be fixed"
3. **Confidence distinction**:
   - **Claim confidence (0.45)**: Confidence that a lint failure occurred
   - **Overall confidence (0.35)**: Lower because no base/head comparison, no reproduction, no source analysis

**Result**:
```json
{
  "claims": [{
    "statement": "CI evidence shows that a linter or static analysis check failed...",
    "confidence": 0.45
  }],
  "confidence": 0.35,
  "uncertainty": [
    "classification is based on normalized CI evidence",
    "a CI lint failure was observed, but no base-versus-head comparison confirms this change introduced it",
    "lint-hints checks may report findings that maintainers consider acceptable to merge"
  ]
}
```

No irrelevant bisect/localization uncertainties appear.

---

## Final Generated JSON

**File**: `FINAL_DIAGNOSIS_OUTPUT.json`

### Key Improvements

**1. Complete Provenance Chain**:
```json
{
  "file": "pkg/kubelet/kuberuntime/kuberuntime_manager.go",  // Source code
  "line": 2227,                                               // Source line
  "log_line": 222,                                            // CI log line
  "identity": {                                               // Prow job
    "workflow": "kubernetes",
    "job": "verify.golangci-lint-pr-hints",
    "step": "verify-golangci-lint-pr-hints.sh"
  },
  "evidence_id": "ci-evidence-2fda950548ab"                   // Links to raw log
}
```

**2. Accurate Provenance Representation**:
```json
{
  "provenance": {
    "provider": "offline",
    "uri": "testdata/prow-lint-failure/ci.log"
    // Note: SourceURL and LogURL only populated by workflow integration
  }
}
```

**3. Relevant Uncertainties Only**:
```json
{
  "uncertainty": [
    "classification is based on normalized CI evidence",
    "a CI lint failure was observed, but no base-versus-head comparison confirms this change introduced it",
    "lint-hints checks may report findings that maintainers consider acceptable to merge"
  ]
  // No bisect/localization uncertainties (not applicable to evidence-only mode)
}
```

---

## Test Coverage

### New Focused Tests

Added to `TestProwLintFailureClassificationAndTraceability`:

**1. Provenance Validation**:
```go
// Offline mode uses local URI, not remote URLs
if normExec.RawEvidence[0].Provenance.Provider != "offline" {
    t.Errorf("expected offline provider for CLI fixture")
}
if normExec.RawEvidence[0].Provenance.SourceURL != "" {
    t.Error("SourceURL should be empty in offline mode")
}
```

**2. Identity Inheritance**:
```go
// Each TestObservation must have identity populated
for i, test := range normExec.Tests {
    if test.Identity.Job == "" {
        t.Errorf("test[%d] identity.Job is empty", i)
    }
    if test.Identity.Job != normExec.Identity.Job {
        t.Errorf("test[%d] identity mismatch", i)
    }
}
```

**3. Uncertainty Filtering**:
```go
// Bisect/localization uncertainties should not appear
for _, unc := range report.Uncertainty {
    if strings.Contains(unc, "bisect") || strings.Contains(unc, "first-bad") {
        t.Errorf("irrelevant localization uncertainty: %q", unc)
    }
}

// Verify lint-hints distinction is present
if !strings.Contains(strings.Join(report.Uncertainty, " "), "acceptable to merge") {
    t.Error("uncertainty should clarify lint-hints findings may be acceptable")
}
```

### Test Results

```bash
$ go test ./internal/diagnosis -run TestProwLintFailure -v
=== RUN   TestProwLintFailureClassificationAndTraceability
--- PASS: TestProwLintFailureClassificationAndTraceability (0.00s)
PASS
ok      github.com/causent/malenia/internal/diagnosis   0.295s
```

All existing tests also pass:
```bash
$ go test ./internal/diagnosis -v
PASS
ok      github.com/causent/malenia/internal/diagnosis   3.082s
```

---

## Changes Summary

**Files Modified**: 3
- `internal/diagnosis/evidence.go` (+2 lines: identity parameter, populate in observations)
- `internal/diagnosis/classify.go` (+4 lines: conditional uncertainty, lint-hints guidance)
- `internal/diagnosis/evidence_test.go` (+45 lines: provenance tests, identity tests, uncertainty tests)

**Total**: 51 LOC changed

**No**:
- New architecture
- New features
- New infrastructure
- Public workflow reruns

---

## Final Artifacts

### 1. Complete Generated JSON
**File**: `FINAL_DIAGNOSIS_OUTPUT.json`

Shows actual diagnosis with:
- Classification: `lint_failure_observed`
- 2 lint failures with complete provenance
- Identity populated in each finding
- Offline provenance (URI only, no remote URLs)
- Relevant uncertainties only
- Lint-hints guidance

### 2. Prow Log Evidence
**File**: `testdata/prow-lint-failure/ci.log` (15KB)

Key excerpts:
- Lines 1-5: Job header (identity extraction source)
- Lines 222, 225: Two ERROR diagnostics (extracted)
- Lines 223-224, 226-227: Code snippets (NOT extracted, correct)
- Lines 228-229: Summary "2 issues"
- Lines 245-249: Exit code 1, FAILED

### 3. Focused Tests
**Location**: `internal/diagnosis/evidence_test.go::TestProwLintFailureClassificationAndTraceability`

Validates:
1. ✅ Offline provenance correctness
2. ✅ Identity inheritance
3. ✅ Uncertainty filtering
4. ✅ Lint-hints guidance

---

## Developer View

From the final JSON, a developer sees:

```
Classification: lint_failure_observed (confidence: 0.45)
Overall diagnosis confidence: 0.35

Prow Job: kubernetes / verify.golangci-lint-pr-hints / verify-golangci-lint-pr-hints.sh

Lint Failures:

1. ST1023 (staticcheck)
   Source: pkg/kubelet/kuberuntime/kuberuntime_manager.go:2227
   Log: line 222 in ci.log
   Issue: should omit type *runtimeapi.PodSandboxConfig from declaration
   
2. require-error (testifylint)
   Source: pkg/kubelet/kuberuntime/kuberuntime_manager_test.go:7620
   Log: line 225 in ci.log
   Issue: for error assertions use require

Uncertainties:
- This is CI evidence only; no local reproduction or base/head comparison
- Lint failure observed, but causality not established
- Lint-hints findings may be acceptable per project guidelines

Note: Consult Kubernetes project lint policy before assuming these must be fixed.
```

**Actionable**: ✅ Developer knows what, where, and has proper context  
**Honest**: ✅ Clear about limitations and uncertainty  
**Traceable**: ✅ Can verify by checking log line 222, 225 in evidence

---

## Implementation Complete

All three inconsistencies resolved:
1. ✅ Provenance path clarified (offline vs workflow mode)
2. ✅ Identity populated in each finding
3. ✅ Irrelevant uncertainties removed, confidence distinction clear

**Total changes**: 51 LOC across 3 files  
**All tests pass**  
**No new infrastructure**  

Implementation stopped as requested.
