# Go probe preflight

This audit uses the 16 saved controlled cases and evaluator-only upstream pull request file lists. The frozen corpus and saved reports are unchanged. The reviewed labels below are for scoring only. Full-head controls show that the fixed revision passes. A passing control alone does not isolate a particular hunk or function.

## The one executed probe

In go-u002, the probe binary ran, failed the same four subtests, and printed the same assertion pairs as the original run (first pair: expected 7, actual -9223372036854775808). Both outputs use the expected/actual syntax accepted by the signature parser. The record contains a binary digest but no parsed operation values or invocation correlation. The signature gate currently also requires at least one parsed operation value, so its false result cannot be read as a log-format mismatch.

The operation ID resolves to `mcp/tools.go:262`, the final `return defaultValue` in `CallToolRequest.GetInt`. The failing call supplies 7 as the fallback and returned the overflowing integer instead. The upstream hunk changes the float conversion path, whose buggy return is at line 255. The selected function was relevant, but the selected return was the wrong operation for the failing call. The saved excerpt cannot independently distinguish absent marker emission from marker rejection outside the excerpt. There is no retained value to assign to a wrong invocation. Separate reproduction, marker emission/parsing, and invocation correlation in the next stage table.

## Scoring labels reviewed against upstream patches

| Case | Reviewed code location | Patch or failing-test evidence |
|---|---|---|
| go-u001 | `pkg/validation/policy/validate.go` · `validateWildcard` | Replaces the unchecked condition-key type assertion. |
| go-u002 | `mcp/tools.go` · `CallToolRequest.GetInt`, `CallToolRequest.RequireInt` | Both APIs fail in the injected test. Direct fix hunks change each conversion fallback. |
| go-u003 | Intended `api-put-object.go` · `Client.PutObject` | Fix adds the nil-reader guard. The saved run never exercises it because the injected test file references a head-only constant. |
| go-u004 | `config/config.go` · `Config.Clone` | The regression passes the clone into the product reporter. The old clone drops `ZeroClusterID`. The upstream clone hunk preserves it. The frozen analytics label points at the downstream consumer. |
| go-u005 | `collector/textfile.go` · `textFileCollector.Update` | Uses the nil-safe HELP accessor and guards pointer reads. |
| go-u006 | `pkg/handler/context.go` · `UnroutedHandler.newContext`; `pkg/handler/unrouted_handler.go` · `UnroutedHandler.writeChunk` | The cancellation goroutine and the writer both access the body. The fix changes both sides and adds `setBody`. The frozen `patchContext` label is not the modified function. |
| go-u007 | `go/vt/vtgate/engine/distinct.go` · `probeTable.hashCodeForRow` | The direct hunk stops persisting the weight-string fallback across rows. `Distinct` is the enclosing operator name. |
| go-u008 | `mdm/checkin_event.go` · `MarshalCheckinEvent`, `UnmarshalCheckinEvent` | Marshal misses the correctly spelled message type. Unmarshal dereferences the absent body. Both functions are patched. |
| go-u009 | `decode_hooks.go` · `RecursiveStructToMapHookFunc` closure | The hunk header names an adjacent function. The changed call is inside this hook's closure. |
| go-u010 | `convert/conv.go` · `toValue` | Fix adds pointer/interface handling in and around the conversion function. |
| go-u011 | `pkg/cmd/api/fields.go` · `addParamsSlice` | Direct hunk handles a nil type in nested array parsing. |
| go-u012 | `orca/server_metrics.go` · concurrent recorder setters including `SetCPUUtilization`, `SetApplicationUtilization`, `SetMemoryUtilization`, `SetQPS`, `SetEPS`, `SetNamedUtilization` | Patch serializes multiple writers. The saved log shows several lost updates. Score this as a shared mechanism. |
| go-u013 | Intended `api-put-object.go` · `PutObjectOptions.getNumThreads` | The cap is implemented in the method using the new constant in `constants.go`. The saved run only demonstrates that the injected test does not compile on base. |
| go-u014 | `convert/conv.go` · `toValue` | Fix adds bounded pointer/interface cycle handling. |
| go-u015 | `internal/analytics/reporter_properties.go` · `reporter.CreatePosthogPropertiesForSettings` | The saved failing assertion checks the unknown provider. The exact hunk normalizes it to `other`. |
| go-u016 | `go/vt/vtgate/evalengine/api_hash.go` · `NullsafeHashcode`, `NullsafeHashcode128` | The test fails both assertions. The fix routes both APIs through actual-type selection. |

Both MinIO base logs fail with `undefined: maxNumThreads`. Repair the shared fixture and confirm that each intended test and assertion executes before counting them toward a runtime target. Preserve the original artifacts as the historical baseline.

## File baselines

Stack baseline takes unique Go file locations in order from the raw failure log, including assertion locations and compiler diagnostics. It strips the checkout prefix, excludes external absolute paths, and resolves bare filenames relative to the one package in the command. Changed-files baseline takes unique changed Go files in that exact package, ordered lexicographically. It uses the **corrective upstream patch**, which is unavailable to buggy-source-only diagnosis. The engine column uses the saved `localization.likely_files` order. Rank and inclusion are computed before comparing to truth.

| Scoring set and limit | Raw log locations | Corrective diff in tested package | Malenia |
|---|---:|---:|---:|
| Frozen labels, 16 cases, all files | 7/16 | 16/16 | 11/16 |
| Frozen labels, 16 cases, first five | 7/16 | 15/16 | 11/16 |
| 14 executed cases, reviewed primary file, first five | 7/14 | 13/14 | 10/14 |

For the reviewed-score row, the primary file changes only for go-u004 among the 14 executed cases. Secondary files were not counted for the multi-site case go-u006. On that same row, the average list size is 1.93 raw-log paths, 3.00 corrective-diff paths, and 4.00 Malenia paths, so any-hit recall does not measure precision. On the frozen labels the raw log's first path is correct in 4/16. Malenia adds useful file leads, but also misses the race location in go-u006 that is already in the raw stack.

These patches compare buggy source with fixes. Running corrective hunk experiments with them would be fix-assisted evaluation. The packets do not establish the introducing base/head pairs needed to validate normal PR regression attribution.
