# Curated demo PRs — reviewed expectations and actual Ceaser output

Six curated, reproducible demos were created under `caeser-org` and run through
the **source-built** engine (`caeser-org/ceaser@release/v9.2-eval` workflow,
engine built from `Yashbhu/ceaser-eval@main`). The reviewed culprit and expected
behavior were recorded in each PR body **before** running Ceaser. Raw comments
are in this directory as `<case>-comment.md`.

| # | Case / category | Reviewed culprit (expected) | Actual primary suspect | Correct? | Misleading / overconfident? |
|---|---|---|---|---|---|
| 1 | `ceaser-demo#1` — nil-pointer / panic | `tree.go` `node.getValue` | `[observed_stack] tree.go:418 node.getValue` | **Yes** | No |
| 2 | `ceaser-demo-koanf#1` — assertion, helper absent from stack | `koanf.go` `toFloat64` | `[inferred_call] getters.go:31 Koanf.Int64s` | **No** | **Yes** — primary wrong; culprit not surfaced |
| 3 | `ceaser-demo-starlight#1` — deeper call chain | `convert/conv.go` `toValue` | `[observed_stack] convert/slice.go:108 GoSlice.Index` | Partial | No — finds the panic site, not the deeper helper |
| 4 | `ceaser-demo-mapstructure#1` — conversion / type handling | `decode_hooks.go` `RecursiveStructToMapHookFunc` | `[observed_stack] decode_hooks.go:380 RecursiveStructToMapHookFunc` | **Yes** | No |
| 5 | `ceaser-demo-gin-race#1` — data race (abstention) | classify race, abstain | `[observed_stack] context.go:122 Context.Copy` (alt `Context.Set`) | **Yes** | No — root cause explicitly unverified |
| 6 | `ceaser-demo-mcp-go#1` — SSA dependency evidence | `mcp/tools.go` `GetInt` | `[inferred_source] mcp/tools.go:255 GetInt` — SSA operand dependency | **Yes** | No |

Tally: **4/6 correct primary** (1, 4, 5, 6), **1/6 misleading** (2), **1/6 partial**
(3 — the reviewed culprit is among the leads).

> **MCP operand-producer rule (2026-11-21).** The assertion's actual operand is
> produced by the call `…GetInt(t14, "n", 7)`, whose SSA `callee` is `GetInt`.
> The operand cone previously followed only `operands`, so `GetInt` was never
> attributed and the generic trace fallback ordered siblings arbitrarily (the
> earlier `GetInt` primary was a Go map-order artifact; before that,
> `GetArguments`). Correlation now follows the operand cone's call `callee` when
> the callee definition is present in the trace. This is established by the trace
> (not a heuristic), keeps correlation as a low-confidence lead, and leaves the
> frozen-16 primaries unchanged (0 changed). `GetInt` is now the deterministic
> primary and `mcp/tools.go` `GetArguments`/`rawIntArgument`/`parseRawInt` remain
> available as related leads via the candidate tier.
>
> **gin-race (#5)** is exercised by a data-race test that only reports a race on
> some runs; the captured comment is from a run where the race was detected.

## Presentation

The mcp-go comment now leads with the readable form (see
`mcp-go-comment.md`): a plain failure summary including the subtest list and the
composed assertion message (`Not equal: expected 7, actual
-9223372036854775808`), a **Primary suspect** with the provenance phrase
("SSA dependency"), an explicit "Identified beyond the observed stack" line,
confidence and unverified status, **Related functions**, and the raw evidence
IDs moved into the collapsed details.

## Notes

- **Beyond-stack localization is demonstrated** (#6, and #4's alternates): the
  mcp-go primary is an SSA dependency value (`E-ssa-…`), not a panic frame, with
  `mcp/tools.go` `GetInt` and its call chain (`rawIntArgument`, `GetArguments`)
  ranked from the failed assertion's value trace. Confidence is `low` and the
  lead is explicitly unverified.
- **Abstention is preserved** (#5): the data-race demo produces leads
  (`Context.Copy` / `Context.Set`) while stating "ranked investigation leads,
  not verified root causes. Root cause not verified." No mechanism is promoted.
- **Honest negative (#2, koanf):** the reviewed culprit `toFloat64` is absent
  from the stack and from the primary/alternate leads; the primary is a
  misleading `inferred_call`. This is a real limitation of the current ranking
  on assertion cases where the failing call path differs from the source
  dependency path (see `evaluation/discovery-findings.md`, mechanism A).
- **Partial (#3, starlight):** `GoSlice.Index` calls `toValue` directly, but the
  verified-call expansion does not admit the deeper helper; the panic site is
  reported instead. Mechanism A.
- "What failed" for testify subtests (#2, #6) shows the failing subtest and its
  file:line but the assertion message column is empty for testify's subtest
  output — a presentation gap recorded for follow-up.

## Reproduce

Each repo's PR reuses the pushed branch and the current engine; re-running the
workflow produces the same comment. No expected result is injected into any
report.
