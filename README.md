# Malenia

Malenia diagnoses failed CI runs from the original evidence. It produces a
report whose claims cite evidence IDs. When the evidence is insufficient, the
report states the missing fact and a next experiment instead of asserting a
cause.

## Capabilities

- Ingests CI logs, JUnit reports, artifacts, and normalized evidence, then
  classifies the failure.
- Reproduces the failing command when needed and compares the failing revision
  with a known good or fixed revision.
- Localizes implementation code through AST indexing, type resolved call edges,
  and bounded Go SSA value tracing.
- Returns a shortlist of likely files, functions, and lines with evidence
  references. The shortlist separates observed stack locations from inferred
  call or source relationships and remains useful when probing is disabled,
  unavailable, or inconclusive.
- Supports an opt in runtime probe. The probe builds one instrumented test
  binary, records its digest, runs that binary three times, and retains
  operation values, assertion correlation, and stop reasons.
- Keeps reproduction, localization, static dependency, runtime observation,
  verified mechanism, PR attribution, and fix verification as separate claims.

The diagnosis is deterministic and bounded. An optional model gateway may
propose interpretations, but every published claim must cite evidence and pass
a contract check.

The Go adapter uses AST parsing, `go/packages` type resolution, and bounded SSA
value tracing. It follows arguments, returns, possible memory writes, and
branch predicates. Dynamic calls, aliases, closures, and concurrency remain
explicit gaps when static evidence cannot resolve them. Other languages receive
bounded symbol extraction and a report of the missing evidence.

## Quick start

Configure the failing command in `.malenia.yml`. See that file for all options.

```sh
go build -o malenia ./cmd/malenia
./malenia diagnose --repo /path/to/repo --command "go test ./..." --json
```

Run `./malenia diagnose -h` for the full flag set. Common inputs include
`--ci-log`, `--artifact-dir`, `--base`, `--agent`, `--bundle`, and
`--go-cold-path`. Runtime probing is opt in through `--runtime-observation` and
uses the configured executor on a disposable source copy.

For a saved log without a checkout, use `malenia evidence --ci-log <file>`.
The evidence path does not run repository code.

## GitHub workflows

The workflows in `.github/workflows/` run the same engine on public changes.

- `try-public-pr.yml` diagnoses a public pull request.
- `controlled-go-regression.yml` checks out a buggy base revision, injects
  comment redacted regression tests, produces a genuine failure log, diagnoses
  the buggy source with the source fix withheld, and runs a fixed head control.
  It accepts extra fixture paths and a SHA verified test override for cases that
  need a separately qualified input.
- `ci-diagnose.yml` publishes a bounded pull request comment. The comment shows
  the candidate shortlist, the evidence behind each lead, and the runtime probe
  status.

A passing workflow or fixed head control does not prove a diagnosis. The
control only shows that the fixed revision passes the same test.

## Optional model analysis

The reusable workflow accepts `agent: true` for model analysis of the diagnosis
report. Set `agent_code_retrieval: true` to let the model request up to five Go
source lookups. The workflow reads Git objects at the failing commit and sends
the matching snippets to the configured model gateway. Search results are
textual leads, not resolved call edges.

For local analysis, pass `--code-repo /path/to/repo --code-revision
<full-commit-id>` to `malenia analyze`. The commit ID must match the revision in
the report. The combined report retains the retrieved snippets and their
evidence IDs.

## Evaluation

`evaluation/corpus/` holds 15 real Go failures with reviewed ground truth. The
scorer lives in `internal/evalcorpus/`. File localization and mechanism
verification are separate measures. A call chain that reaches the known buggy
file does not verify a root cause. Score saved reports with:

```sh
go run ./cmd/corpus-score --corpus evaluation/corpus --reports /path/to/report-index.json
```

The report index maps case IDs to saved report paths. Expected answers reach
the scorer only and never enter diagnosis input.

The 15 case corpus result at engine revision `e219b25` recorded 14 correct
culprit file localizations and two verified mechanisms. The artifact is
`evaluation/results/go-predicate-mechanisms-e219b25.json`.

The frozen acceptance criteria for the 16 held out cases are in
`evaluation/go-probe-acceptance.md`. The qualification audit and reviewed
upstream labels are in `evaluation/go-probe-preflight.md`. `evaluation/probe-rerun/`
holds the repaired MinIO fixture, the original reference artifacts, the 16
public run artifacts, and a case by case explanation of each stop point.

The 16 case rerun at engine revision `ea50a5c` recorded 16 passing fixed head
controls, one executed probe with a preserved failing check, no correlated
invocations, and no verified mechanisms. The first unmet gate was preserved
probes. The result file is `evaluation/results/go-probe-rerun-ea50a5c.json`.
Selection and preservation take priority before correlation work.

## Status and limitations

Go diagnoses provide localization and static dependency evidence. These do not
establish branch feasibility or a violated invariant on their own. Unresolved
questions are reported as next experiments for the existing isolated execution
workflow. SSA analysis does not start another sandbox or invoke a model.

The runtime probe observes one SSA identified operation per attempt. It retains
operation identity, result slot, event sequence, the caller frame, and the
executed binary digest. Unsupported source expressions abstain. Equal values and
a matching failure remain observations.

A mechanism is verified only for narrow cases. A supported testify `Equal`
assertion with a constant expected value and a direct scalar call as actual can
be bracketed. Verification then requires exactly one error return operation in
that assertion invocation on the same goroutine, an observed non nil error, and
exact SSA return slots that carry the value unchanged to the failed assertion.
Memory aliases, deferred mutation, asynchronous paths, unsupported assertions,
and ambiguous invocations do not receive this verification.

Native boolean mismatch checks immediately after a scalar producer assignment
can bind an observation to the failed predicate without reading diagnostic
message arguments as the actual value. For supported reflection comparisons,
integer conversion evidence can show that narrowing changed the numeric value
and made two different values compare equal. That check covers the correlated
integer episode. Floating point and complex conversions are outside its scope.

Data races remain experimental. PR attribution and fix verification remain
separate claims from mechanism verification. Accuracy claims must distinguish
localization, verified mechanisms, unsupported claims, and justified
abstention.

## Development

```sh
go test ./...
go vet ./...
go build ./...
gofmt -l .
```

The `evaluation/` and `benchmarks/` directories contain evaluation harnesses,
not production behavior.

## Evaluator proof audit

`go run ./cmd/proof-audit --reports /path/to/sealed-report-index.json` checks
retained assertion spans independently of the diagnosis promotion function.
The index maps case IDs to report JSON paths. Outcomes are `consistent`,
`insufficient`, `rejected`, and `not_claimed`. The audit checks invocation
identity, observed values, SSA scalar return edges, caller frames and primitive
integer conversion arithmetic. It executes no target code and reads no expected
answers.

New runtime observations retain bounded `runtime-assertion-v1` proof events
separately from the truncated human output excerpt. Older reports without the
complete span are marked insufficient for this audit, not refuted. The existing
corpus scorer and baseline results remain unchanged.

A consistent proof is not an independent verdict on the source or upstream
culprit. Report authenticity, binary-byte verification, complete source/call-site
validation and ground-truth attribution remain evaluator responsibilities.
Never use a consistency pass alone as launch-accuracy evidence.
