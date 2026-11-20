# Ceaser release readiness

Status: **NO-GO.** All release-blocking engineering regressions are resolved and
validated end-to-end (see §13), but the validation criterion "actual generated
comments from all seven demo PRs" is unmet: only one curated demo PR exists and
can be diagnosed. Details and the shortest path to GO are below. Nothing is
published; no tag was created; no production branch was modified.

## 1. Executive summary

This cycle made the existing Ceaser intelligence more actionable without
changing verification. Shipped on a review branch: evidence-ordered
likely-function ranking, implementation-first candidate report selection,
concrete panic-text extraction, and a rewritten PR comment (primary suspect →
other leads → what to investigate). On the frozen 16-case Go corpus the
likely-function top-1 rose from 5/16 to 8/16 (function) with the misleading
primary rate falling from 7 to 5; candidate recall rose from 9 to 11. The
changes are covered by new unit tests and pass the full suite, vet, build,
gofmt and the repo's workflow-validation tests.

Two discovery mechanisms (call-admission depth, dependency-package indexing
scope) are investigated and documented but deliberately **not** shipped: they
cannot be validated end-to-end against the frozen reports without re-cloning
upstream repositories, and shipping them unvalidated risks noise. The engine
changes are validated offline and locally, but a public release additionally
requires validating the engine end-to-end in CI, which requires publishing a
version (a release action withheld pending approval) and is currently further
blocked by GitHub failing to create the demo workflow runs.

## 2. Everything implemented

Commits (see §9). In order:

1. `diagnosis: rank likely-function leads by evidence, not provenance kind` —
   the shortlist preserves the candidate tier's evidence order, promotes
   operand-correlated SSA leads (labelled leads, low confidence), and orders
   observed stack frames ahead of runtime/testing/dependency frames. Regression
   tests for operand correlation and framework filtering.
2. `eval: add offline likely-function ranking harness` — `cmd/likely-bench`
   rebuilds the likely-function tier (and optionally the candidate tier) from
   saved reports to compare changes with identical inputs; documents the
   coverage/hit/misleading reconciliation identity.
3. `diagnosis: surface the concrete panic text and next checks` — extracts the
   real panic/exception line in the Go test and generic-runtime paths; adds a
   "What to investigate" block to the PR comment from `root_cause.next_actions`.
4. `diagnosis: keep implementation candidates ahead of test files` — the
   bounded candidate report keeps non-test candidates ahead of `_test.go`
   entries before the limit.
5. `eval: rebuild candidate tier in likely-bench for discovery runs` — enables
   offline evaluation of candidate-selection changes.
6. `ci: lead the PR comment with the primary suspect and other leads` —
   restructures the comment.

Also produced (not shipped): `evaluation/discovery-findings.md`, this file,
`docs/releases/v9.2.md`, `CHANGELOG.md`.

## 3. Before-and-after evaluation metrics

Frozen 16-case corpus, ranking stage, identical inputs (`cmd/likely-bench`):

| Metric | Before | After |
| --- | ---: | ---: |
| Coverage (primary present) | 14/16 | 14/16 |
| Top-1 (function) | 5 | 8 |
| Top-1 (function or file) | 7 | 9 |
| Misleading primary | 7 (50.0% of covered) | 5 (35.7%) |
| Non-stack cases → top-1 | 3/9 | 3/9 |
| Raw-stack baseline (culprit in log) | 7/16 | 7/16 |
| Static primaries (hit) | 11 (5) | 11 (7) |
| Probe primaries (hit) | 3 (2) | 3 (2) |

Candidate-selection stage (`--rebuild-candidate-report`):

| Metric | Before | After |
| --- | ---: | ---: |
| Candidate recall (function) | 9 | 11 |
| Candidate recall (function or file) | 11 | 14 |
| Top-1 (function or file) | 9 | 10 |
| Top-3 (function or file) | 11 | 13 |
| Coverage | 14 | 16 |
| Misleading primary | 5 | 6 |
| Non-stack → top-1 | 3 | 4 |

Changed primaries (all from removing a coarse-kind bucket that demoted a ranked
source candidate): `go-u005`, `go-u008`, `go-u011`, `go-u014`. Identification
accuracy and verification behaviour are unchanged.

## 4. Actual generated comments from the demo PRs

A curated demo PR (`caeser-org/ceaser-demo#1`, gin routing-tree panic) was run
through the updated caller workflow and produced this real comment
(`github-actions[bot]`, run 44649265901):

```markdown
## Ceaser diagnosis

**What failed**

TestLateRouteRegistrationSkippedNodes — TestLateRouteRegistrationSkippedNodes
runtime exception — runtime exception or panic was reported

**Likely function to inspect**

**Primary suspect**

[observed_stack] tree.go:418 node.getValue — candidate derived from an observed stack location [confidence: medium] [Evidence: E-frame-d6efd1043f06, E-source-da543fcd8f80]

**Other ranked leads**

[observed_stack] gin.go:719 Engine.handleHTTPRequest — candidate derived from an observed stack location [confidence: medium] [...]
[observed_stack] gin.go:691 Engine.ServeHTTP — candidate derived from an observed stack location [confidence: medium] [...]

**What to investigate**

- run the smallest failing test in a network-disabled sandbox
- compare the same targeted test on base and head
- collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective

These are ranked investigation leads, not verified root causes. Root cause not verified.
```

Notes:
- The primary suspect is now the **innermost application frame**
  (`tree.go:418 node.getValue`), addressing the earlier reviewer concern that a
  caller was ranked above the panic site.
- "What failed" still shows the generic runtime-exception line **because the
  reusable workflow installs the released v9.1 binary by default**; the panic
  text fix is in the engine and only appears once a release carries it. The
  source-built path (below) is how the engine change is meant to reach users.
- **Resolved (see §13):** after this run, demo runs briefly began failing at
  creation with GitHub's "workflow file issue". The cause was an organization
  rename (`ceaser-org` → `caeser-org`) that GitHub Actions `uses:` does not
  follow. After updating the owner and fixing a nondeterministic candidate
  order, the engine changes were confirmed end-to-end in CI (run `44824631301`):
  the comment shows the real panic text and the stable primary suspect
  `tree.go:418 node.getValue`.
- The other six open PRs in `caeser-org/ceaser-demo` are Dependabot bumps, not
  curated demos; their CI passes, so they produce no diagnosis. Creating the
  remaining curated demos is the outstanding validation item (§12).

A source-build throwaway repository (`Yashbhu/ceaser-eval`, public, reversible)
lets the workflow build the unreleased engine, because the reusable workflow's
`ceaser_version: main` resolves `refs/heads/main` of `ceaser_repository` and the
engine changes are not on any released ref.

## 5. Unseen-corpus validation

No unseen corpus with saved reports exists in the repository. The `go-001…go-015`
and `case-00x` corpus cases have failure logs but no saved reports, and
regenerating them requires cloning upstream repositories (out of scope here).

Two genuine held-out reproductions were evaluated with the offline harness
(`--rebuild-candidate-report`):

| Case | Culprit | Result |
| --- | --- | --- |
| gin skipped-nodes | `tree.go node.getValue` | top-1 correct |
| koanf float64 | `koanf.go toFloat64` | top-3 (rank 2, SSA dependency lead) |

The koanf case is a concrete example of localization beyond the stack: the
culprit is an assertion-dependency function surfaced by SSA, not a panic frame.
n=2 is a smoke check, not statistical validation.

## 6. Regressions discovered and resolved

- **Fix B precision trade-off.** Candidate-selection reordering raised coverage
  (14→16) and misleading primaries (5→6). Analysis: the precision rate is
  approximately flat (35.7%→37.5%) and the added leads are low-confidence
  structural leads consistent with existing behaviour; the single added
  misleading case (`go-u004`) is the cross-package scope limitation. Kept and
  documented rather than shipped silently.
- **Package-scope regression (`go-u004`).** Documented; the generalizable fix is
  mechanism C (not shipped).
- **CI run-creation failure.** Unresolved (see §4). Release-blocking.
- No test, vet, build or formatting regressions.

## 7. Remaining limitations

- Mechanism A (function admission depth): `GoSlice.Index` calls `toValue`
  directly, but the verified-call expansion does not admit `toValue` (edge
  absence/provenance); not fixed.
- Mechanism C (dependency-package indexing scope): targeted indexing selects
  CI-reported and PR-relevant files only; a culprit in another package is not
  indexed. Not fixed. Design in `evaluation/discovery-findings.md`.
- No verified-mechanism or root-cause behaviour changed; the probe/shadow
  verification gates remain as documented.
- One of seven demo PRs validated end-to-end in CI; the rest blocked.
- Toolchain: `govulncheck` reports Go standard-library advisories for
  `go1.26.2` (fixed in `go1.26.3`/`go1.26.6`). No called third-party module
  vulnerability.

## 8. Test, build and security-check results

| Check | Result |
| --- | --- |
| `go build ./...` | pass |
| `go test ./...` | 21 packages ok, 0 FAIL |
| `go vet ./...` | clean |
| `gofmt -l internal cmd` | clean |
| repo workflow-validation tests (`go test .`) | pass |
| install: `ceaser version`, `ceaser init --yes` in a clean repo | generated `.ceaser.yml` + `.github/workflows/ceaser.yml` correctly |
| `govulncheck ./...` | 22 stdlib advisories (toolchain), 0 in called third-party code |
| `gosec` / `staticcheck` | not installed in this environment |

## 9. Commit hashes

Local `main`, nine commits ahead of `origin/main` (3baff34):

```
0d8187a fix: point repository references at the renamed owner (caeser-org)
2ad4268 localization: make candidate order deterministic and prefer innermost frames
03d1584 docs: release notes, changelog and release-readiness handoff
357017d ci: lead the PR comment with the primary suspect and other leads
174a15c eval: rebuild candidate tier in likely-bench for discovery runs
7ff5bc8 diagnosis: keep implementation candidates ahead of test files
8f5f332 diagnosis: surface the concrete panic text and next checks
46492b3 eval: add offline likely-function ranking harness
9178eeb diagnosis: rank likely-function leads by evidence, not provenance kind
```

Pushed to review branch `caeser-org/ceaser` `release/v9.2-eval` at `0d8187a`.
No changes to `main`; no tags.

## 10. Exact release commands

After approval and only once §4/§5 validation is complete:

```sh
# 1. Merge the review branch to main (or fast-forward) and confirm CI is green.
git checkout main && git merge --ff-only release/v9.2-eval && git push origin main

# 2. Validate locally.
go build ./... && go test ./... && go vet ./... && gofmt -l internal cmd

# 3. Tag — this triggers .github/workflows/release.yml, which builds the
#    binaries, verifies `ceaser version == ceaser <tag>`, and publishes the
#    release with SHA256SUMS.
git tag v9.2 && git push origin v9.2

# 4. Confirm the published assets and checksums.
gh release view v9.2
```

Rollback: `git push origin --delete v9.2` before promotion; the demo caller
already pins a ref and can be repointed to `v9.1`.

## 11. Proposed release notes

See `docs/releases/v9.2.md`. Summary: evidence-ordered likely-function ranking,
implementation-first candidate report, concrete failure text, and an actionable
PR comment (primary suspect → other leads → what to investigate). No change to
verification or promotion.

## 12. GO / NO-GO

**NO-GO**, with all release-blocking engineering regressions resolved. The
single remaining blocker is validation coverage, not product correctness.

Resolved and validated this cycle:

- Cross-repo reusable-workflow resolution (organization rename) — §13.
- Nondeterministic candidate ordering (release-blocking: identical input could
  yield a different primary suspect) — §13.
- Engine changes confirmed end-to-end in CI on the curated demo: the generated
  comment shows the real panic text and the correct, stable primary suspect
  `tree.go:418 node.getValue`.

Remaining before GO:

- **Demo coverage.** Only one curated demo PR exists (`ceaser-demo#1`); the other
  six open PRs are Dependabot bumps whose CI passes, so they produce no
  diagnosis. The criterion "actual generated comments from all seven demo PRs"
  requires creating six more curated reproductions (distinct failures in fitting
  repositories). This is the blocker.
- **Unseen-corpus breadth.** No fitted unseen corpus with saved reports exists in
  the repository; held-out validation is two reproductions (gin, koanf).
- **Toolchain.** `govulncheck` flags standard-library advisories for `go1.26.2`;
  the release build should pin `go1.26.6`+.
- **Owner identity assumption.** The fix standardizes on the currently
  resolvable owner `caeser-org`. If the organization is intended to be
  `ceaser-org` (i.e. the rename is to be reverted), the references must be
  updated again.

Shortest path to GO: (a) add six curated demo PRs (at least one non-stack case,
e.g. koanf `toFloat64`) and confirm each produces a clear, correct comment;
(b) pin the release toolchain to `go1.26.6`+; (c) re-run the frozen corpus and
held-out cases on the release build. Then the release commands in §10 apply.

## 13. Blocker resolution log (2026-11-21)

### Blocker 1 — GitHub Actions "workflow file issue" (resolved)

- **Symptom:** new `Ceaser` runs were created with the workflow *path* as the
  run name, no jobs, conclusion `failure`; message "This run likely failed
  because of a workflow file issue".
- **Isolation:** a minimal caller in the same repo produced the same failure;
  a caller using a **same-repo** reusable workflow (`./.github/workflows/...`)
  succeeded. So the caller file was valid and the fault was **cross-repo
  reusable-workflow resolution**.
- **Cause:** `gh api repos/ceaser-org/ceaser` returns
  `full_name: caeser-org/ceaser` — the **organization was renamed
  `ceaser-org` → `caeser-org`**. GitHub REST follows the rename, but GitHub
  Actions `uses: ceaser-org/...` does not resolve the old owner, so the run is
  rejected. The same file that succeeded before the rename (commit `74644750`)
  failed afterward with byte-identical content (verified with `cmp`).
- **Fix:** update all repository references to the canonical owner
  `caeser-org` (action, install script, reusable workflows, `ceaser init`
  template, tests and docs; 43 occurrences). See commit `0d8187a`.
- **Evidence:** `ceaser init --yes` in a clean repo now emits
  `uses: caeser-org/ceaser/.github/workflows/ci-diagnose.yml@v9.1`, and the
  demo workflow run `44824631301` completed with conclusion `success`.

### Blocker 2 — nondeterministic primary suspect (resolved)

- **Symptom:** the same demo input produced different primary suspects across
  runs (`tree.go node.getValue` vs `gin.go Engine.ServeHTTP`).
- **Cause:** `localization.Generate` collected candidates from Go maps and then
  stable-sorted by score, so equally-scored observed stack frames kept a
  **randomized** map order. For a panic, all frames share the execution score.
- **Fix:** deterministic discovery ordinal plus a score-then-ordinal sort
  (frames are added in stack order, so the innermost frame is stable and
  preferred). Commit `2ad4268`, with determinism regression test
  `TestGenerateOrdersObservedFramesDeterministically` (8 iterations).
- **Evidence:** the final demo comment (run `44824631301`) shows
  `Primary suspect: [observed_stack] tree.go:418 node.getValue` with
  `handleHTTPRequest`/`ServeHTTP` as alternates, and the real panic text
  `panic: runtime error: slice bounds out of range [:2] with capacity 1`.

### Not resolved

- Demo coverage (1 of 7 curated). Six Dependabot PRs produce no diagnosis.
- Mechanism A (admission depth) and mechanism C (dependency scope) remain
  investigated-but-unshipped (`evaluation/discovery-findings.md`).
