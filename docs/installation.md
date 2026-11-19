# Installation

## Build from source

Install Go 1.26, Git, and a C compiler with CGO support. The CLI uses SQLite.
Use an absolute output path when you want to run Ceaser from other checkouts.

```sh
git clone https://github.com/ceaser-org/ceaser.git
cd ceaser
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/ceaser" ./cmd/ceaser
"$HOME/.local/bin/ceaser" version
```

Add `$HOME/.local/bin` to `PATH` to use `ceaser` by name. See
[supported environments](supported-environments.md) for target toolchain
requirements. The Docker executor also requires Docker and an image with the
tools needed by the target repository.

## Install a released binary

v9.1 provides `ceaser_linux_amd64` for Linux x86_64, built on Ubuntu 24.04 with
CGO enabled. It is dynamically linked; Alpine/musl and older glibc environments
need a compatible source build. Download it and its
`SHA256SUMS` file from the same [release](https://github.com/ceaser-org/ceaser/releases).
From the download directory on Linux, verify and install the asset:

```sh
grep '  ceaser_linux_amd64$' SHA256SUMS | sha256sum -c -
mkdir -p "$HOME/.local/bin"
install -m 0755 ceaser_linux_amd64 "$HOME/.local/bin/ceaser"
"$HOME/.local/bin/ceaser" version
```

Use a source build for other platforms. The GitHub integration defaults to
`ceaser_version: v9.1`, which downloads the prebuilt binary. `latest` follows the
latest release. `main` explicitly builds source at a resolved commit. The
`uses:` reference selects the workflow code independently of the binary version.

For a custom job, the setup action installs the binary and adds it to PATH:

```yaml
- uses: ceaser-org/ceaser@v9.1
- run: ceaser version
```

The setup action installs the CLI only. Use the
[reusable workflow](ci-integration.md) to collect a failed run and publish PR
comments. Neither integration requires Blacksmith or a hosted Ceaser account.

## Set up a project

From the target project, run `ceaser init` and review the proposed files.
See [CI integration](ci-integration.md) for the complete caller workflow.
If Ceaser cannot find a command in the project's existing configuration,
take the command from the failed job and put it in `.ceaser.yml`.

To check setup without running the target command, start with a saved log:

```sh
ceaser evidence --ci-log failure.log > report.json
ceaser brief --report report.json
```

## Troubleshooting

- For SQLite build errors, check that a C compiler is installed and
  `go env CGO_ENABLED` reports `1`.
- For a missing command in a reproduction run, configure the target compiler,
  package manager, or test runner on the runner, or choose a suitable Docker
  image.
- For a missing PR comment, start with the
  [workflow troubleshooting table](../README.md#troubleshooting).
- For a missing Markdown brief, see [where to find and regenerate it](reports.md#if-the-brief-is-missing).
