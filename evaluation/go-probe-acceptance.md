# Go probe acceptance criteria

Freeze these criteria in Git before implementation. Record results in a separate file.

Use the 16 case IDs in [the preflight audit](go-probe-preflight.md). Keep the original artifacts and frozen scores for comparison. For go-u003 and go-u013, prepare separate fixture revisions that remove the dependency on the head-only constant. Record their diffs and hashes, demonstrate that the intended assertion fails on base and passes on fixed head, and collect new baseline logs before counting their probes. If either repair fails qualification, keep its row in the 16 with a fixture-blocked outcome and report the eligible denominator alongside the absolute counts. Do not substitute cases or lower the targets. Use reviewed patch labels only in scoring.

| Measure | Target |
|---|---:|
| Cases with an executed, preserved probe | ≥8 |
| Cases correlated to the original failing invocation | ≥4 |
| Cases with discriminating evidence | ≥3 across ≥3 repositories |
| Unsupported established mechanisms | 0 |
| Cases with a typed terminal outcome | 16/16 |

Count each case once per measure. A case with a discriminating probe must also meet the execution and correlation criteria.

- **Executed and preserved.** The selected test runs in the exact instrumented binary, whose digest and invocation are recorded. Retain the operation marker, its location and provenance, and the baseline and probe outcomes. The test and original check must execute. A probe must preserve the baseline failure signature in all three runs before its observations count. A passing, skipped, changed, or unstable failure is inconclusive.
- **Correlated.** Match the original test or subtest and failure site or identified check, then bind the observation to the actual failing invocation through a recorded call or trace identity. A matching scalar or a shared function name is insufficient. Preserve the correlation evidence for review.
- **Discriminating.** Before the probe runs, record two plausible explanations and their distinct predicted observations at a named code boundary. Count the result when the correlated observation is stable across three runs and supports one prediction while ruling out the other for that invocation. For example, an incorrect value at function entry rules out that invocation as the first point where the value became incorrect. Reproduction, coverage, and extra logs alone count only at their respective stages.
- **Established mechanism.** Keep the existing proof requirement. Report supported or refuted hypotheses at the scope tested, and mark missing, conflicting, or unstable observations inconclusive.

## Budgets and sequence

Allow up to five ranked operations and 180 seconds of wall time per case for the optional runtime investigation, including coverage, preparation, builds, and reruns. Within that deadline, allow up to 60 seconds per candidate including build and up to three executions of its binary. Use the remaining deadline if it is shorter. A deadline after fewer than three qualifying runs is inconclusive. Keep the default evidence-only diagnosis available in its existing execution path.

1. Contain failed analysis units and preserve typed reasons and SSA panic diagnostics while discarding partial output.
2. Connect localized files to selection. Use coverage and repository stack locations when SSA is unavailable and use SSA evidence where it can scope or strengthen a candidate.
3. Add the bounded probe loop and preservation checks. Record outcomes as supported, refuted, or inconclusive.
4. Return a candidate tier with checkable reasons even when probing stops.
5. Rerun the 16 and record one row per case with selection inputs, candidate rank, stage, typed reason, timing, repeat outcomes, and retained evidence. Report fixture failures and stages such as selection, instrumentation, build, execution, preservation, and correlation separately.

Use a four-week ceiling for this slice. Stop expansion at the first unmet gate. If fewer than eight cases reach a preserved probe, inspect selection and execution failures. If at least eight execute but fewer than four correlate, focus on correlation. If correlation passes but discrimination falls short, review the hypotheses and whether the chosen observations distinguish them. Any unsupported established mechanism stops promotion and requires correction before proceeding. Record the observed results and stop reason at the time limit.

After the rerun meets these gates, add the experiment runner and bounded hunk reversion, then validate on fresh cases from at least three repositories and prepare the release. The frozen packets hold corrective diffs. Normal PR regression attribution stays untested until a legitimate introducing base/head pair is available.
