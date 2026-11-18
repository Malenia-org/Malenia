# CI integration

## Onboard a repository with `ceaser init`

From the root of a project, run:

```sh
ceaser init
```

The command reports manifest, language, package-manager, GitHub Actions, runtime,
and test-command evidence, then proposes `.ceaser.yml` and
`.github/workflows/ceaser.yml`. It never overwrites an existing file without
confirmation. Use `ceaser init --yes` for scripted onboarding, and review the
generated `command` whenever no test command could be proven from an existing
workflow or package script.

Ceaser diagnoses a failed CI run in **your** repository. Your repository keeps
its source, its CI workflow, and a small `.ceaser.yml`. Ceaser supplies the
diagnosis engine through a reusable workflow, built from source by default.

## 1. Add `.ceaser.yml`

Commit this at the repository root:

```yaml
command: go test ./...
attempts: 3
timeout: 10m
executor: local
```

A command is required for reproduction if Ceaser cannot discover one. For log-only
analysis, provide the CI evidence. Flags and workflow inputs override the configuration.

| Key | Meaning |
| --- | --- |
| `command` | Command that reproduces the failure |
| `base` | Base revision for the head-vs-base comparison |
| `good` | Known-good revision; enables `git bisect` |
| `attempts` | Reproduction attempts per revision (default 3) |
| `timeout` | Per-attempt timeout, Go duration (default 10m) |
| `executor` | `local` or `docker` |
| `image` | Docker image, required when `executor: docker` |
| `agent` | Ask the configured model gateway to analyze evidence |
| `probes` | Extra diagnostic commands; never modify the repository |

For a project without a toolchain on the runner, use Docker:

```yaml
command: pytest -q
executor: docker
image: python:3.12
```

## 2. Add the caller workflow

`.github/workflows/ceaser.yml`:

```yaml
name: Ceaser

on:
  workflow_run:
    workflows: [CI]          # name of your existing CI workflow
    types: [completed]

permissions:
  actions: read              # read the failed run's logs
  contents: read             # check out the failing commit
  issues: write
  pull-requests: write       # post the diagnosis on the PR

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

That is the whole integration. On the next failing run Ceaser will:

1. check out the exact failing commit (`head_sha`, not the default branch);
2. download the failed jobs' logs with `gh run view --log-failed`;
3. include a bounded copy of the original log as separate evidence;
4. download available CI artifacts and use logs/reports as the primary evidence;
5. write the JSON report, Markdown brief, and job summary;
6. publish a PR comment from a separate job when the run belongs to a PR.

The CI logs are existing observations. An evidence-first report records local
reproduction as deferred.

## Inputs

All inputs are optional; `.ceaser.yml` supplies the defaults.

| Input | Default | Purpose |
| --- | --- | --- |
| `command` | from `.ceaser.yml` | Override the failing command |
| `head_sha` | checked-out commit | Commit to diagnose |
| `base_sha` | none | Exact base commit |
| `base_ref` | default branch | Base ref; merge-base with head is used |
| `run_id` | none | Failed run whose logs are collected |
| `attempts` | from `.ceaser.yml` | Reproduction attempts |
| `docker_image` | from `.ceaser.yml` | Docker executor image |
| `agent` | `false` | Model-backed analysis of collected evidence |
| `ceaser_version` | `main` | Build from a resolved main commit, or choose a released binary by tag or `latest` |
| `comment_detail` | `short` | Collapse diagnosis details, or use `detailed` to show them by default |
| `agent_code_retrieval` | `false` | Allow optional model analysis to request bounded Go source excerpts from the failing commit |
| `comment` | `true` | Post the diagnosis as a PR comment |

Secrets `CEASER_GATEWAY_ENDPOINT` and `CEASER_GATEWAY_MODEL` are optional and
only used when `agent: true`. They are passed only to the separate trusted
analysis job, which consumes the deterministic report artifact. With code
retrieval enabled, that job also reads source from Git objects at the failing
commit. It does not run target repository commands. If model analysis fails,
the deterministic report is still available.

Model analysis is not repair. It adds separately labeled model hypotheses to
the deterministic report. It does not change classification, reproduction,
refusal, or verification results. Patch generation remains a separate,
explicit `repair --model-proposals` capability.

## Manual runs

`.github/workflows/diagnosis.yml` in the Ceaser repository is a
`workflow_dispatch` caller for diagnosing a known failure on demand.

## Requirements and limits

- The default source build runs on a Linux amd64 GitHub-hosted runner.
  Released binaries, when selected, are verified against the release checksum.
- Go is the only automatically configured target toolchain in this workflow.
  For other languages, existing CI artifacts can still supply test results.
- The diagnosis job has read permissions and checkout credential persistence
  disabled. The publication job has the permissions needed to post the comment.
  Gateway secrets are scoped to optional model analysis.
- The example deliberately skips fork PRs. Use the same-repository guard
  unless you have independently verified your execution and publication setup.
- Set `comment: false` to keep the summary and artifact without posting a comment.
- Keep reproduction, repair, and the model off when you want to work only
  with existing CI evidence. Review artifacts and briefs before sharing them.

## Local reproduction

```sh
ceaser diagnose --repo . --base origin/main
```

`ceaser diagnose` reads `.ceaser.yml` when flags are not supplied, and prints
the replay command for the bundle it writes.
