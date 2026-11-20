# Changelog

Ceaser uses release notes under `docs/releases/`. This file summarizes them.

## Unreleased — v9.2 (proposed)

Diagnostics-quality release. No change to verification or promotion.

- Likely-function ranking now preserves the candidate tier's evidence order and
  promotes operand-correlated SSA leads; framework/runtime stack frames are
  ordered behind application code.
- The bounded candidate report keeps implementation candidates ahead of the
  failing test file.
- The concrete panic/exception text is extracted instead of a generic
  placeholder.
- The PR comment leads with the primary suspect and lists what to investigate.

See [docs/releases/v9.2.md](docs/releases/v9.2.md).

## v9.1 — localization crash fix

- Fixes a crash when a stack frame matched only a whole-file node.
- Install defaults pinned to v9.1.

See [docs/releases/v9.1.md](docs/releases/v9.1.md).

## v9 — likely-function handoff tier

- Adds the structured `likely_function` tier (primary suspect plus alternates,
  each with file, function, line, reason, provenance kind, confidence and
  evidence IDs).

See [docs/releases/v9.md](docs/releases/v9.md).

## v8 and earlier

See [docs/releases/](docs/releases/).
