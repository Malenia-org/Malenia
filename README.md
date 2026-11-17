# Malenia

Evidence-first CI failure diagnosis. Malenia analyzes a failed CI run and
produces a bounded, evidence-cited diagnosis — it does not guess.

## What it does

- Ingests original CI evidence (logs, artifacts, JUnit) and classifies the failure
- Reproduces the failure when appropriate and compares base vs head revisions
- Localizes reachable implementation code and traces typed Go SSA dependencies
  from the reported assertion, with source evidence for inspected operations
- Refuses to claim a verified root cause when evidence is insufficient (e.g.
  data races without a runtime binary) and states concrete next actions instead

The diagnosis is deterministic and bounded. An optional model gateway can
propose interpretations, but every claim must cite evidence IDs and is checked
against a contract before publication.

The `go` adapter uses AST, `go/packages` type-resolved call graphs, and bounded
SSA value tracing. It follows arguments, returns, possible memory writes, and
branch predicates. Dynamic calls, aliases, closures, and concurrency remain
explicit gaps when static evidence cannot resolve them. Other languages get bounded symbol extraction and an honest refusal
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
`internal/evalcorpus/` for the scorer. File localization and mechanism verification
are separate metrics. A call chain reaching the known buggy file is insufficient
to verify a root cause. Saved reports can be scored using `go run ./cmd/corpus-score
--reports /path/to/case-report-index.json`, where the index maps case IDs to report paths.

## Status

Go diagnoses currently provide localization and static dependency evidence;
these do not establish branch feasibility or a violated invariant. Unresolved
questions are reported as next experiments for the existing isolated execution
workflow; SSA analysis does not start another sandbox or invoke an LLM. Data races remain experimental.
The Go corpus contains 15 cases; accuracy claims must distinguish localization,
verified mechanisms, unsupported claims, and justified abstention.

## Development

```sh
go test ./...
go vet ./...
```

The `evaluation/` and `benchmarks/` directories contain evaluation harnesses,
not production behavior.
