package command

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/undo"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/internal/session"
)

// UndoHandler performs multi-level undo operations. Implemented by undo.Handler.
type UndoHandler interface {
	Undo() (*undo.Result, error)
	UndoSession() (*undo.Result, error)
	UndoByMode(mode undo.Mode) (*undo.Result, error)
}

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

// CommandRouter defines the interface for routing commands to native handlers
type CommandRouter interface {
	Route(input string) (handled bool, cmd tea.Cmd)
}

// NewCommandRouter creates a new command router with all handlers
func NewCommandRouter(sess *session.Session, resolver *modes.Resolver, undoH UndoHandler) *Router {
	return &Router{
		sess:     sess,
		resolver: resolver,
		undo:     undoH,
	}
}

// Router implements CommandRouter
type Router struct {
	sess     *session.Session
	resolver *modes.Resolver
	undo     UndoHandler
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
		return r.handleUndoWithMode([]string{"undo"})
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
			return r.handleUndoWithMode(parts)
		case "/commit":
			return r.handleCommit()
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

func (r *Router) handleUndoWithMode(parts []string) (bool, tea.Cmd) {
	if r.undo == nil {
		r.sess.AddMessage("system", "Undo handler not available (checkpoint engine not initialized)", 1)
		return true, nil
	}

	// Parse the undo mode (/undo, /undo session, /undo --all).
	input := strings.Join(parts, " ")
	undoMode := undo.Parse(input)
	var result *undo.Result
	var err error

	switch undoMode {
	case undo.ModeSession:
		result, err = r.undo.UndoSession()
		if err != nil {
			r.sess.AddMessage("error", fmt.Sprintf("Session undo failed: %v", err), 1)
			return true, nil
		}
	default:
		result, err = r.undo.Undo()
		if err != nil {
			r.sess.AddMessage("error", fmt.Sprintf("Undo failed: %v", err), 1)
			return true, nil
		}
	}

	status := "system"
	if !result.Success {
		status = "error"
	}
	r.sess.AddMessage(status, result.Message, 1)
	return true, nil
}

func (r *Router) handleCommit() (bool, tea.Cmd) {
	// Stage all changes and create a conventional commit.
	// This runs git add -A && git commit with the aggregate diff.
	r.sess.AddMessage("system", "Creating conventional commit from session changes...", 1)
	// The actual commit logic is handled by the build/commit engine,
	// invoked from the TUI layer. This router handler gates access.
	return true, nil
}
