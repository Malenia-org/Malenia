# Committed-criteria probe rerun (16 Go cases)

Engine `ea50a5c` (snapshot of the working tree containing harness, memory, SSA
containment, candidate tier and probe-loop changes). Criteria frozen at
`c7293cb` in `evaluation/go-probe-acceptance.md`. Original reports/logs are in
`original-reference/` and prior result JSON files are preserved unchanged.

Full machine-readable record: `../results/go-probe-rerun-ea50a5c.json`.
Per-case artifacts (raw report, base failure log, head control, bundle):
`artifacts/<case-id>/`. Run URLs and commands are in each case record.

## Gate results

| Measure | Target | Observed |
|---|---:|---:|
| Fixed-head controls passed | 16 | 16 |
| Executed, preserved probes | ≥8 | **1** (go-u002) |
| Correlated invocations | ≥4 | 0 |
| Discriminating cases (≥3 repos) | ≥3 | 0 |
| Unsupported established mechanisms | 0 | 0 |
| Typed terminal outcomes | 16/16 | 16/16 |
| Mean diagnosis | — | 14.33 s |

The first missed target is **preserved probes**. Work on correlation comes only
after selection and preservation reach eight cases. Hunk reversion and release
preparation remain deferred.

## Where cases stop

- **13 cases — `no_candidate`** (go-u001, u004–u012, u014–u016 except u002/u003/u013):
  SSA is unavailable or exhausted and no observed stack/coverage line anchors
  a source operation. go-u006 has four runner-absolute stack frames that the
  source selector cannot normalize to the workspace; no coverage evidence was
  attached in any of these cases.
- **go-u002 — `candidate_limit`**: five operations probed at `mcp/tools.go`
  (255, 83, 250, 253, 258). The operation at line 255 produced the overflowing
  integer in all three runs and preserved the same failing check
  (`TestCallToolRequest_IntegerOutOfRange/exponent_above_int_range`), but no
  assertion-invocation correlation exists. The other four emitted no operation
  marker. Unresolved mechanism, not a verified cause.
- **go-u003 — `candidate_limit`**: repaired fixture qualified (base fails the
  nil-reader assertion, head passes). Five fan-out operations were probed; all
  three runs failed the correct test, but the preservation check returned no
  named failing check because the assertion is message-only, so these attempts
  are typed `failure_not_preserved`.
- **go-u013 — `candidates_exhausted`**: repaired fixture qualified (base fails
  `101 != 100`, head passes). `getNumThreads` observations (`4, 1, 4, 100, 101,
  1099511627776`) were identical across three runs, but the `got N, want M`
  message has no expected/actual pair in the accepted formats, so preservation
  also failed; a third attempt stopped at instrumentation.

## MinIO fixture repair

`../fixture-repairs/minio-2342/test-only.patch` was applied to the shared
redacted test and decoded into the harness as a SHA-256-verified override
(`41a7982b…`) on both revisions. No production hunk was supplied to diagnosis.
The same repaired test was used for go-u003 and go-u013.

## Remaining limitations

- `preservedProbeCheck` accepts only testify expected/actual pairs, the typed
  `got X (T), want Y (T)` form, and correlated native predicates. Message-only
  and untyped got/want assertions are not preserved even when the test fails
  identically in all three runs.
- Source-probe selection depends on normalized stack locations or coverage;
  most cases have neither, so no operation reaches instrumentation.
- No case produced a verified mechanism or an unsupported established claim;
  the reported classification is localization/observation only.
