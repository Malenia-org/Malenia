# HY4 Flash execution workstream

Worktree: /tmp/ceaser-hy4-eval
Branch: eval/hy4-go-holdout
Frozen baseline engine: e219b2565b794c6489c5fcfddfa0437192ddf076
Coordinator worktree: /tmp/ceaser-codex-proof (fix/go-proof-audit)
Original user workspace: /Users/yashbahuguna/hades; do not edit it.

You own candidate verification, reproduction, preparation/harness code,
workflow dispatch/monitoring, artifacts, first-pass reasoning and gap analysis.
Begin work immediately. Read repository instructions. Candidates are in
evaluation/holdout-prep/CANDIDATES.json in the HY4 worktree; none are accepted until independently verified.

Read GitHub metadata to verify closed/unmerged status and full revisions.
Inspect fixes only in evaluator preparation. Prove buggy failure and fix control
using equivalent tests, dependencies and environment. A closed PR or a passing
fix head does not establish a correct isolated fix. Quarantine ambiguous or
unreproducible cases; replace rejected candidates to target 15 accepted unseen
cases. Koanf is regression-only. Preserve wrong-values, panic/nil, mutation,
interface and race coverage. Predeclare repetitions for nondeterministic cases
and retain all attempts. Do not cherry-pick.

Write accepted/rejected/quarantined status, exact reasons, full SHAs, commands,
Go versions, test execution confirmation and infrastructure needs. Cases must
have independently verifiable culprit function/operation and mechanism.

Keep evaluator-only facts outside diagnosis's filesystem/mount/job. Use separate
prep, diagnosis and scoring jobs where needed. The diagnosis job receives only
buggy source, redacted functional regression tests, command and observed failure.
It receives no PR description, source fix, fix revision hints, expected.yaml,
explanatory comments or upstream culprit notes. Neutral case IDs belong in
input bundles; PR mapping belongs in evaluator metadata. Do not claim blindness
merely because files have different directory names.

The baseline engine remains frozen at e219b25 even if coordinator changes are
available. Seal and hash diagnosis reports before scoring or engineering fixes.
Existing 15 Go cases and their logs/expected answers/manifest are immutable.
Keep new preparation/inputs separate. Track development exposure: a case used
to engineer a fix is no longer held out for that changed engine.

You may edit harness/workflow/preparation scripts and add tests for those tools.
Do not change internal/diagnosis/, internal/repository/, internal/evalcorpus/ or
cmd/corpus-score without a coordinator handoff. Do not merge main or another
agent's branch. Keep commits scoped; push this branch when useful.

For each accepted case retain raw logs, SHA-256 hashes, engine and source SHAs,
commands, environment, JSON report, workflow URLs, repro/fix outcomes and timing.
Distinguish harness failures, diagnosis failures, correct localization, correct
mechanisms, unsupported causal claims and justified abstentions. Audit each
claimed mechanism beyond matching file/function names and evidence labels.

Deliver:
- candidate decisions with reasons;
- input bundles and separately isolated evaluator truth;
- sealed baseline reports and scoring;
- machine-readable summary (candidate-decisions.json, baseline-results.json);
- ranked gap packets with source/SSA stopping points, hypotheses, evidence and
  proposed general fixes/tests.

Report bounded fixes you can implement safely. Escalate architecture, disputed
causality and verification-contract changes. Do not relax scoring to get success.
Do not spend work on the coordinator's proof-audit implementation.
