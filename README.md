# Malenia

Evidence-first CI failure diagnosis. Malenia analyzes a failed CI run and
produces a bounded, evidence-cited diagnosis — it does not guess.

## What it does

- Ingests original CI evidence (logs, artifacts, JUnit) and classifies the failure
- Reproduces the failure when appropriate and compares base vs head revisions
- Builds a source-grounded causal chain from the failing test into the buggy
  implementation, with every step citing retrieved source evidence
- Refuses to claim a verified root cause when evidence is insufficient (e.g.
  data races without a runtime binary) and states concrete next actions instead

The diagnosis is deterministic and bounded. An optional model gateway can
propose interpretations, but every claim must cite evidence IDs and is checked
against a contract before publication.

The `go` adapter is the deep path (AST + `go/packages` type-resolved call
graphs). Other languages get bounded symbol extraction and an honest refusal
where deeper analysis isn't available.

## Quick start

Configure the failing command in `.malenia.yml` (see the file for all options),
then:

```sh
go build -o malenia ./cmd/malenia
./malenia diagnose --repo /path/to/repo --command "go test ./..." --json
```

Run `./malenia diagnose -h` for the full flag set (`--ci-log`, `--artifact-dir`,
`--base`, `--agent`, `--bundle`, `--go-cold-path`, and more).

The `try-public-pr` and `controlled-go-regression` GitHub Actions workflows in
`.github/workflows/` run the same engine against public PRs; the latter
reproduces a documented pre-fix failure from a buggy base with the PR's
regression test injected, with the source fix withheld from the diagnosis.

## Evaluation corpus

`evaluation/corpus/` holds real failures with ground-truth diagnoses used to
score classification, attribution, causal-chain correctness, and honest
abstention. See `evaluation/corpus/cases/go-*/` for the Go cases and
`internal/evalcorpus/` for the scorer. A case is only "correct" when its
evidence-cited causal chain reaches the known buggy file; claiming a verified
root cause on an insufficient-evidence case is scored wrong.

## Status

Deterministic Go logic/contract failures produce established causal chains.
Data races are experimental and reported as `unknown` with next actions — a
race log alone does not prove an ordering violation. The corpus is being grown
toward 15–20 real cases before any accuracy claim is made.

## Development

```sh
go test ./...
go vet ./...
```

The `evaluation/` and `benchmarks/` directories contain evaluation harnesses,
not production behavior.
