package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/providers"
)

// ── TERMINAL EXECUTION EVENT (truthful state transition) ──────────────────
//
// TerminalExecutionMsg is the strongly-typed fatal-termination event for the
// execution control plane. It is emitted when the autonomous Driver (or an
// execution-boundary command) returns an unrecoverable error — for example an
// OpenRouter HTTP 403 ("... is only available on agentic harnesses") — so the
// TUI performs ONE atomic, idempotent termination:
//
//  1. halt every animation/timer tick loop (shimmer, stream, frame, header),
//  2. finalize the foreground operation (cancels the active run context,
//     clears every transient busy flag, stops the spinner, records the
//     terminal outcome),
//  3. unwind the WorkflowStateMachine to StateIdle so the BUILDING header
//     status can never survive a halted engine,
//  4. restore interactive prompt control (StateChat / IDLE, focused input).
//
// The event is a plain tea.Msg with no runtime import so the UI projection
// boundary (the UI never imports internal/runtime/autonomy) stays intact. It
// carries the exact causal error; the raw provider message is preserved
// verbatim through providers.SanitizeAPIError (control-sequence stripping
// only).
type TerminalExecutionMsg struct {
	// Source identifies the terminating layer: "autonomy", "executor", ...
	Source string
	// Cause is the exact terminal error that halted execution.
	Cause error
	// At is the wall-clock time the terminal condition was observed.
	At time.Time
}

var _ tea.Msg = TerminalExecutionMsg{}

// newTerminalExecutionMsg constructs the terminal event for a fatal error.
func newTerminalExecutionMsg(source string, cause error) TerminalExecutionMsg {
	return TerminalExecutionMsg{Source: source, Cause: cause, At: time.Now()}
}

// isRecoverableAutonomyErr reports whether a driver error is a bounded,
// recoverable human-interaction rejection rather than a fatal termination. The
// only such case is an invalid DecisionSurface selection: the driver
// republishes the surface and stays parked, so the UI must NOT terminate.
func isRecoverableAutonomyErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "invalid proposal intent")
}

// handleTerminalExecution performs the atomic truthful termination of a fatal
// execution error. Every step is idempotent, so a duplicate terminal event or
// an overlapping emergency interrupt can never corrupt the operation
// lifecycle. It is the single transition from an active execution to IDLE.
func (m *model) handleTerminalExecution(msg TerminalExecutionMsg) (tea.Model, tea.Cmd) {
	// 1. HALT EVERY ANIMATION/TIMER LOOP.
	// The tick loops are self-terminating: they re-arm only while their
	// lifecycle flags are set, so clearing the flags here guarantees the next
	// frame drops out WITHOUT scheduling another tea.Tick — no lingering
	// timer goroutine and no orphaned spinner after the engine has halted.
	m.execStreamCh = nil
	m.execStreaming = false
	m.streamTickActive = false
	m.frameTickActive = false
	m.stopShimmer()
	// Mark the model stage failed so no "waiting"/"streaming" indicator can
	// survive the terminal error.
	m.setStage("model", m.getActiveModelName(), stageFailed)

	// 2. RELEASE THE AUTONOMOUS PRESENTATION LANE.
	// The driver's terminal path is now this event itself; any parked boundary
	// is void (there is nothing left to resume).
	m.autonomousActive = false
	m.autonomousBoundary = nil
	m.autonomousSelect = 0

	// 3. FINALIZE THE FOREGROUND OPERATION.
	// The single authoritative terminal cleanup: cancels the active run
	// context (releasing any provider/subprocess worker), clears transient
	// busy flags, stops the spinner, and records the terminal outcome.
	m.finalizeOperation(classifyOpErr(msg.Cause), msg.Cause)

	// 4. UNWIND THE WORKFLOW STATE MACHINE.
	// finalizeOperation clears the busy flags, but the phase machine still
	// owns the workflow state; without this reset the header would keep
	// rendering BUILDING after the engine halted. EventReset is valid from
	// every state and returns to StateIdle, from which all modes are reachable.
	m.unwindBuildFailure()

	// 5. RESTORE INTERACTIVE PROMPT CONTROL.
	// unwindBuildFailure focuses the input and re-derives the presentation;
	// recalc/sync keep the viewport geometry and derived state truthful after
	// the stage and loading rows collapse.
	m.recalcViewportHeight()
	m.syncUIState()

	// 6. REPORT THE EXACT CAUSAL ERROR. The raw provider text is preserved;
	// sanitization only strips terminal-hostile control sequences.
	if msg.Cause != nil {
		m.push(roleError, terminalExecutionLabel(msg)+providers.SanitizeAPIError(msg.Cause))
	}

	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return m, m.flushPendingRecords()
}

// terminalExecutionLabel renders the human prefix for the terminating layer.
func terminalExecutionLabel(msg TerminalExecutionMsg) string {
	switch msg.Source {
	case "autonomy":
		return "[autonomous] terminal failure: "
	case "executor":
		return "execution failed: "
	default:
		return "execution terminated: "
	}
}

// ── §20.2 HARD-DEADLINE CANCELLATION FALLBACK ─────────────────────────────
//
// Soft cancellation (Esc / Ctrl+C) cancels the active run context immediately
// and waits for the execution to confirm termination via TerminalExecutionMsg
// (or an equivalent terminal message). A wedged worker that never observes the
// cancellation must never hold the input lock or leave a stale BUILDING /
// processing state, so the emergency-interrupt path also arms a single
// non-blocking watchdog:
//
//	Esc / Ctrl+C → root context cancelled (soft)
//	             → arm terminalDeadlineMsg in 250ms
//	             → if not confirmed: force-detach the execution handle,
//	               unwind the WorkflowStateMachine to IDLE, release input.
//
// The watchdog carries a sequence number so a stale tick can never detach a
// newer run (beginOperation and every fresh arm advance the sequence).

// terminalDetachDeadline is the §20.2 hard deadline within which a cancelled
// execution must confirm termination before the TUI detaches from it.
const terminalDetachDeadline = 250 * time.Millisecond

// terminalDeadlineMsg fires terminalDetachDeadline after a cancellation signal.
// seq invalidates the watch when a newer operation begins or a fresher watch is
// armed, so a stale tick can never detach an unrelated run.
type terminalDeadlineMsg struct{ seq uint64 }

var _ tea.Msg = terminalDeadlineMsg{}

// armTerminalDeadline schedules the hard-deadline watchdog. It is armed only
// from the emergency-interrupt path (i.e. after a real cancellation signal).
func (m *model) armTerminalDeadline() tea.Cmd {
	m.cancelDeadlineSeq++
	seq := m.cancelDeadlineSeq
	return tea.Tick(terminalDetachDeadline, func(time.Time) tea.Msg {
		return terminalDeadlineMsg{seq: seq}
	})
}

// handleTerminalDeadline enforces the hard-deadline contract: if the active
// execution has not confirmed termination by the deadline, detach its handle
// and force-reset the presentation to IDLE. It is a no-op when termination
// already landed or the watch was superseded.
func (m *model) handleTerminalDeadline(msg terminalDeadlineMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.cancelDeadlineSeq {
		return m, nil // superseded by a newer operation or a fresher watch
	}
	if !m.executionInFlight() {
		return m, nil // termination already confirmed — nothing to detach
	}
	return m.forceDetachExecution()
}

// executionInFlight reports whether any execution surface still owns the TUI
// (the operation handle, the autonomous lane, or a live stream/agent).
func (m *model) executionInFlight() bool {
	return m.activeOp != nil || m.autonomousActive || m.execStreaming || m.streaming || m.agentRunning
}

// forceDetachExecution is the §20.2 fallback: it detaches the active execution
// handle, cancels and drops every cancellation root, unwinds the workflow
// machine to IDLE, and releases the input lock — without waiting for the wedged
// worker. Go cannot preempt a goroutine, so the handle is detached rather than
// killed; the worker's late terminal message remains idempotent (finalize and
// unwind are no-ops once the operation is released and the phase is IDLE).
func (m *model) forceDetachExecution() (tea.Model, tea.Cmd) {
	// 1. Detach cancellation roots so the worker can never keep the TUI locked.
	if m.activeOp != nil && m.activeOp.Cancel != nil {
		m.activeOp.Cancel()
	}
	m.cancelAllBackgroundContexts()
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	if m.shellCancel != nil {
		m.shellCancel()
		m.shellCancel = nil
	}
	execution.KillAllOrphans()

	// 2. Halt every animation/timer and drop the autonomous presentation lane.
	m.execStreamCh = nil
	m.execStreaming = false
	m.streamTickActive = false
	m.frameTickActive = false
	m.stopShimmer()
	m.setStage("model", m.getActiveModelName(), stageCancelled)
	m.autonomousActive = false
	m.autonomousBoundary = nil
	m.autonomousSelect = 0

	// 3. Release the operation (cancellation is the truthful terminal outcome).
	m.finalizeOperation(OpOutcomeCancelled, nil)

	// 4. Unwind the workflow phase and restore prompt control.
	m.unwindBuildFailure()
	m.recalcViewportHeight()
	m.syncUIState()

	// 5. Invalidate any other pending watch for this generation.
	m.cancelDeadlineSeq++

	m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
		"[ ABORT ] Execution did not confirm termination within %s — detached and reset to idle.",
		terminalDetachDeadline)))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return m, m.flushPendingRecords()
}
