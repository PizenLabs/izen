package command

import (
	"fmt"
	"strings"

	riview "github.com/PizenLabs/izen/internal/review"
)

// IsReviewTestComposite detects the composite shortcut syntax
// "/review $test" (in any ordering / spacing) and reports whether the
// user input should be routed through the dynamic-test-then-review pipeline
// instead of the default static audit.
func IsReviewTestComposite(input string) bool {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return false
	}
	// Must contain the /review slash command and the $test sub-command token.
	if !strings.Contains(trimmed, "/review") {
		return false
	}
	if !strings.Contains(trimmed, "$test") {
		return false
	}
	return true
}

// TestExecutor runs the dynamic test suite for the composite pipeline.
type TestExecutor interface {
	// RunDynamicTests executes the project test suite and returns the raw
	// telemetry (exit codes, pass/fail counts, output) to be injected into
	// the forensic ledger.
	RunDynamicTests() (passed bool, telemetry string, err error)
}

// LedgerInjector feeds test telemetry into the forensic ledger context so the
// subsequent risk analysis engine can consume it alongside the git diff.
type LedgerInjector interface {
	// InjectTestTelemetry records the dynamic test report into the ledger.
	InjectTestTelemetry(passed bool, telemetry string) error
}

// ReviewRunner executes the comprehensive review engine driven by both the
// git diff and the injected test suite reports.
type ReviewRunner interface {
	// RunComprehensiveReview triggers the risk analysis engine with the git
	// diff AND the test telemetry already present in the ledger.
	RunComprehensiveReview() (summary string, ledger *riview.ReviewLedger, err error)
}

// ReviewTestCompositeResult carries the outcome of the composite pipeline so
// the UI layer can render it deterministically.
type ReviewTestCompositeResult struct {
	TestPassed bool
	TestReport string
	Review     string
	Ledger     *riview.ReviewLedger
	Err        error
}

// HandleReviewTestComposite implements the composite fast-query command:
//  1. Trigger the dynamic test execution silently or with minimal UI logs.
//  2. Feed the test results (exit codes, failures) into the forensic ledger.
//  3. Trigger the Risk Analysis engine with both git diff AND test reports.
func HandleReviewTestComposite(tests TestExecutor, ledger LedgerInjector, review ReviewRunner) ReviewTestCompositeResult {
	res := ReviewTestCompositeResult{}

	passed, telemetry, err := tests.RunDynamicTests()
	if err != nil {
		res.Err = fmt.Errorf("dynamic test execution failed: %w", err)
		return res
	}
	res.TestPassed = passed
	res.TestReport = telemetry

	if err := ledger.InjectTestTelemetry(passed, telemetry); err != nil {
		res.Err = fmt.Errorf("inject test telemetry to ledger: %w", err)
		return res
	}

	summary, reviewLedger, err := review.RunComprehensiveReview()
	if err != nil {
		res.Err = fmt.Errorf("comprehensive review failed: %w", err)
		return res
	}
	res.Review = summary
	res.Ledger = reviewLedger

	return res
}
