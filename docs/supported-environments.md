# Supported environments

| Environment | What Ceaser detects | Source analysis | Automatic setup in the reusable workflow |
| --- | --- | --- | --- |
| Go | `go.mod` | AST and typed calls, bounded value tracing, and race evidence | Go from `go.mod` |
| JavaScript / TypeScript | `package.json` and lockfiles | Source symbols and stack locations. Jest-style failures and JUnit reports. | None |
| Python | Python project and dependency manifests | Source symbols and traceback locations | None |
| Rust | `Cargo.toml` | Source symbols and compiler diagnostics | None |
| Java / Kotlin | Maven and Gradle build files | Source symbols, JUnit evidence, and exception locations | None |
| Bazel | Workspace or module markers | Environment detection. Supply the exact target and flags. | None |

Any runner that emits compatible JUnit XML can supply basic test evidence.
That includes browser runners when they upload JUnit reports. Runner-specific
features such as browser trace decoding and automatic source-map recovery
have not been validated end to end.

Use the exact command from the failing job for reproduction. Toolchain
detection says which files were found. Local services, native libraries,
generated files, flags, and credentials still depend on the target setup.
See [environment adapters](environment-adapters.md) for implementation details.
