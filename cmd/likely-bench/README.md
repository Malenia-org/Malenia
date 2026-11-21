# likely-bench

Offline evaluation harness for the **likely-function ranking stage**.

It loads saved Caeser reports and rebuilds the likely-function tier from each
report's own candidate tier and SSA trace. Because ranking is a pure function of
those inputs, this compares a ranking change **before/after with identical
inputs** and isolates ranking from candidate discovery. It does **not**
re-diagnose.

```sh
go run ./cmd/likely-bench --artifacts evaluation/probe-rerun/artifacts --out /tmp/rank.json
```

Output: one record per case with the status, primary, alternates, candidate
counts and SSA status. Score it against reviewed labels with the rules below.

## Metric definitions

Every case has a reviewed culprit **file** and one or more culprit **function**
names (scoring only; taken from `evaluation/go-probe-preflight.md`, never used
to tune ranking). A primary suspect matches when:

- `fn_hit` — the primary function contains a reviewed culprit function name
  (receiver-qualified substring, so `bootTimeCollector.Update` does not match
  `textFileCollector.Update`), **or**
- `file_hit` — the primary file basename equals a reviewed culprit file.

Then, per case: `hit = fn_hit OR file_hit`.

| Metric | Definition |
| --- | --- |
| coverage | cases with a primary suspect (`status = available`) |
| fn_top1 | primaries matching a culprit function |
| file_top1 | primaries matching a culprit file |
| hit_top1 | primaries matching a culprit function **or** file |
| misleading_primary | `coverage − hit_top1`: a presented primary that matches neither |
| non-stack cases | reviewed culprit file absent from the raw failure log |
| nonstack_success | non-stack cases where `hit_top1` |
| rawstack_baseline_success | cases whose culprit file appears in the raw log (what reading the log alone reaches) |
| static / probe | primaries split by `probe_operation` vs any static kind |

### Reconciliation identity

```
hit_top1 + misleading_primary = coverage
cases − coverage             = abstentions (a miss, but not misleading)
```

So abstention can never make the misleading rate look better: it lowers
coverage, which is reported next to the rate. `fn_top1 ≤ hit_top1` because a
file match without a function match still counts as a hit.

## Discovery mode

`--rebuild-candidate-report` rebuilds the candidate tier from each report's own
fields (candidates, failure incidents, localization, mechanism observation)
instead of using the saved tier. This evaluates **candidate-selection** changes
the same way, with everything else held fixed:

```sh
go run ./cmd/likely-bench --rebuild-candidate-report --out /tmp/discovery.json
```

Each record includes all report entries, so candidate recall (culprit present in
the implementation entries) can be scored alongside top-1/top-3. See
`evaluation/discovery-findings.md` for the recorded before/after.

## Recorded run — frozen 16 cases (2026-11-20)

Ranking change: preserve the candidate tier's evidence order, promote
operand-correlated SSA leads, push framework stack frames behind application
frames. Root-cause verification, promotion and abstention unchanged.

| Metric | Before | After |
| --- | ---: | ---: |
| coverage | 14/16 | 14/16 |
| fn_top1 | 5 | 8 |
| file_top1 | 7 | 9 |
| hit_top1 | 7 | 9 |
| misleading_primary | 7 (50.0% of covered) | 5 (35.7% of covered) |
| static primaries (hit) | 11 (5) | 11 (7) |
| probe primaries (hit) | 3 (2) | 3 (2) |
| non-stack cases (success) | 9 (3) | 9 (3) |
| rawstack_baseline_success | 7/16 | 7/16 |

Changed primaries (all for the same reason — the old code demoted a ranked
source candidate below resolved call edges): `go-u005`, `go-u008`, `go-u011`,
`go-u014`. The operand-correlation and framework-ordering logic changed no
primary on this corpus (traces unavailable/`budget_exhausted`; only one case has
observed stack locations), which is why `internal/diagnosis/likely_function_test.go`
adds direct regression tests for both.
