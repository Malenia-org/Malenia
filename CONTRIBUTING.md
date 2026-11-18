# Contributing to Ceaser

## Report a failure Ceaser handles poorly

Open a GitHub issue with the CI provider, language and test runner, Ceaser
version or commit, and the failing source revision. Include the command, a
small redacted log excerpt, and the current result compared with the result
you expected. For a wrong source lead, name the file or function you expected
and explain what supports it. For a missing comment, include the workflow run
and whether the source run was associated with a pull request.

For private repositories, use a small reproducible example or anonymized
excerpts. Remove tokens, private source, and service addresses before sharing
an artifact or prompt.

## Set up a development checkout

Install Go 1.26, Git, and a C compiler for SQLite. Docker is only needed for
changes involving the Docker executor. Fork or clone the repository and
create a branch for your change.

```sh
git clone https://github.com/ceaser-org/ceaser.git
cd ceaser
go build -o /tmp/ceaser-dev ./cmd/ceaser
go test ./...
```

Point the binary at a saved log to exercise parsing without running the
target repository's tests:

```sh
/tmp/ceaser-dev evidence --ci-log /path/to/failure.log > /tmp/ceaser-report.json
/tmp/ceaser-dev brief --report /tmp/ceaser-report.json
```

Read [How Ceaser works](docs/runtime-architecture.md) for the end-to-end
pipeline, execution boundaries, and limits before changing diagnosis behavior.

## Find the right code

| Area | Location |
| --- | --- |
| CLI commands and configuration | [cmd/ceaser](cmd/ceaser/) |
| Evidence parsing, diagnosis, and reports | [internal/diagnosis](internal/diagnosis/) |
| Repository indexing and Go source analysis | [internal/repository](internal/repository/) |
| Candidate generation and ranking | [internal/localization](internal/localization/) |
| Execution backends | [internal/execution](internal/execution/) |
| Optional model analysis and code lookup | [internal/agent](internal/agent/) |
| GitHub setup and result publication | [.github/workflows](.github/workflows/) |
| Evaluation and scoring | [evaluation](evaluation/) · [internal/evalcorpus](internal/evalcorpus/) |

## Make and verify a change

- Keep each PR focused and format changed Go files with `gofmt`.
- For a parsing or diagnosis fix, capture the smallest example that reproduces
  the issue. Retain the relevant log format and distinguish observed evidence
  from inferred source leads in both the test and the report.
- Keep upstream fixes and expected answers on the scoring side of an
  evaluation. Preserve existing logs and historical results. If a fixture
  changes, record why and show that the intended test ran.
- For workflow changes, check the rendered output with a saved report. When
  possible, link a live run that exercises the change on a maintainer-owned
  test PR.
- Before opening the PR, run `go test ./...`, `go vet ./...`, and
  `go build ./...`. Note any checks you could not run and why.

In the PR description, explain the failure or workflow that motivated the
change, what the reader will see now, and how you checked it. For a
diagnosis improvement, name the case and the evidence that supports the
stronger conclusion.

## Release changes

The reusable workflow and setup action default to a released binary. A change
on `main` does not change that binary. Use `ceaser_version: main` in a trusted
development workflow to validate unreleased code.

For a release, update the installation defaults and release notes together,
run the checks above, then publish the version tag. The release workflow builds
and tests the Linux binary once, uploads its checksum and build information,
and checks the setup action against the published download. Keep previous
release tags and assets immutable. See [Action packaging](docs/github-action.md).
