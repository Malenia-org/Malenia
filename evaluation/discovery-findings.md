# Candidate-discovery findings (frozen 16 cases)

Investigation of why reviewed culprits never reached the candidate tier for
`go-u007`, `go-u010`, `go-u016`, `go-u004`, `go-u015`. No root-cause
verification, confidence, or abstention semantics were changed.

Method: for every case the reviewed culprit (scoring-only labels from
`go-probe-preflight.md`) was checked against three stages, using each report's
own fields:

- `in_likely` — culprit file present in `localization.likely_files`
- `in_cands` — culprit function admitted to `report.candidates`
- `in_report` — culprit function inside `candidate_report.entries` (what the
  likely-function tier consumes)

## Pipeline trace

`failure frames → enclosing symbol → seeds → call expansion (MaxDepth=2,
verified_static_call only) → failing-test inferred callees → Rank → truncate to
MaxCandidates (25 with CI evidence) → BuildCandidateReport (5-entry report) →
likely-function tier (function required; operand-correlated SSA promoted)`.

Seeds are only (a) the enclosing symbol of each failure frame and (b) `Test`
nodes named by the failure text. Expansion is by AST-verified `CALLS` edges to
depth 2, plus the failing test's directly inferred `TESTS`/`CALLS` callees.
With no app stack frame (assertion failures) the only execution seed is the
failing test, and SSA is the only value-dependency path.

## What the corpus shows (all 16)

| case | ssa | in_likely | in_cands | in_report |
| --- | --- | --- | --- | --- |
| go-u007 | unavailable | yes | **no** | no |
| go-u010 | none | yes | **no** | no |
| go-u016 | unavailable | yes | yes | **no** |
| go-u004 | unavailable | no | no | no |
| go-u015 | unavailable | yes | yes | **no** |

Across all 16: 14/16 localized the culprit file; 12/16 admitted the culprit
function to `report.candidates`; only 10/16 kept it in `candidate_report.entries`.

## Three distinct mechanisms

**A. Function admission depth (u007, u010).** The culprit file is localized
(rank 1 for u007), but the culprit function is a deeper helper reached only
through non-verified or deeper calls (`hashCodeForRow`, `toValue`). With
`MaxDepth=2` and expansion restricted to `verified_static_call` edges, and no
usable SSA trace, no function candidate is admitted from the localized file.
Loss type: **traversal budget + verified-call-only expansion + missing SSA**.

**B. Bounded-report selection (u015, u016).** The culprit *is* admitted to
`report.candidates` (`reporter.CreatePosthogPropertiesForSettings` rank 7;
`api_hash.go NullsafeHashcode128` rank 6) but the 5-entry `candidate_report`
was filled by the failing test's own file and `inferred_callee` entries first.
Loss type: **candidate selection** — test-file entries crowding out
implementation candidates. Not missing information.

**C. Analysis scope (u004).** The reviewed culprit lives in another package
(`config/config.go Config.Clone`) that targeted indexing did not include; every
candidate is in the failing package. Loss type: **missing information at the
snapshot level**.

## Generalizable fixes

- **B (implemented).** Implementation candidates must not be crowded out of the
  bounded report by the failing test's own file. `BuildCandidateReport` now
  keeps non-test candidates ahead of test-file entries before applying the
  5-entry limit. This is a selection fix over evidence already admitted — it
  discovers no new candidates.
- **A (not implemented).** When no function candidate can be admitted from a
  localized file, consider the localized file's enclosing/declared functions, or
  extend expansion by one hop only along the failing test's call chain. Must be
  validated for precision, not just recall.
- **C (not implemented).** Extend targeted indexing to the direct dependency
  packages on the failing test's call path. Cost-bearing; validate separately.

## Validation of fix B (frozen 16, ranking already fixed)

`go run ./cmd/likely-bench --rebuild-candidate-report` before/after:

| Metric | Before | After |
| --- | ---: | ---: |
| candidate recall (function) | 9 | **11** |
| candidate recall (function or file) | 11 | **14** |
| top-1 (function) | 8 | 8 |
| top-1 (function or file) | 9 | **10** |
| top-3 (function or file) | 11 | **13** |
| coverage | 14 | **16** |
| misleading primary | 5 | 6 |
| non-stack cases → top-1 | 3 | **4** |
| raw-stack baseline | 7/16 | 7/16 |

Net: +2 candidate recall, +1 top-1, +2 top-3, +1 non-stack top-1, at the cost of
**+1 misleading primary**. The regression is `go-u004`, mechanism C: surfacing
the failing package's implementation candidates converts an abstention into a
wrong primary when the culprit is in an unindexed dependency package. Fix B is
net-positive on discovery but should ship alongside the scope work (C), or with
a same-package guard, so it does not trade abstention for a misleading lead.

## Remaining open items

- u007/u010: mechanism A (admission depth) — unaffected by fix B.
- u004: mechanism C (scope) — the one regression fix B introduces.
- u016: culprit now in the report but not primary — a ranking problem, not
  discovery.
