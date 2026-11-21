<!-- caeser-diagnosis -->
## Caeser diagnosis

**Unverified**

**Failure**

<pre>TestRecursiveStructToMapHookFunc_nil
1 failing subtests · unknown:0
- composed
panic: reflect: call of reflect.Value.Interface on zero Value [recovered]</pre>

**Primary suspect**

**RecursiveStructToMapHookFunc** — Observed stack frame

`decode_hooks.go:380`

This function appears in the observed failure stack.

Medium confidence · Causal mechanism not verified

**Related functions**

- `cachedDecodeHook` — decode_hooks.go:52
- `ComposeDecodeHookFunc` — decode_hooks.go:109
- `Decoder.decode` — mapstructure.go:554

Investigation lead only. Root cause not verified.

**What to investigate**

<pre>- run the smallest failing test in a network-disabled sandbox
- compare the same targeted test on base and head
- collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>

[View the workflow run and download the investigation brief](https://github.com/caeser-org/ceaser-demo-mapstructure/actions/runs/44834262628)

<details>
<summary>Diagnosis details</summary>

<pre>Classification: test_failure_observed
Command: go test -count=1 -run TestRecursiveStructToMapHookFunc_nil .
CI evidence records failed tests, but no structured race or compiler diagnostic was available to establish a more specific incident.
CI evidence records failed tests, but no structured race or compiler diagnostic was available to establish a more specific incident.
Reproduction: deferred; analyzed existing CI evidence.
Evidence: E-causal-28ea4beaff8adb5ab8cd, E-causal-ddb061eba145d70860c5
Primary evidence: E-frame-051f4c576ac9
Primary evidence: E-source-aaafc84888d9
Still needed: data flow connecting the specific operation to the failed assertion
Still needed: evidence establishing the violated invariant
Next check: run the smallest failing test in a network-disabled sandbox
Next check: compare the same targeted test on base and head
Next check: collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>
</details>

