<!-- ceaser-diagnosis -->
## Ceaser diagnosis

**What failed**

<pre>TestRecursiveStructToMapHookFunc_nil/composed — panic: reflect: call of reflect.Value.Interface on zero Value [recovered]</pre>

**Likely function to inspect**

**Primary suspect**

<pre>[observed_stack] decode_hooks.go:380 RecursiveStructToMapHookFunc — candidate derived from an observed stack location [confidence: medium] [Evidence: E-frame-051f4c576ac9, E-source-a3b5ff7e86ed]</pre>

**Other ranked leads**

<pre>[observed_stack] decode_hooks.go:52 cachedDecodeHook — candidate derived from an observed stack location [confidence: medium] [Evidence: E-frame-a171a2307efd, E-source-6d2808bae2c5]
[observed_stack] decode_hooks.go:109 ComposeDecodeHookFunc — candidate derived from an observed stack location [confidence: medium] [Evidence: E-frame-187b66c6a6bc, E-source-5c355dd4dd48]
[observed_stack] mapstructure.go:554 Decoder.decode — candidate derived from an observed stack location [confidence: medium] [Evidence: E-frame-45f5e7b46a16, E-frame-3cab2be5f049, E-source-3101bb366ba8]</pre>

**What to investigate**

<pre>- run the smallest failing test in a network-disabled sandbox
- compare the same targeted test on base and head
- collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>

These are ranked investigation leads, not verified root causes. Root cause not verified.

Deferred. Analyzed existing CI evidence.

<details>
<summary>Diagnosis details</summary>

<pre>Classification: test_failure_observed
Command: go test -count=1 -run TestRecursiveStructToMapHookFunc_nil .
CI evidence records failed tests, but no structured race or compiler diagnostic was available to establish a more specific incident.
Evidence: E-causal-fc947e3c94e92128dd0c, E-causal-aa2fca658d9e6e93f827
Still needed: data flow connecting the specific operation to the failed assertion
Still needed: evidence establishing the violated invariant
Next check: run the smallest failing test in a network-disabled sandbox
Next check: compare the same targeted test on base and head
Next check: collect the runtime-specific diagnostic artifact identified by the unresolved investigation objective</pre>

<pre>[observed_stack] decode_hooks.go:380 RecursiveStructToMapHookFunc — candidate derived from an observed stack location [Evidence: E-frame-051f4c576ac9, E-source-a3b5ff7e86ed]
[observed_stack] decode_hooks.go:52 cachedDecodeHook — candidate derived from an observed stack location [Evidence: E-frame-a171a2307efd, E-source-6d2808bae2c5]
[observed_stack] decode_hooks.go:109 ComposeDecodeHookFunc — candidate derived from an observed stack location [Evidence: E-frame-187b66c6a6bc, E-source-5c355dd4dd48]</pre>

</details>

[View the workflow run and download the investigation brief](https://github.com/caeser-org/ceaser-demo-mapstructure/actions/runs/44827197679)

