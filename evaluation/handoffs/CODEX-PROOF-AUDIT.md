# Codex workstream

Branch fix/go-proof-audit; baseline engine e219b25 frozen for HY4.
HY4 owns /tmp/ceaser-hy4-eval on eval/hy4-go-holdout. Its preparation files
include evaluator-only candidate mappings and must never be diagnosis inputs.

Codex owns general proof retention and independent proof-audit tooling/tests.
This must distinguish missing retained proof from rejected proof, preserve the
existing frozen corpus and never present structural validation as independent
upstream ground-truth review or binary-byte verification.

Do not consume unseen evaluator explanations to design engine fixes before
baseline reports are sealed. Do not merge worktrees automatically.
