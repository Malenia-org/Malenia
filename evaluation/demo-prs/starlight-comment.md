<!-- caeser-diagnosis -->
## Caeser diagnosis

**Unverified**

**Failure**

<pre>TestCollectionPointerMaterialization
1 failing subtests · unknown:0
- nil
panic: type *int is not a supported starlark type [recovered, repanicked]</pre>

**Primary suspect**

**GoSlice.Index** — Observed stack frame

`convert/slice.go:108`

This function appears in the observed failure stack.

Medium confidence · Causal mechanism not verified

Investigation lead only. Root cause not verified.

**What to investigate**

<pre>- run the smallest failing test in a network-disabled sandbox
- compare the same targeted test on base and head
- collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>

[View the workflow run and download the investigation brief](https://github.com/caeser-org/ceaser-demo-starlight/actions/runs/44834175049)

<details>
<summary>Diagnosis details</summary>

<pre>Classification: test_failure_observed
Command: go test -count=1 -run TestCollectionPointerMaterialization ./convert
CI evidence records failed tests, but no structured race or compiler diagnostic was available to establish a more specific incident.
CI evidence records failed tests, but no structured race or compiler diagnostic was available to establish a more specific incident.
Reproduction: deferred; analyzed existing CI evidence.
Evidence: E-causal-9b25b8af148dc13b02c0, E-causal-00ce6901bdabe1f71616
Primary evidence: E-frame-07acbd58cc22
Primary evidence: E-source-c9e7324db8c0
Still needed: data flow connecting the specific operation to the failed assertion
Still needed: evidence establishing the violated invariant
Next check: run the smallest failing test in a network-disabled sandbox
Next check: compare the same targeted test on base and head
Next check: collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>
</details>

