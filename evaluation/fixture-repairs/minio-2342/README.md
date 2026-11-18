# MinIO fixture repair

This evaluator record replaces the compile-blocked inputs for go-u003 and go-u013 with separately qualified baselines. The frozen corpus, historical reports, and acceptance criteria remain intact.

Apply [test-only.patch](test-only.patch) to the shared, comment-redacted `input-validation_test.go` fixture. The patch defines the expected worker ceiling within the test so the test can compile before the implementation introduces its constant. Use the identical resulting test file on both revisions. Only send the test fixture, buggy source, test command, and base failure log to diagnosis.

| Input | Value |
|---|---|
| Buggy revision | `6bfd31ed6aa196c1c60ca567bdd2b5e36a6498dd` in `minio/minio-go` |
| Fixed control | `1c4094b5c3c327a829b42628d4cd4657492c1931` in `jiuker/minio-go` |
| Toolchain | `go1.26.2 darwin/arm64` |
| Original redacted test SHA-256 | `9624b539446cd82d18792d9ad046611ac34fb7ad2ebcc1e57f836a47ba0b9978` |
| Repaired test SHA-256, both revisions | `41a7982bc04489533ee8b664e8e5d2f5f241f71ecb100ef9803c73780d02ed83` |

Run each case with `go test -count=1 -v -run '^<test>$' .`. The focused commands logged actual test execution on both revisions. Dependencies were fetched separately and test execution used `GOPROXY=off`.

| Case | Test | Buggy revision | Fixed revision |
|---|---|---|---|
| go-u003 | `TestNilReaderIsRejected` | [Exit 1](go-u003-base.log). A request was issued with a nil reader. | [Exit 0](go-u003-head.log). Test passed. |
| go-u013 | `TestGetNumThreads` | [Exit 1](go-u013-base.log). The method returned worker counts above 100. | [Exit 0](go-u013-head.log). Test passed. |

Use these logs as the repaired baselines. Repeat the same qualification on the corpus runner platform if its toolchain or environment differs from this run.
