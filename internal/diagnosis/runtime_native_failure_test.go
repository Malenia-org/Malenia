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
