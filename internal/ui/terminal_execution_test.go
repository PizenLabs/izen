package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/httpx"
)

// ── PHASE 12 ADDENDUM: TRUTHFUL TERMINAL STATE & CANCELLATION ──────────────
//
// These tests lock the control-plane invariant: a fatal execution error (e.g.
// OpenRouter HTTP 403) must atomically move the TUI out of BUILDING into IDLE,
// halt every timer/spinner, and never leave an orphaned operation behind.

// terminal403 models the OpenRouter agentic-harness refusal surfaced to the TUI.
func terminal403() error {
	return httpx.ParseProviderError("openrouter", 403,
		[]byte(`{"error":{"message":"thinkingmachines/inkling-small:free is only available on agentic harnesses.","code":403}}`))
}

// buildPhaseModel returns a chat-ready model whose WorkflowStateMachine is in
// the BUILDING phase — the exact state the header renders as "BUILDING".
func buildPhaseModel(t *testing.T) *model {
	t.Helper()
	m := readyChatModel(newTestModel())
	if err := m.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("setup plan event: %v", err)
	}
	if err := m.workflowSM.SendEvent(workflow.EventBuild, workflow.TransitionContext{
		HasPlan:         true,
		HasCapabilities: true,
	}); err != nil {
		t.Fatalf("setup build event: %v", err)
	}
	if got := m.workflowSM.State(); got != workflow.StateBuilding {
		t.Fatalf("setup: phase = %v, want Building", got)
	}
	return m
}

// TestTerminalExecutionMsgHaltsTimersAndUnwindsBuildPhase is the regression
// test for the reported defect: a fatal execution error must stop the spinner
// and running timer, clear the BUILDING header status, and restore IDLE input
// control — synchronously, with no lingering tick loop.
func TestTerminalExecutionMsgHaltsTimersAndUnwindsBuildPhase(t *testing.T) {
	m := buildPhaseModel(t)

	// Simulate an in-flight autonomous execution with every animation armed.
	m.beginOperation(OpAutonomous)
	m.autonomousActive = true
	m.execStreamCh = make(chan tea.Msg, 8)
	m.execStreaming = true
	m.shimmerActive = true
	m.streamTickActive = true
	m.frameTickActive = true
	m.spinnerFrame = 5

	start := time.Now()
	resModel, _ := m.Update(TerminalExecutionMsg{Source: "autonomy", Cause: terminal403(), At: time.Now()})
	elapsed := time.Since(start)
	m2 := resModel.(*model)

	// The transition is a synchronous, no-I/O state change; it must be atomic
	// and effectively instantaneous.
	if elapsed > 50*time.Millisecond {
		t.Fatalf("terminal transition took %s, want < 50ms", elapsed)
	}
	// The BUILDING header status must clear (the phase machine is Idle).
	if got := m2.workflowSM.State(); got != workflow.StateIdle {
		t.Fatalf("workflow phase = %v, want Idle (BUILDING header must clear)", got)
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat (IDLE)", m2.state)
	}
	// Every timer/spinner surface must be halted.
	if m2.shimmerActive {
		t.Fatal("shimmer must be halted on terminal execution error")
	}
	if m2.execStreaming {
		t.Fatal("execStreaming must be cleared on terminal execution error")
	}
	if m2.autonomousActive {
		t.Fatal("autonomousActive must be cleared on terminal execution error")
	}
	if m2.activeOp != nil {
		t.Fatal("active operation must be finalized on terminal execution error")
	}
	if !m2.executionStartedAt.IsZero() {
		t.Fatal("execution timer baseline must be cleared on terminal execution error")
	}
	if m2.streamTickActive || m2.frameTickActive {
		t.Fatal("tick loops must not remain armed after terminal execution error")
	}
	if !m2.ti.Focused() {
		t.Fatal("prompt input must be restored after terminal execution error")
	}
	// The exact causal provider message must survive to the user (truthfulness).
	if !strings.Contains(recordsText(m2), "agentic harnesses") {
		t.Fatalf("terminal error must surface the causal provider message, got:\n%s", recordsText(m2))
	}
	// No orphaned timer: neither animation tick may re-arm itself.
	if _, cmd := m2.Update(smoothStreamTickMsg(time.Now())); cmd != nil {
		t.Fatal("smooth-stream tick must not re-arm after terminal execution error")
	}
	if _, cmd := m2.Update(shimmerFrameMsg{}); cmd != nil {
		t.Fatal("shimmer tick must not re-arm after terminal execution error")
	}
}

// TestExecutionResultFailureUnwindsBuildPhase proves the RuntimeExecutor
// terminal-error projection (gated /build path) also releases the workflow
// phase — the same defect class, on the non-autonomous path.
func TestExecutionResultFailureUnwindsBuildPhase(t *testing.T) {
	m := buildPhaseModel(t)
	m.beginOperation(OpBuild)

	res := &execution.ExecutionResult{RequestID: "req-terminal"}
	resModel, _ := m.executionResultUpdate(executionResultMsg{
		res: res,
		err: errors.New("openrouter: model unavailable for Izen's current execution path"),
	})
	m2 := resModel.(*model)

	if got := m2.workflowSM.State(); got != workflow.StateIdle {
		t.Fatalf("workflow phase = %v, want Idle after terminal executor failure", got)
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after terminal executor failure", m2.state)
	}
	if m2.activeOp != nil {
		t.Fatal("active operation must be finalized after terminal executor failure")
	}
}

// TestFatalDriverErrorEmitsTerminalExecutionMsg proves the autonomous bridge
// converts an unrecoverable driver error into the strongly-typed terminal
// event (not a generic run message), and that feeding it back terminates
// truthfully.
func TestFatalDriverErrorEmitsTerminalExecutionMsg(t *testing.T) {
	drv := &fakeAutonomousDriver{runErr: errors.New("openrouter: model unavailable for Izen's current execution path")}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("do the thing @note.txt")
	if cmd == nil {
		t.Fatal("runAutonomousDriver must return a command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("want tea.BatchMsg, got %T", cmd())
	}
	var term TerminalExecutionMsg
	found := false
	for _, c := range batch {
		if msg, ok := c().(TerminalExecutionMsg); ok {
			term = msg
			found = true
		}
	}
	if !found {
		t.Fatal("a fatal driver error must emit a strongly-typed TerminalExecutionMsg")
	}
	if term.Source != "autonomy" {
		t.Fatalf("Source = %q, want autonomy", term.Source)
	}
	if term.Cause == nil {
		t.Fatal("terminal event must carry the causal error")
	}

	resModel, _ := m.Update(term)
	m2 := resModel.(*model)
	if m2.autonomousActive {
		t.Fatal("terminal event must clear autonomousActive")
	}
	if m2.activeOp != nil {
		t.Fatal("terminal event must finalize the active operation")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after terminal event", m2.state)
	}
}

// TestRecoverableDriverErrorStaysAutonomousRunMsg proves a bounded,
// recoverable DecisionSurface rejection is NOT treated as a fatal termination:
// the run stays parked and the UI must not tear it down.
func TestRecoverableDriverErrorStaysAutonomousRunMsg(t *testing.T) {
	drv := &fakeAutonomousDriver{
		parkOnRun: true,
		runErr:    errors.New("autonomy: invalid proposal intent selected"),
	}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("do the thing @note.txt")
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("want tea.BatchMsg, got %T", cmd())
	}
	sawRunMsg := false
	for _, c := range batch {
		switch msg := c().(type) {
		case TerminalExecutionMsg:
			t.Fatalf("recoverable rejection must not emit TerminalExecutionMsg: %v", msg.Cause)
		case autonomousRunMsg:
			if msg.err != nil {
				sawRunMsg = true
			}
		}
	}
	if !sawRunMsg {
		t.Fatal("recoverable rejection must stay an error-carrying autonomousRunMsg")
	}
}

// ── §20.2 HARD-DEADLINE CANCELLATION FALLBACK ─────────────────────────────

// TestEmergencyInterruptArmsHardDeadlineWatchdog proves soft cancellation arms
// the 250ms watchdog while leaving finalization to the driver's terminal
// message (the canonical path). The deadline is the backstop, not the primary.
func TestEmergencyInterruptArmsHardDeadlineWatchdog(t *testing.T) {
	m := buildPhaseModel(t)
	m.beginOperation(OpAutonomous)
	m.autonomousActive = true
	m.execStreaming = true

	before := m.cancelDeadlineSeq
	m.handleEmergencyInterrupt("test")

	if m.cancelDeadlineSeq == before {
		t.Fatal("emergency interrupt on an active run must arm the hard-deadline watchdog")
	}
	// Soft cancellation does NOT immediately finalize the autonomous operation;
	// the driver's terminal message owns that cleanup. The watchdog is armed.
	if m.activeOp == nil {
		t.Fatal("soft cancellation must not finalize the autonomous operation immediately")
	}
	if !m.autonomousActive {
		t.Fatal("autonomous lane must stay active until the terminal message or the deadline")
	}
}

// TestTerminalDeadlineDetachesUnconfirmedExecution is the deterministic §20.2
// test: when the watchdog fires and no terminal message has arrived, the TUI
// detaches the execution handle, unwinds to IDLE, and releases the input lock.
func TestTerminalDeadlineDetachesUnconfirmedExecution(t *testing.T) {
	m := buildPhaseModel(t)
	m.beginOperation(OpAutonomous)
	m.autonomousActive = true
	m.execStreamCh = make(chan tea.Msg, 8)
	m.execStreaming = true
	m.shimmerActive = true
	m.streamTickActive = true
	m.frameTickActive = true

	_ = m.armTerminalDeadline()
	seq := m.cancelDeadlineSeq

	start := time.Now()
	resModel, _ := m.Update(terminalDeadlineMsg{seq: seq})
	elapsed := time.Since(start)
	m2 := resModel.(*model)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("hard-deadline detach took %s, want < 50ms", elapsed)
	}
	if got := m2.workflowSM.State(); got != workflow.StateIdle {
		t.Fatalf("workflow phase = %v, want Idle (BUILDING must clear on detach)", got)
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat (input released)", m2.state)
	}
	if m2.autonomousActive {
		t.Fatal("hard-deadline detach must clear autonomousActive")
	}
	if m2.activeOp != nil {
		t.Fatal("hard-deadline detach must release the operation handle")
	}
	if m2.shimmerActive || m2.execStreaming || m2.streamTickActive || m2.frameTickActive {
		t.Fatal("hard-deadline detach must halt every timer/spinner surface")
	}
	if !m2.ti.Focused() {
		t.Fatal("hard-deadline detach must restore prompt input control")
	}
	if !strings.Contains(recordsText(m2), "did not confirm termination") {
		t.Fatalf("hard-deadline detach must report the detach, got:\n%s", recordsText(m2))
	}
}

// TestTerminalDeadlineNoOpAfterConfirmedTermination proves the watchdog is a
// no-op once the execution has already confirmed termination.
func TestTerminalDeadlineNoOpAfterConfirmedTermination(t *testing.T) {
	m := buildPhaseModel(t)
	m.beginOperation(OpAutonomous)
	m.autonomousActive = true
	m.execStreaming = true

	_ = m.armTerminalDeadline()
	seq := m.cancelDeadlineSeq

	resModel, _ := m.Update(TerminalExecutionMsg{Source: "autonomy", Cause: errors.New("boom")})
	m2 := resModel.(*model)
	if m2.activeOp != nil || m2.autonomousActive {
		t.Fatal("terminal event must release the operation before the deadline")
	}

	after, _ := m2.Update(terminalDeadlineMsg{seq: seq})
	m3 := after.(*model)
	if strings.Contains(recordsText(m3), "did not confirm termination") {
		t.Fatal("confirmed termination must make the deadline watchdog a no-op")
	}
	if m3.workflowSM.State() != workflow.StateIdle {
		t.Fatalf("phase = %v, want Idle", m3.workflowSM.State())
	}
}

// TestTerminalDeadlineStaleSeqDoesNotDetachNewOperation proves a stale watchdog
// tick cannot detach a run that started after it was armed.
func TestTerminalDeadlineStaleSeqDoesNotDetachNewOperation(t *testing.T) {
	m := buildPhaseModel(t)
	m.beginOperation(OpAutonomous)
	m.autonomousActive = true
	_ = m.armTerminalDeadline()
	staleSeq := m.cancelDeadlineSeq

	// A newer operation begins: beginOperation invalidates the pending watch.
	m.autonomousActive = false
	m.beginOperation(OpBuild)

	resModel, _ := m.Update(terminalDeadlineMsg{seq: staleSeq})
	m2 := resModel.(*model)
	if m2.activeOp == nil {
		t.Fatal("a stale deadline tick must not detach a newer operation")
	}
	if m2.workflowSM.State() != workflow.StateBuilding {
		t.Fatalf("phase = %v, want Building (untouched by stale tick)", m2.workflowSM.State())
	}
}
