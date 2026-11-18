package diagnosis

import (
	"strings"
	"testing"
)

func TestNativeGoFailurePreservationUsesNamedCheck(t *testing.T) {
	check := "    value_test.go:7: expected: 43\n        actual: 42\n"
	verbose := "=== RUN   TestConvert\n" + check + "--- FAIL: TestConvert (0.00s)\nFAIL\n"
	nonverbose := "--- FAIL: TestConvert (0.00s)\n" + check + "FAIL\n"
	for _, original := range []string{verbose, nonverbose} {
		report := &Report{Evidence: []EvidenceItem{{Kind: "ci_log", Detail: original}}}
		if got := preservedProbeCheck(report, runtimeProbeRepeat{output: verbose, failed: true}); got != "TestConvert value_test.go:7" {
			t.Fatalf("native failure/check was lost: %q", got)
		}
	}
	prefixed := ""
	for _, line := range strings.Split(strings.TrimSuffix(verbose, "\n"), "\n") {
		prefixed += "test\tTest\t2026-11-19T00:00:00.000Z " + line + "\n"
	}
	if _, ok := nativeGoSignatureForNamedTest(prefixed, "TestConvert", "value_test.go", 7); !ok {
		t.Fatal("GitHub job prefixes hid the native test scope")
	}
	if got := preservedProbeCheck(&Report{Evidence: []EvidenceItem{{Kind: "ci_log", Detail: prefixed}}}, runtimeProbeRepeat{output: verbose, failed: true}); got == "" {
		t.Fatal("GitHub job prefixes hid the original failing test set")
	}
	for name, output := range map[string]string{
		"different test":                         "=== RUN   TestOther\n" + check + "--- FAIL: TestOther (0.00s)\n",
		"line prefix":                            "=== RUN   TestConvert\n    value_test.go:70: expected: 43\nactual: 42\n--- FAIL: TestConvert\n",
		"wrong file":                             "=== RUN   TestConvert\n    other_test.go:7: expected: 43\nactual: 42\n--- FAIL: TestConvert\n",
		"pair in another test":                   "=== RUN   TestOther\n" + check + "--- FAIL: TestOther\n=== RUN   TestConvert\n    value_test.go:7: assertion failed\n--- FAIL: TestConvert\n",
		"repeated site":                          "=== RUN   TestConvert\n" + check + check + "--- FAIL: TestConvert\n",
		"repeated site with opaque second check": "=== RUN   TestConvert\n" + check + "    value_test.go:7: different failure\n--- FAIL: TestConvert\n",
		"multiple pairs":                         "=== RUN   TestConvert\n" + check + "expected: 99\nactual: 98\n--- FAIL: TestConvert\n",
		"parallel stream":                        "=== RUN   TestConvert\n=== PAUSE TestConvert\n=== CONT TestConvert\n" + check + "--- FAIL: TestConvert\n",
		"after test boundary":                    "=== RUN   TestConvert\n--- FAIL: TestConvert\n" + check,
	} {
		t.Run(name, func(t *testing.T) {
			if sig, ok := nativeGoSignatureForNamedTest(output, "TestConvert", "value_test.go", 7); ok {
				t.Fatalf("ambiguous or unrelated native check accepted: %#v", sig)
			}
		})
	}
	for name, output := range map[string]string{
		"different value": "=== RUN   TestConvert\n    value_test.go:7: expected: 43\nactual: 41\n--- FAIL: TestConvert\n",
		"test passed":     "=== RUN   TestConvert\n" + check + "--- PASS: TestConvert\nPASS\n",
	} {
		t.Run(name, func(t *testing.T) {
			report := &Report{Evidence: []EvidenceItem{{Kind: "ci_log", Detail: verbose}}}
			if got := preservedProbeCheck(report, runtimeProbeRepeat{output: output, failed: true}); got != "" {
				t.Fatalf("changed failure was preserved: %q", got)
			}
		})
	}
}

func TestTestifyFailureSignatureIsBoundToExactCheck(t *testing.T) {
	block := func(site, pair, name string) string {
		return "Error Trace: " + site + "\nError: Not equal:\n" + pair + "\nTest: " + name + "\n"
	}
	pair := "expected: 43\nactual: 42"
	valid := block("value_test.go:7", pair, "TestConvert")
	if _, ok := signatureForNamedTest(valid, "TestConvert", "value_test.go", 7); !ok {
		t.Fatal("valid Testify check was lost")
	}
	for name, output := range map[string]string{
		"line prefix":                     block("value_test.go:70", pair, "TestConvert"),
		"pair from previous test":         block("other_test.go:4", pair, "TestOther") + "value_test.go:7\nError: opaque failure\nTest: TestConvert\n",
		"repeated matching assertion":     valid + valid,
		"repeated site with opaque check": valid + block("value_test.go:7", "opaque failure", "TestConvert"),
		"multiple value pairs":            block("value_test.go:7", pair+"\nexpected: 99\nactual: 98", "TestConvert"),
	} {
		t.Run(name, func(t *testing.T) {
			if sig, ok := signatureForNamedTest(output, "TestConvert", "value_test.go", 7); ok {
				t.Fatalf("unrelated or ambiguous Testify check accepted: %#v", sig)
			}
		})
	}
}

func TestFailureSignatureDoesNotReadLabelsInsideIdentifiers(t *testing.T) {
	for _, text := range []string{
		"Error Trace: actual_test.go:7\nexpected: 43\nactual: 42\n",
		"Error Trace: expected_actual_test.go:7\nexpected: 43\nactual 42\n",
		"unexpected: 99\nexpected: 43\nactual: 42\n",
	} {
		got, ok := extractFailureSignature(text)
		if !ok || got.expected != "43" || got.actual != "42" {
			t.Fatalf("identifier was read as an assertion value: %q => %#v, %v", text, got, ok)
		}
	}
	for _, text := range []string{"unexpected: 43\nactual: 42", "expected: 43\nactual_test.go:7", "expected: 43\nactual:\nTest: TestConvert", "expected: 43\nactual :\nTest: TestConvert"} {
		if sig, ok := extractFailureSignature(text); ok {
			t.Fatalf("missing assertion label/value was accepted: %q => %#v", text, sig)
		}
	}
}

func TestProbePreservationDeduplicatesEvidenceRecordsNotAssertions(t *testing.T) {
	log := "=== RUN   TestConvert\n    value_test.go:7: expected: 43\nactual: 42\n--- FAIL: TestConvert\nFAIL\n"
	report := &Report{
		Baseline: Baseline{Results: []Attempt{{Stderr: log}}},
		Evidence: []EvidenceItem{{Kind: "ci_log", Detail: log}, {Kind: "original_ci_log", Detail: log}},
	}
	if check := preservedProbeCheck(report, runtimeProbeRepeat{output: log, failed: true}); check == "" {
		t.Fatal("duplicated storage of one original log lost the preserved check")
	}
	report = &Report{Evidence: []EvidenceItem{{Kind: "ci_log", Detail: log + log}}}
	if check := preservedProbeCheck(report, runtimeProbeRepeat{output: log, failed: true}); check != "" {
		t.Fatal("repeated assertions within one execution were deduplicated into proof")
	}
}
