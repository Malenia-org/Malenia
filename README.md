# Ceaser

Ceaser reads failed Go CI runs and posts one comment on the pull request. It
shows the failing test, the reported error, and the code worth inspecting.
Open the details for the evidence and the checks that could narrow the cause.

Ceaser can work from existing CI logs without rerunning the test. You can also
use it from the command line on a saved log.

## Try it locally

You need Go 1.26, Git, and a C compiler for SQLite.

```sh
git clone https://github.com/ceaser-org/ceaser.git
cd ceaser
go build -o ceaser ./cmd/ceaser
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

## Read or share the detailed report

Download the diagnosis artifact from the workflow run and open
`ceaser-brief.md`. It contains the failing checks, source locations,
assessment, evidence excerpts, and what still needs to be checked. It is
generated from the report without a model. The JSON report retains the full
structured evidence.

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

## Development

```sh
go test ./...
go vet ./...
go build ./...
```

The [evaluation corpus](evaluation/corpus/) contains saved failures and
scoring labels. [Open an issue](https://github.com/ceaser-org/ceaser/issues/new)
with a CI run and what you expected the comment to report.

Licensed under [Apache 2.0](LICENSE).
