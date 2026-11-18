package diagnosis

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type RuntimeProbeRun struct {
	FailureCheck     string                  `json:"failure_check,omitempty"`
	FailurePreserved bool                    `json:"failure_preserved"`
	Observations     []ProbeValueObservation `json:"observations,omitempty"`
	Correlation      *AssertionCorrelation   `json:"correlation,omitempty"`
}

type RuntimeProbeAttempt struct {
	OperationID      string            `json:"operation_id"`
	File             string            `json:"file"`
	Line             int               `json:"line"`
	BinaryDigest     string            `json:"binary_digest,omitempty"`
	ExecutionCommand []string          `json:"execution_command,omitempty"`
	DurationMS       int64             `json:"duration_ms"`
	Outcome          string            `json:"outcome"`
	Reason           string            `json:"reason"`
	Preserved        bool              `json:"preserved"`
	Correlated       bool              `json:"correlated"`
	Hypotheses       []string          `json:"hypotheses,omitempty"`
	Runs             []RuntimeProbeRun `json:"runs,omitempty"`
}

type runtimeProbeRepeat struct {
	output      string
	failed      bool
	values      []ProbeValueObservation
	correlation *AssertionCorrelation
}

var goProbeFailure = regexp.MustCompile(`(?m)^\s*--- FAIL: ([^\s]+)`)

// Exact subtest sets and the original assertion signature are both required.
// Instrumented assertions with shifted lines use their invocation correlation.
func preservedProbeCheck(report *Report, run runtimeProbeRepeat) string {
	if !run.failed {
		return ""
	}
	original := failureText(report.Baseline, report.Evidence)
	before := probeFailingTests(original)
	after := probeFailingTests(run.output)
	if len(before) == 0 || !slices.Equal(before, after) {
		return ""
	}
	if run.correlation != nil && run.correlation.Verified {
		if slices.Contains(after, run.correlation.Test) {
			return run.correlation.Test + " " + run.correlation.AssertionID
		}
		return ""
	}
	// Without a mapped assertion invocation, require the same named
	// assertion site and pair in the raw reports.
	for _, test := range after {
		for _, match := range goFrame.FindAllStringSubmatch(original, -1) {
			line, _ := strconv.Atoi(match[2])
			file := filepath.Base(match[1])
			before, ok := signatureForNamedTest(original, test, file, line)
			if !ok {
				continue
			}
			after, ok := signatureForNamedTest(run.output, test, file, line)
			if ok && before == after {
				return fmt.Sprintf("%s %s:%d", test, file, line)
			}
		}
	}
	return ""
}

func probeFailingTests(output string) []string {
	set := map[string]bool{}
	for _, match := range goProbeFailure.FindAllStringSubmatch(output, -1) {
		set[match[1]] = true
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func stableProbeCorrelation(report *Report, repeats []runtimeProbeRepeat) bool {
	if len(repeats) != 3 || repeats[0].correlation == nil {
		return false
	}
	first := repeats[0].correlation
	for _, run := range repeats {
		c := run.correlation
		if preservedProbeCheck(report, run) == "" || c == nil || !c.Verified || c.Test != first.Test ||
			c.AssertionID != first.AssertionID || c.Expected != first.Expected ||
			c.Actual != first.Actual || c.ErrorPresent != first.ErrorPresent ||
			!slices.Equal(c.ReturnPath, first.ReturnPath) || (c.Numeric == nil) != (first.Numeric == nil) {
			return false
		}
		if c.Numeric != nil && *c.Numeric != *first.Numeric {
			return false
		}
	}
	return true
}

func summarizeProbeAttempt(report *Report, op *probeOperation, obs *RuntimeMechanismObservation, duration time.Duration, expired bool) RuntimeProbeAttempt {
	attempt := RuntimeProbeAttempt{
		OperationID: op.ID, File: op.File, Line: op.Line, BinaryDigest: obs.BinaryDigest,
		ExecutionCommand: obs.ExecutionCommand, DurationMS: duration.Milliseconds(),
		Outcome: "inconclusive", Hypotheses: obs.hypotheses, Preserved: len(obs.repeated) == 3,
		Correlated: stableProbeCorrelation(report, obs.repeated),
	}
	var firstCheck string
	for i, repeat := range obs.repeated {
		check := preservedProbeCheck(report, repeat)
		if i == 0 {
			firstCheck = check
		}
		attempt.Preserved = attempt.Preserved && check != "" && check == firstCheck && len(repeat.values) > 0
		attempt.Runs = append(attempt.Runs, RuntimeProbeRun{FailureCheck: check, FailurePreserved: check != "", Observations: repeat.values, Correlation: repeat.correlation})
	}
	attempt.Correlated = attempt.Correlated && attempt.Preserved
	switch {
	case expired && !attempt.Preserved:
		attempt.Reason = "deadline"
	case obs.failureStage != "":
		attempt.Reason = obs.failureStage
	case len(obs.repeated) < 3:
		attempt.Reason = "incomplete_repeats"
	case !attempt.Preserved:
		attempt.Reason = "missing_operation_marker"
		for _, run := range attempt.Runs {
			if !run.FailurePreserved || run.FailureCheck != firstCheck {
				attempt.Reason = "failure_not_preserved"
				break
			}
		}
	case !attempt.Correlated:
		attempt.Reason = "invocation_not_correlated"
	case len(attempt.Hypotheses) != 2:
		attempt.Reason = "no_discriminating_check"
	case op.NumericComparison && obs.Correlation.Numeric == nil:
		attempt.Reason = "conversion_not_observed"
	default:
		attempt.Outcome, attempt.Reason = "refuted", "second_prediction_observed"
		if op.NumericComparison && verifiedIntegerConversion(obs.Correlation.Numeric) || !op.NumericComparison && obs.Correlation.ErrorPresent {
			attempt.Outcome, attempt.Reason = "supported", "first_prediction_observed"
		}
	}
	obs.repeated = nil
	return attempt
}

func probeObservationRank(obs *RuntimeMechanismObservation, attempt RuntimeProbeAttempt) int {
	switch {
	case obs.Mechanism != "":
		return 5
	case attempt.Correlated:
		return 4
	case attempt.Preserved:
		return 3
	case obs.Status == "observed":
		return 2
	case strings.TrimSpace(obs.BinaryDigest) != "":
		return 1
	default:
		return 0
	}
}
