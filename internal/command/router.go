package command

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
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
	RunComprehensiveReview() (summary string, err error)
}

// ReviewTestCompositeResult carries the outcome of the composite pipeline so
// the UI layer can render it deterministically.
type ReviewTestCompositeResult struct {
	TestPassed bool
	TestReport string
	Review     string
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

	summary, err := review.RunComprehensiveReview()
	if err != nil {
		res.Err = fmt.Errorf("comprehensive review failed: %w", err)
		return res
	}
	res.Review = summary

	return res
}

// CommandRouter defines the interface for routing commands to native handlers
type CommandRouter interface {
	Route(input string) (handled bool, cmd tea.Cmd)
}

// NewCommandRouter creates a new command router with all handlers
func NewCommandRouter(sess *session.Session, resolver *modes.Resolver) *Router {
	return &Router{
		sess:     sess,
		resolver: resolver,
	}
}

// Router implements CommandRouter
type Router struct {
	sess     *session.Session
	resolver *modes.Resolver
}

// Route processes input and returns whether it was handled by a native command
func (r *Router) Route(input string) (bool, tea.Cmd) {
	input = strings.TrimSpace(input)
	if input == "" {
		return false, nil
	}

	// Handle bare commands (without slash) that should bypass LLM
	switch input {
	case "run":
		return r.handleRun()
	case "apply":
		return r.handleApply()
	case "build":
		return r.handleBuild()
	case "clear":
		return r.handleClear()
	case "undo":
		return r.handleUndo()
	case "mode":
		return r.handleModeBare()
	}

	// Handle slash commands that should bypass LLM
	if strings.HasPrefix(input, "/") {
		parts := strings.Fields(input)
		if len(parts) == 0 {
			return false, nil
		}

		cmd := parts[0]
		switch cmd {
		case "/run":
			return r.handleRun()
		case "/apply":
			return r.handleApply()
		case "/build":
			return r.handleBuild()
		case "/clear":
			return r.handleClear()
		case "/undo":
			return r.handleUndo()
		case "/mode":
			if len(parts) >= 2 {
				return r.handleModeWithArg(parts[1])
			}
			return r.handleModeBare()
		}
	}

	// Not handled by router, pass to LLM
	return false, nil
}

// Native command handlers - these execute immediately without LLM involvement

func (r *Router) handleRun() (bool, tea.Cmd) {
	// Execute a shell command or internal action
	r.sess.AddMessage("system", "Executing run command...", 1)
	// TODO: Implement actual run logic based on current context
	return true, nil
}

func (r *Router) handleApply() (bool, tea.Cmd) {
	// Apply staged changes
	r.sess.AddMessage("system", "Applying staged changes...", 1)
	// TODO: Implement actual apply logic
	return true, nil
}

func (r *Router) handleBuild() (bool, tea.Cmd) {
	// Trigger build process
	r.sess.AddMessage("system", "Starting build process...", 1)
	// TODO: Implement actual build logic
	return true, nil
}

func (r *Router) handleClear() (bool, tea.Cmd) {
	// Clear conversation + restore banner
	r.sess.AddMessage("system", "Clearing conversation...", 1)
	return true, nil
}

func (r *Router) handleUndo() (bool, tea.Cmd) {
	// Undo last operation
	r.sess.AddMessage("system", "Undoing last operation...", 1)
	// TODO: Implement actual undo logic
	return true, nil
}

func (r *Router) handleModeBare() (bool, tea.Cmd) {
	// Show current mode or help
	current := r.resolver.Current()
	r.sess.AddMessage("system", fmt.Sprintf("Current mode: %s", current), 1)
	return true, nil
}

func (r *Router) handleModeWithArg(modeStr string) (bool, tea.Cmd) {
	// Set mode to specified value
	mode, ok := modes.Parse(modeStr)
	if !ok {
		r.sess.AddMessage("error", fmt.Sprintf("Invalid mode: %s", modeStr), 1)
		return true, nil
	}
	r.sess.SetMode(mode)
	_ = r.sess.Save()
	r.sess.AddMessage("system", fmt.Sprintf("Switched to %s mode", mode), 1)
	return true, nil
}
