# Ceaser

### CI failure diagnosis, starting with Go

Ceaser reads failed CI runs and updates one comment on the pull request.
See the failing test, the reported error, and the source locations worth
inspecting. Expand the evidence or download a brief when you need more context.

**Current adapter — Go, alpha.** Additional language adapters are planned.
The existing cross-language parsers and environment detection are foundations
for that work. They have not received the same end-to-end validation as Go.

[Install locally](#try-it-locally) · [Connect GitHub Actions](#add-it-to-github-actions) ·
[Read the reports](#read-or-share-the-detailed-report) · [Contribute](#contributing)

## What you get

| On the pull request | In the downloadable report | In your own assistant |
| --- | --- | --- |
| A short failure summary and up to two source leads | Failed checks, source excerpts, evidence, and next checks in `ceaser-brief.md` | A paste-ready brief from `ceaser brief --report report.json --prompt` |
| Expand **Diagnosis details** for the reasoning behind a lead | Inspect `ceaser-report.json` for the structured record | Export locally without calling a model |

## Adapter roadmap

| Adapter | Availability |
| --- | --- |
| **Go** | Available in this alpha. Go test output, assertion and panic locations, source relationships, and race evidence. |
| JavaScript / TypeScript | Planned. Existing environment detection and report parsing need runner-specific validation. |
| Python | Planned. Existing traceback parsing and environment detection provide a starting point. |
| Java / Kotlin | Planned. Existing JUnit and exception parsing provide a starting point. |
| Rust | Planned. Existing Cargo detection and diagnostic parsing provide a starting point. |

There are no release dates for additional adapters. Contributions with real
failure logs and reproducible tests will help define their scope. See
[adapter coverage](docs/supported-environments.md) for the implementation
foundations already present.

## Try it locally

You need Go 1.26, Git, and a C compiler for SQLite. Docker is needed only
when you choose the Docker executor. Build the CLI from a checkout:

```sh
git clone https://github.com/ceaser-org/ceaser.git
cd ceaser
go build -o ceaser ./cmd/ceaser
./ceaser version
```

Analyze a saved CI log against the checkout it came from:

```sh
./ceaser diagnose \
  --repo /path/to/checkout \
  --ci-log /path/to/failed-ci.log \
  --artifact-first --json > report.json
```

Check out the revision that produced the log so source locations can be
matched to the right code. If you only have the log, run
`./ceaser evidence --ci-log /path/to/failed-ci.log`.

## Add it to GitHub Actions

Save this as `.github/workflows/ceaser.yml` on your default branch. Replace
`CI` with the name of the workflow that runs your Go tests.

```yaml
name: Ceaser

on:
  workflow_run:
    workflows: [CI]
    types: [completed]

permissions:
  actions: read
  contents: read
  issues: write
  pull-requests: write

jobs:
  diagnose:
    if: |
      github.event.workflow_run.conclusion == 'failure' &&
      github.event.workflow_run.head_repository.full_name == github.repository
    uses: ceaser-org/ceaser/.github/workflows/ci-diagnose.yml@main
    with:
      run_id: ${{ github.event.workflow_run.id }}
      head_sha: ${{ github.event.workflow_run.head_sha }}
      base_sha: ${{ github.event.workflow_run.pull_requests[0].base.sha }}
```

This example uses evidence from same-repository runs and excludes fork PRs.
Ceaser updates its existing comment on later failures. It puts the report and
supporting logs in a workflow artifact. Use `comment_detail: detailed` to show
the evidence section by default, or `comment: false` to publish only the workflow
summary and artifact. For reproducible installs, pin the reusable workflow
reference and select a checksum-verified release with `ceaser_version` when one
is available. See [integration options](docs/ci-integration.md).

<details>
<summary><strong>Configuration reference</strong></summary>


Run `ceaser init` from your repository to review a proposed `.ceaser.yml` and
GitHub Actions caller. If it cannot find a test command, add the command from
the failing CI job yourself. You can also write the config by hand.

```yaml
command: go test ./...
attempts: 3
timeout: 10m
executor: local
```

| Setting | When to use it |
| --- | --- |
| `command` | Set the exact test command, including any package or test filter used in CI. |
| `attempts`, `timeout` | Control explicit reproduction runs. |
| `base` | Supply the known base revision for a comparison when you intend to run one. |
| `executor`, `image` | Choose the local or Docker executor. Docker requires an image. |
| `--ci-log`, `--junit`, `--artifact-dir` | Read existing evidence from files. |
| `--artifact-first` | Start from existing evidence. Keep explicit execution options off for log-only triage. |

For a trusted local checkout, running `ceaser diagnose --repo . --command
'go test ./...'` explicitly asks Ceaser to run the command. See the
[installation guide](docs/installation.md) and
[workflow input reference](docs/ci-integration.md#inputs) for the full setup.

</details>

## Read or share the detailed report

Open the workflow run linked in the PR comment, then download the
`ceaser-diagnosis-<run-id>` artifact. Open `ceaser-brief.md` to read the
report or `ceaser-report.json` for the full structured evidence. See the
[report guide](docs/reports.md) for each output and its location.

You can also export a brief locally. Add `--prompt` when you want a version
you can paste into another assistant. Review the excerpts before sharing
logs or private source code.

```sh
./ceaser brief --report report.json > ceaser-brief.md
./ceaser brief --report report.json --prompt > ceaser-prompt.md
```

## What a comment can tell you

On [Kubernetes PR #143709](https://github.com/kubernetes/kubernetes/pull/143709),
the original failure log showed a race between cacher startup and test cleanup.
Ceaser connected the detector markers to overlapping `WaitGroup.Add` and
`Wait` calls. It reported the two stack locations and left the exact call and
PR attribution unresolved. [View the run and report](https://github.com/ceaser-org/ceaser/actions/runs/44322156729).

On the [Ceaser smoke PR](https://github.com/ceaser-org/ceaser/pull/25#issuecomment-6980363714),
the test expected `"ready"` and got `"pending"`. The assertion line was the
useful location. There was no application call to investigate.

## Status

Ceaser is a Go-first alpha. It separates observed failures, inferred code leads,
and verified mechanisms. Stack locations and likely files can narrow the search.
They cannot establish that the PR caused a failure. Runtime probes and verified
mechanism checks are experimental.

On 14 reviewed cases that ran their intended tests, Ceaser included the culprit
file in its first five candidates in 10 cases. The first five raw log locations
included it in 7. The lists had different lengths, and the comparison used a
small development set. The later 16-case probe run verified zero mechanisms.
See the [file comparison](evaluation/go-probe-preflight.md) and
[probe results](evaluation/probe-rerun/README.md).

## How it works

1. Collect the failed command output, structured test reports, and available
   artifacts. Keep the failing revision and evidence provenance with the report.
2. Extract failing checks and observed stack locations. Use the checked-out
   code to find enclosing symbols and infer related functions where possible.
3. Write the diagnosis and evidence bundle. Publication runs separately and
   updates a single comment. The brief is derived from the same report.

Go is the adapter currently offered for the alpha. Shared parsers and
environment detection support the planned adapters. See the
[runtime architecture](docs/runtime-architecture.md) for the code behind
the diagnosis and reporting stages.

<details>
<summary><strong>Troubleshooting</strong></summary>


| Symptom | What to check |
| --- | --- |
| No diagnosis run | The caller must be on the default branch. Check the name under `workflows:` and confirm that the source run failed. |
| Summary exists without a PR comment | Check that GitHub associated the source run with a PR, `comment` is on, and the caller grants the publishing permissions shown above. The example excludes forks. |
| The report points only to a test | Inspect the assertion first. If a stack frame points to application code, check that the source checkout matches the failing revision. |
| A rerun fails during setup | Check the Go version, working directory, dependencies, and required services. Analyze existing logs if a local run cannot match CI. |
| Missing Markdown brief | Open the Ceaser run’s diagnosis artifact and check the **Export investigation brief** step. If the artifact contains `ceaser-report.json`, use `ceaser brief --report ceaser-report.json > ceaser-brief.md` with a current build. [Details](docs/reports.md#if-the-brief-is-missing). |

Use the documented same-repository workflow guard for the starter setup.
Review logs, source excerpts, and exported briefs before sharing them
outside your project.

</details>

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, where to make changes,
fixture guidelines, and how to submit a useful bug report or PR.

```sh
go test ./...
go vet ./...
go build ./...
```

| Reference | Contents |
| --- | --- |
| [Installation](docs/installation.md) | Source builds, released binaries, and common setup failures |
| [CI integration](docs/ci-integration.md) | Workflow inputs, permissions, and execution options |
| [Adapter coverage](docs/supported-environments.md) | Current Go adapter and foundations for planned adapters |
| [Runtime architecture](docs/runtime-architecture.md) | Storage, execution, and gateway internals |
| [Reports and briefs](docs/reports.md) | Find the artifact, read the report, or export a missing brief |
| [External validation](docs/external-validation.md) | Live PR and upstream CI checks, with limits |
| [Diagnosis evaluation](docs/diagnosis-evaluation.md) | Running and interpreting evaluation checks |
| [Evaluation corpus](evaluation/corpus/) | Saved failures and scoring labels |

[Open an issue](https://github.com/ceaser-org/ceaser/issues/new) with a CI run
and what you expected the comment to report.

Licensed under [Apache 2.0](LICENSE).
