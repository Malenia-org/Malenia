# Evaluation

This directory contains failure fixtures, scored reports, and experiment
records. For product setup, use the [Ceaser README](../README.md).
For submission guidelines, see [Contributing](../CONTRIBUTING.md).

| Path | Contents |
| --- | --- |
| [corpus](corpus/) | Frozen case manifest, logs, and evaluator-only expectations |
| [fixture repairs](fixture-repairs/) | Test-only corrections with baseline and fixed-head checks |
| [results](results/) | Saved metrics grouped by engine revision |
| [probe rerun](probe-rerun/) | Per-case outcomes, raw artifacts, and original reference reports |
| [case schema](schema/README.md) | Corpus fields and evidence requirements |

Start with the [evaluation guide](../docs/diagnosis-evaluation.md) to prepare
a report index and score saved runs. The [preflight review](go-probe-preflight.md)
describes the plain-log and corrective-diff baselines. The
[frozen probe criteria](go-probe-acceptance.md) define the stage metrics used
for the 16-case rerun.

## Tools

- `ceaser eval freeze` records the current corpus manifest revision. Run it
  only as part of a deliberate corpus update.
- `go run ./cmd/corpus-score --corpus evaluation/corpus --reports index.json`
  scores saved Go reports against evaluator expectations.
- `go run ./cmd/proof-audit --reports index.json` checks retained evidence on
  reports claiming a mechanism. Its consistency result still requires
  independent review of the underlying cause.

Keep withheld fixes and evaluator notes outside the diagnosis inputs. Preserve
historic metrics and logs when running a new experiment.
