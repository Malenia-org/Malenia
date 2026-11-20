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
diagnosis engine through a reusable workflow. v9.1 downloads a checksum-verified
prebuilt binary; it does not compile Ceaser on each run.

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
    uses: caeser-org/ceaser/.github/workflows/ci-diagnose.yml@v9.1
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
reproduction as deferred. See [the report guide](reports.md) for the artifact
name and instructions if the Markdown brief is missing.

## Inputs

All inputs are optional. Diagnosis settings can come from `.ceaser.yml`;
installation and publication defaults are defined by the reusable workflow.

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
| `ceaser_version` | `v9.1` | Download a checksum-verified release; `latest` follows releases and `main` explicitly builds development source |
| `comment_detail` | `short` | Collapse diagnosis details, or use `detailed` to show them by default |
| `agent_code_retrieval` | `false` | Allow optional model analysis to request bounded Go source excerpts from the failing commit |
| `ceaser_repository` | `caeser-org/ceaser` | Repository providing release assets |
| `go_cold_path` | `false` | Explicit focused Go verification; requires `docker_image` |
| `go_cold_path_attempts` | `3` | Focused verification attempts per revision (1–20) |
| `comment` | `true` | Post the diagnosis as a PR comment |
| `comment_app_client_id` | none | Publish using your own installed GitHub App instead of `github-actions[bot]` |

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

## Optional custom comment author

The default author is `github-actions[bot]`. To use an App you own, register
and install a GitHub App with **Issues: read and write**, upload its avatar,
and disable webhooks if it is used only for workflow authentication.

This option is for the repository adopting Ceaser, not for Ceaser's maintainers
or a shared Ceaser bot. Each caller uses an App that it owns and controls the
credentials for. Installing a Ceaser-provided App in a repository does not by
itself change comment authorship: without that App's client ID and private key
in the caller's own Actions settings, comments still come from
`github-actions[bot]`. Do not distribute one App's private key to unrelated
repositories. Offering a shared bot identity to the public would require a
trusted token-issuance service outside callers' workflows; this setup does not
provide one.

In the caller repository, store its client ID as the Actions variable
`CEASER_APP_CLIENT_ID`. Store the generated private key file as the Actions
secret `CEASER_APP_PRIVATE_KEY`. Never commit or paste the key into an issue.
Pass both to the reusable workflow:

```yaml
    with:
      run_id: ${{ github.event.workflow_run.id }}
      head_sha: ${{ github.event.workflow_run.head_sha }}
      comment_app_client_id: ${{ vars.CEASER_APP_CLIENT_ID }}
    secrets:
      CEASER_APP_PRIVATE_KEY: ${{ secrets.CEASER_APP_PRIVATE_KEY }}
```

Use the v9.1 reusable workflow, which includes this input. The binary remains
prebuilt; App authentication changes publication, not diagnosis.
Keep the existing same-repository guard and permissions in your caller.

Only the publication job receives the private key. GitHub's token action
requests an installation token for the current repository, limits it to issue
comments, and revokes it when the job finishes. If a configured App cannot
authenticate, publication fails instead of silently switching authors. Existing
comments by that App are updated; comments by a different author are not edited.

This setup works within repositories where you securely control the App
credentials. Installing an App alone does not configure the workflow or supply
credentials. Do not distribute a shared Ceaser App private key to unrelated
users. A public installation-only service needs trusted token issuance outside
those users' workflows.

## Manual runs

`.github/workflows/diagnosis.yml` in the Ceaser repository is a
`workflow_dispatch` caller for diagnosing a known failure on demand.

## Requirements and limits

- The default install downloads the v9.1 Linux amd64 binary and verifies its
  checksum. `ceaser_version: main` opts into a source build.
- The CLI is prebuilt; reproducing the target project can still require its own
  dependencies and compilation. GitHub runners are the default. Blacksmith is
  not required.
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
