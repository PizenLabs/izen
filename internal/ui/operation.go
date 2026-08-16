package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
)

// Foreground operation lifecycle.
//
// Every long-running execution path (hotfix generation, build apply, provider
// calls, subprocesses) runs as a single authoritative foreground operation:
//
//	IDLE → DISPATCHED → RUNNING → terminal outcome → FINALIZING → IDLE
//
// Terminal outcomes are SUCCESS, AMBIGUOUS, FAILURE, CANCELLED, TIMEOUT. Every
// execution path MUST reach a terminal state through exactly one finalization
// path (model.finalizeOperation) so cleanup — active-operation ownership, busy
// flags, spinner state, cancellation resources — is never duplicated across
// branches.
//
// Human interaction NEVER keeps an execution worker blocked: AMBIGUOUS is a
// terminal outcome of the operation that produced it; continuing (Clarify /
// Inspect / Candidate selection / approval) creates a NEW operation when actual
// execution resumes.

// OperationKind discriminates the kind of foreground operation.
type OperationKind string

// Canonical operation kinds.
const (
	OpHotfix      OperationKind = "hotfix"
	OpBuild       OperationKind = "build"
	OpInvestigate OperationKind = "investigate"
	OpReview      OperationKind = "review"
	OpAsk         OperationKind = "ask"
	OpPlan        OperationKind = "plan"
	OpShell       OperationKind = "shell"
)

// String returns the canonical kind name.
func (k OperationKind) String() string { return string(k) }

// OperationState is the lifecycle state of a foreground operation.
type OperationState string

// Lifecycle states.
const (
	OpStateIdle       OperationState = "idle"
	OpStateDispatched OperationState = "dispatched"
	OpStateRunning    OperationState = "running"
	OpStateFinalizing OperationState = "finalizing"
	OpStateTerminal   OperationState = "terminal"
)

// String returns the lifecycle state name.
func (s OperationState) String() string { return string(s) }

// OperationOutcome is a terminal outcome of a foreground operation.
type OperationOutcome string

// Terminal outcomes.
const (
	OpOutcomeSuccess   OperationOutcome = "success"
	OpOutcomeAmbiguous OperationOutcome = "ambiguous"
	OpOutcomeFailure   OperationOutcome = "failure"
	OpOutcomeCancelled OperationOutcome = "cancelled"
	OpOutcomeTimeout   OperationOutcome = "timeout"
)

// String returns the terminal outcome name.
func (o OperationOutcome) String() string { return string(o) }

// operation is the single authoritative foreground-operation record. It is
// owned exclusively by the model and is only ever mutated on the Bubble Tea UI
// goroutine (in Update/command handlers), so it needs no locking. The Ctx and
// Cancel fields are safe to read from worker goroutines (context is
// concurrency-safe); Cancel is invoked from the UI goroutine and observed by
// workers.
type operation struct {
	ID    string
	Kind  OperationKind
	Ctx   context.Context
	State OperationState

	// Cancel cancels Ctx. It is the one authoritative cancellation handle for
	// every worker spawned under this operation (provider requests,
	// subprocesses, background goroutines).
	Cancel context.CancelFunc

	// Outcome is set when the operation reaches a terminal state.
	Outcome OperationOutcome
	Err     error

	// StartedAt is the wall-clock start of the operation.
	StartedAt time.Time
	// LastProgress is the timestamp of the last meaningful runtime progress.
	// The watchdog uses it to distinguish a genuine long-running worker from a
	// stuck one. It is only updated on the UI goroutine (no data races).
	LastProgress time.Time

	// Telemetry is the authoritative per-operation execution record. It
	// attributes wall-clock latency to real runtime stages (target, read,
	// model/provider, patch, validate, apply) and to provider sub-phases
	// (request → waiting → first-token → streaming → terminal). It is created
	// at dispatch, fed from the stage boundaries the worker goroutines reach,
	// and finalized at the operation terminal. It is never nil for a
	// foreground operation. See internal/execution/telemetry.go.
	Telemetry *execution.Telemetry
}

// running reports whether the operation is still owned by the runtime (not yet
// terminal).
func (op *operation) running() bool {
	return op != nil && op.State != OpStateTerminal
}

// describe renders a compact diagnostic descriptor for the watchdog.
func (op *operation) describe() string {
	if op == nil {
		return "none"
	}
	return fmt.Sprintf("op=%s kind=%s state=%s started=%s progress=%s",
		op.ID, op.Kind, op.State, fmtDuration(op.StartedAt), fmtDuration(op.LastProgress))
}

func fmtDuration(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Millisecond).String()
}

// nextOperationID returns a monotonically increasing operation ID.
func (m *model) nextOperationID() string {
	m.opIDCounter++
	return fmt.Sprintf("op-%d", m.opIDCounter)
}

// beginOperation registers a new foreground operation as the single
// authoritative active operation. It cancels any stale operation first, resets
// the double-Ctrl+C force-exit grace, and arms the transient busy flags so the
// derived presentation state enters StateProcessing immediately.
func (m *model) beginOperation(kind OperationKind) *operation {
	ctx, cancel := context.WithCancel(context.Background())
	// ── ACTION SURFACE OWNERSHIP (Phase 7) ────────────────────────────
	// A new operation owns the action surface: any chips left over from a
	// PREVIOUS operation's result are stale the moment actual execution begins.
	// They disappear here (centralized) so no completed operation's actions can
	// be re-triggered by a newer one. The operation's own terminal result
	// message re-populates the surface with its valid transitions.
	m.currentResult = nil
	// A new foreground operation reopens the activity surface sealed by /clear:
	// this operation's events are a fresh execution and belong in the viewport.
	m.unsealActivitySurface()
	op := &operation{
		ID:           m.nextOperationID(),
		Kind:         kind,
		Ctx:          ctx,
		Cancel:       cancel,
		State:        OpStateDispatched,
		StartedAt:    time.Now(),
		LastProgress: time.Now(),
	}
	// ── EXECUTION TELEMETRY (Phase 3) ────────────────────────────────
	// Every foreground operation carries the authoritative execution record.
	// It is fed from the real stage boundaries the worker goroutines reach
	// (setStage / setStageMetrics) and finalized on the terminal outcome so
	// stage timestamps never outlive the operation. The operation ID is the
	// stable identity every telemetry event traces back to. The record is
	// attached to the mutex-protected authoritative stage so workers publish
	// into it race-safely without touching the UI-owned activeOp pointer.
	op.Telemetry = execution.NewTelemetry(op.ID, string(kind))
	op.Telemetry.Workers().Spawn("operation")
	if m.stage == nil {
		m.stage = &execStage{}
	}
	m.stage.mu.Lock()
	m.stage.Telemetry = op.Telemetry
	m.stage.mu.Unlock()
	// A new operation supersedes any previous one (single-ownership rule).
	if m.activeOp != nil {
		m.activeOp.Cancel()
	}
	m.activeOp = op
	m.cancelGraceDeadline = time.Time{}
	m.streaming = false
	m.agentRunning = true
	m.agentDone = false
	// A new operation supersedes the gated-execution resolving phase: its
	// terminal events must never clear a newer operation's loading state.
	m.executionResolving = false
	m.reviewRunning = false
	m.investigateRunning = false
	m.pipelineRunning = false
	m.planPending = false
	m.shellRunning = false
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	m.lastAgentActivity = time.Now()
	m.lastActionTime = time.Now()
	// Start the authoritative execution stage for the new operation. The
	// progress UI derives its indicator from this stage — never from a
	// fabricated label.
	m.resetStage(kind)
	m.syncUIState()
	return op
}

// operationContext returns the cancellable context of the active operation, or
// a fresh background context when none is active (defensive for direct cmd
// callers in harnesses). Workers must derive their contexts from this so a
// Ctrl+C cancellation propagates all the way into the provider/subprocess.
func (m *model) operationContext() context.Context {
	if m.activeOp != nil && m.activeOp.Ctx != nil {
		return m.activeOp.Ctx
	}
	return context.Background()
}

// finalizeOperation is THE single authoritative terminal cleanup path. Every
// execution outcome — SUCCESS, AMBIGUOUS, FAILURE, CANCELLED, TIMEOUT, or a
// panic recovered at a worker boundary — flows through here. It:
//
//  1. moves the operation to FINALIZING,
//  2. cancels the operation context (releases provider/subprocess/worker),
//  3. records the terminal outcome + error,
//  4. releases the active-operation ownership,
//  5. clears every transient busy flag and stops the spinner,
//  6. re-derives the presentation state.
//
// It is idempotent: a terminal message that arrives after the operation was
// already finalized (e.g. a cancelled provider call) simply re-runs the same
// cleanup.
func (m *model) finalizeOperation(outcome OperationOutcome, err error) {
	if m.activeOp != nil {
		op := m.activeOp
		op.State = OpStateFinalizing
		op.Outcome = outcome
		op.Err = err
		if op.Cancel != nil {
			op.Cancel()
		}
		op.LastProgress = time.Now()
		op.State = OpStateTerminal
		// ── EXECUTION TELEMETRY TERMINALIZATION (Phase 3) ─────────────
		// The authoritative execution record is closed with the operation's
		// terminal outcome so no stage span can survive the operation. The
		// retained snapshot backs the debug/inspect view.
		if op.Telemetry != nil {
			op.Telemetry.Workers().Release("operation")
			op.Telemetry.Finalize(outcome.String())
			m.lastExecutionSnapshot = op.Telemetry.Snapshot()
			m.lastExecutionTelemetry = op.Telemetry
		}
		m.activeOp = nil
	}
	// The authoritative stage is terminalized alongside the operation so no
	// progress indicator can survive the operation that produced it.
	m.finishStage(outcome)
	m.clearBusyFlags()
	m.stopShimmer()
	m.syncUIState()
}

// finalizeBuildOperation finalizes the active build-patch operation (OpBuild)
// if one is in flight. It is the single terminal cleanup path for the legacy
// per-task FILE_MUTATE / GIT_ACTION patch-generation path: the operation begun
// in handleBuildRun is released here on EVERY terminal message — proposal
// ready, failure, cancellation, or the zero-patch short-circuit — so the
// "Processing file mutations..." spinner can never survive the generation
// phase. It is idempotent: when the operation was already finalized (e.g.
// superseded by a Ctrl+C cancellation), it is a no-op. err is classified into
// SUCCESS / CANCELLED / TIMEOUT / FAILURE exactly like every other worker.
func (m *model) finalizeBuildOperation(err error) {
	if m.activeOp != nil && m.activeOp.Kind == OpBuild {
		outcome, outErr := classifyOpErrWithErr(err)
		m.finalizeOperation(outcome, outErr)
	}
}

// cancelActiveOperation gracefully cancels the active foreground operation and
// runs the universal emergency reset. It is the Ctrl+C / Esc / OS-signal
// cancellation entry point.
func (m *model) cancelActiveOperation(reason string) (tea.Model, tea.Cmd) {
	return m.handleEmergencyInterrupt(reason)
}

// handleCtrlC implements the unified Ctrl+C cancellation protocol:
//
//   - First Ctrl+C: graceful cancellation — cancels the active operation (or
//     dismisses the ambiguity card) and returns an interrupt command. The
//     force-exit grace window is armed.
//   - Second Ctrl+C while a cancellation is still in progress (within the
//     grace window): hard exit with status 130.
//   - Idle chat: returns handled=false so the normal chat Ctrl+C behavior
//     (clear the input buffer) runs unchanged.
//
// handled=false means no cancellation was applicable and the caller should fall
// through to the legacy Ctrl+C handling.
func (m *model) handleCtrlC() (bool, tea.Cmd) {
	if !m.cancelGraceDeadline.IsZero() && time.Now().Before(m.cancelGraceDeadline) {
		m.hardExit130()
		return true, nil
	}
	switch {
	case m.state == StateHotfixAmbiguous:
		m.armCancelGrace()
		_, cmd := m.cancelHotfixAmbiguous()
		return true, cmd
	case m.activeOp != nil || m.isWorkflowBusy():
		m.armCancelGrace()
		_, cmd := m.cancelActiveOperation("ctrl-c")
		return true, cmd
	}
	return false, nil
}

// armCancelGrace arms the double-Ctrl+C force-exit window.
func (m *model) armCancelGrace() {
	m.cancelGraceDeadline = time.Now().Add(cancelGraceWindow)
}

// hardExit130 forces termination with status 130. It restores the terminal
// (best effort) so the shell does not inherit raw mode. Tests override
// hardExitFn.
func (m *model) hardExit130() {
	if m.program != nil {
		_ = m.program.RestoreTerminal()
	}
	hardExitFn(130)
}

// classifyOpErr maps a worker error onto a terminal outcome, keeping the
// lifecycle truthful about why an operation ended (cancellation and timeout are
// distinct from a hard failure).
func classifyOpErr(err error) OperationOutcome {
	if err == nil {
		return OpOutcomeSuccess
	}
	if isContextCancelled(err) {
		return OpOutcomeCancelled
	}
	if isContextDeadline(err) {
		return OpOutcomeTimeout
	}
	return OpOutcomeFailure
}

// isContextCancelled reports whether err is (or wraps) context.Canceled.
func isContextCancelled(err error) bool {
	return err != nil && errors.Is(err, context.Canceled)
}

// isContextDeadline reports whether err is (or wraps) context.DeadlineExceeded.
func isContextDeadline(err error) bool {
	return err != nil && errors.Is(err, context.DeadlineExceeded)
}

// classifyOpErrWithErr returns both the outcome and the error, nil when the
// outcome is success.
func classifyOpErrWithErr(err error) (OperationOutcome, error) {
	if err == nil {
		return OpOutcomeSuccess, nil
	}
	if isContextCancelled(err) {
		return OpOutcomeCancelled, nil
	}
	if isContextDeadline(err) {
		return OpOutcomeTimeout, nil
	}
	return OpOutcomeFailure, err
}

// ── Watchdog / stuck detection ────────────────────────────────────────────

// opWatchdogInterval is how often the operation watchdog ticks.
const opWatchdogInterval = 1 * time.Second

// opWatchdogStuckAfter is how long an operation may go without any meaningful
// progress before the watchdog emits a diagnostic warning. It is deliberately
// generous: the watchdog must never kill normal long-running work — it only
// reports.
const opWatchdogStuckAfter = 45 * time.Second

// watchdogMsg is the diagnostic tick that drives the operation watchdog. It is
// a private message type; the watchdog self-schedules via opWatchdogCmd.
type watchdogMsg struct{ now time.Time }

// opWatchdogCmd returns the watchdog tick command. It self-reschedules as long
// as an operation is active, and returns nil when the runtime is idle so the
// loop never leaks.
func (m *model) opWatchdogCmd() tea.Cmd {
	if m.activeOp == nil {
		return nil
	}
	return tea.Tick(opWatchdogInterval, func(t time.Time) tea.Msg {
		return watchdogMsg{now: t}
	})
}

// handleWatchdog processes one watchdog tick. For every active operation it
// reports — never kills — when no meaningful progress has been observed for
// longer than opWatchdogStuckAfter.
func (m *model) handleWatchdog(w watchdogMsg) tea.Cmd {
	op := m.activeOp
	if op == nil {
		return nil
	}
	if op.running() && !op.LastProgress.IsZero() {
		idle := w.now.Sub(op.LastProgress)
		if idle >= opWatchdogStuckAfter {
			m.push(roleActivity, fmt.Sprintf(
				"  ⚠ watchdog: %s stalled %s without progress — press Ctrl+C to cancel", op.describe(), idle.Round(time.Second)))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			op.LastProgress = w.now // re-arm: warn once per window, never spam
		}
	}
	return m.opWatchdogCmd()
}
