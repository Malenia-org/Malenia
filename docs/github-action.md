# GitHub Action

There are two entry points. Both run in your GitHub Actions account.

| Entry point | Use it for |
| --- | --- |
| `caeser-org/ceaser/.github/workflows/ci-diagnose.yml@v9.1` | Read a failed CI run, produce reports, and update its PR comment |
| `caeser-org/ceaser@v9.1` | Install the prebuilt CLI in a job you control |

Start with the [reusable workflow setup](ci-integration.md) for automatic PR
reports. It already installs the CLI; do not add the setup action separately.

## Install the CLI in a custom job

```yaml
name: Read saved failure
on: workflow_dispatch
permissions:
  contents: read
jobs:
  diagnose:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v6
        with:
          persist-credentials: false
      - uses: caeser-org/ceaser@v9.1
      # Supply your own saved failure.log before this step.
      - run: |
          ceaser evidence --ci-log failure.log > ceaser-report.json
          ceaser brief --report ceaser-report.json > ceaser-brief.md
      - uses: actions/upload-artifact@v4
        with:
          name: ceaser-evidence
          path: |
            ceaser-report.json
            ceaser-brief.md
```

This example parses a log without source analysis. To inspect a checkout as
well, use `ceaser diagnose --repo . --ci-log failure.log --artifact-first --json`.
The checkout must match the failed revision. See
[execution behavior](runtime-architecture.md#4-collect-additional-evidence-when-requested)
before using diagnosis on untrusted code.

## Setup action reference

| Input | Default | Meaning |
| --- | --- | --- |
| `version` | `v9.1` | Release tag to install. `latest` follows the newest release. No source build option. |

Output `binary-path` is the absolute installed executable path. The action also
adds its directory to `PATH` for subsequent steps. It requires Linux x86_64,
Bash, curl, awk, sha256sum and a glibc environment compatible with Ubuntu 24.04.
It downloads the binary and checksum from the same release and rejects missing,
duplicate or mismatching checksum records. This checks download integrity;
it is not independent publisher attestation.

The action does not request a GitHub token, start a service, run tests, call a
model, or post a comment. Those behaviors belong to explicit CLI commands or
the reusable workflow. Blacksmith is optional infrastructure, not an account
requirement for Ceaser users.

Pin the action/workflow reference to a full commit SHA for stricter supply-chain
control. The binary version is selected separately. Do not select `latest`
when you need a fixed binary revision.

## Release and Marketplace publishing

A `v*` tag such as `v9.1` triggers `.github/workflows/release.yml`. The release job tests
the source, builds and smoke-tests the CLI, publishes the assets, and verifies
the setup action can download and execute the published binary. Maintainers
should inspect that run before announcing the release.

A GitHub Action can be used directly from its public repository without a
Marketplace listing. Marketplace publication has an additional GitHub release
form step: select **Publish this Action to the GitHub Marketplace**, accept
the Developer Agreement if prompted, and choose the category. An organization
owner must accept that agreement. See
[GitHub's publication instructions](https://docs.github.com/en/actions/how-tos/create-and-publish-actions/publish-in-github-marketplace).

This repository supplies an Action and a reusable workflow. It does not register
a hosted GitHub App or run a Ceaser-owned webhook service.
