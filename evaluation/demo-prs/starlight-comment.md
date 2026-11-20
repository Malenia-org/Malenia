<!-- ceaser-diagnosis -->
## Ceaser diagnosis

**What failed**

<pre>TestCollectionPointerMaterialization/nil — panic: type *int is not a supported starlark type [recovered, repanicked]</pre>

**Likely function to inspect**

**Primary suspect**

<pre>[observed_stack] convert/slice.go:108 GoSlice.Index — candidate derived from an observed stack location [confidence: medium] [Evidence: E-frame-07acbd58cc22, E-source-c9e7324db8c0]</pre>

**What to investigate**

<pre>- run the smallest failing test in a network-disabled sandbox
- compare the same targeted test on base and head
- collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>

These are ranked investigation leads, not verified root causes. Root cause not verified.

Deferred. Analyzed existing CI evidence.

<details>
<summary>Diagnosis details</summary>

<pre>Classification: test_failure_observed
Command: go test -count=1 -run TestCollectionPointerMaterialization ./convert
CI evidence records failed tests, but no structured race or compiler diagnostic was available to establish a more specific incident.
Evidence: E-causal-18338579917a70a09e98, E-causal-fd9511ddbe7f4797ec75
Still needed: data flow connecting the specific operation to the failed assertion
Still needed: evidence establishing the violated invariant
Next check: run the smallest failing test in a network-disabled sandbox
Next check: compare the same targeted test on base and head
Next check: collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>

<pre>[observed_stack] convert/slice.go:108 GoSlice.Index — candidate derived from an observed stack location [Evidence: E-frame-07acbd58cc22, E-source-c9e7324db8c0]
[inferred_source] convert/interface.go — ranked among localized likely files [Evidence: E-causal-fd9511ddbe7f4797ec75]
[inferred_source] convert/slice.go — ranked among localized likely files [Evidence: E-source-c9e7324db8c0]</pre>

</details>

[View the workflow run and download the investigation brief](https://github.com/caeser-org/ceaser-demo-starlight/actions/runs/44826427798)

