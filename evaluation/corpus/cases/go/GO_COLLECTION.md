# Go PR Failure Corpus — Collection Guide

This directory holds **Go-specific** failure cases for the Caeser evaluation
corpus. Each case is a real public Go PR failure with a ground-truth diagnosis.
None of the files below contain real PR data yet — they are templates that need
to be filled in with actual Go failures.

## Required case layout

Each case lives in its own directory under `cases/go/`:

```
cases/go/<case-id>/
  metadata.yaml   # public case metadata (no ground truth)
  expected.yaml   # ground-truth diagnosis (scorer only)
  failure.log     # raw CI failure log (SHA-256 must match metadata)
```

## Field reference

### metadata.yaml (public — runners see this)

| Field | Required | Description |
|-------|----------|-------------|
| `id` | yes | Stable case ID, e.g. `go-001-compile-failure` |
| `tier` | yes | `log_only` (no base/head) or `full` (base+head comparison) |
| `category` | yes | `regression`, `pre_existing`, `dependency`, `flaky`, `infrastructure`, `not_reproduced` |
| `repo` | yes | Canonical repo URL, e.g. `https://github.com/golang/go` |
| `run_url` | yes | Public GitHub Actions / CI run URL |
| `base_run_url` | full only | Base CI run URL for regression comparison |
| `log_sha256` | yes | Lowercase hex SHA-256 of `failure.log` (loader verifies) |
| `head_sha` | full only | 40-char buggy commit SHA |
| `base_sha` | full only | 40-char known-good commit SHA |
| `command` | full only | Exact CI command as a list of strings |
| `license_note` | yes | Redistribution permission statement |
| `redacted` | yes | `true` if the log has been redacted |
| `log_cache_path` | no | Alternate path to cached log if not in repo |

### expected.yaml (scorer only — never sent to the model)

| Field | Required | Description |
|-------|----------|-------------|
| `primary_failure` | yes | The exact failure string from the log |
| `category` | yes | Must match metadata category |
| `expected_outcome` | yes | `answerable` or `insufficient_evidence` |
| `culprit_commit` | no | SHA of the commit that introduced the bug |
| `culprit_file` | no | File containing the root cause |
| `culprit_package` | no | Go package path containing the root cause |
| `evidence_source` | yes | What established the ground truth (fix commit, maintainer comment, etc.) |
| `annotator` | yes | Who annotated this case |
| `disagreement_notes` | no | Caveats or alternative interpretations |

### failure.log

Raw CI output. Must match `log_sha256` in metadata.yaml exactly. If redacted,
note what was removed in `disagreement_notes`.

## Target coverage (15–20 cases)

| # | Category | Example failure mode |
|---|----------|---------------------|
| 1 | regression | Compile/build failure after a PR |
| 2 | regression | Panic in Go runtime or stdlib |
| 3 | regression | Assertion failure (`require`, `assert`, `t.Fatal`) |
| 4 | dependency | `go.sum`/`go.mod` mismatch or bad dependency version |
| 5 | infrastructure | Toolchain version mismatch (Go toolchain) |
| 6 | regression | Timeout in test suite |
| 7 | pre_existing | Same failure on base and head |
| 8 | flaky | Test passes sometimes, fails sometimes |
| 9 | regression | Deterministic PR regression (reverted by fix commit) |
| 10-12 | regression | Additional Go regressions (stdlib, crypto, net, etc.) |
| 13 | experimental | Race condition (`-race` detector) |
| 14 | experimental | Deadlock in Go code |
| 15-20 | any | Additional cases to reach 15–20 total |

## Collection process

1. Find a real public Go PR with a failed CI run.
2. Record the repo, PR number, run URL, head SHA, base SHA, and exact command.
3. Download the raw failure log. Compute `sha256sum failure.log`.
4. Write `metadata.yaml` with public fields only.
5. Diagnose the failure manually (read the fix commit, maintainer comments, source diff).
6. Write `expected.yaml` with the ground truth.
7. Add the case ID to `manifest.yaml` under the appropriate category.
8. Run `go test ./internal/evalcorpus/...` to verify the case loads and the log hash matches.

## Redistribution

Only include logs you have permission to redistribute. If a log cannot be
redistributed, set `redacted: true`, strip sensitive data, and note the
restriction in `license_note` and `disagreement_notes`. Public GitHub Actions
logs from public repositories are generally redistributable under the repo's
license — verify before including.

## Launch thresholds

Do not ship Caeser's diagnosis product until:
- No false "verified root cause" claims on the corpus
- Correct failure classification above 90%
- Patch verification required for verified diagnoses
- Unresolved output accepted when evidence is insufficient

## Example: adding a new case

```
mkdir -p evaluation/corpus/cases/go/go-001-compile-failure
# Write metadata.yaml, expected.yaml, failure.log
# Add entry to evaluation/corpus/manifest.yaml
go test ./internal/evalcorpus/...
```
