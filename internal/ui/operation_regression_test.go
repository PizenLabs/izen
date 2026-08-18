package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
)

// blockingProvider blocks Execute until its context is cancelled, proving
// provider-cancellation propagation end to end.
type blockingProvider struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (b *blockingProvider) Name() string { return "blocking" }

func (b *blockingProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	if b.started != nil {
		close(b.started)
	}
	select {
	case <-ctx.Done():
		if b.cancelled != nil {
			close(b.cancelled)
		}
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("provider was never cancelled")
	}
}

func (b *blockingProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

// ── D. Subprocess cancellation ────────────────────────────────────────────

// TestRegressionSubprocessCancellation asserts Ctrl+C terminates a running
// child process (context-aware exec.CommandContext) and the runtime survives.
func TestRegressionSubprocessCancellation(t *testing.T) {
	m := newTestModel()
	// `exec sleep` replaces bash with the sleep process so the direct child IS
	// the long-running process — the context-cancellation kill terminates it
	// deterministically (no grandchild inheriting the output pipes).
	cmd := m.streamShellCmd("exec sleep 30")
	if cmd == nil {
		t.Fatal("streamShellCmd returned nil")
	}
	if !m.shellRunning || m.shellCancel == nil {
		t.Fatal("shell not armed")
	}

	// Drain the shell channel in the background. The channel reference is
	// captured BEFORE the interrupt (the interrupt tears it down), so the
	// drain goroutine must bind to the pre-interrupt channel.
	shellCh := m.shellCh
	var exitCh = make(chan shellExitMsg, 1)
	go func() {
		for msg := range shellCh {
			if se, ok := msg.(shellExitMsg); ok {
				select {
				case exitCh <- se:
				default:
				}
				return
			}
		}
	}()

	// Ctrl+C cancels the subprocess.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)
	if m2.shellRunning {
		t.Fatal("shell flag still set after Ctrl+C")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after Ctrl+C", m2.state)
	}

	// The child process must terminate promptly (context cancellation).
	select {
	case <-exitCh:
		// child terminated
	case <-time.After(5 * time.Second):
		t.Fatal("subprocess did not terminate after Ctrl+C — tmux kill-pane would be required")
	}

	// Izen is still alive and usable.
	if m2.activeOp != nil || m2.isWorkflowBusy() {
		t.Fatal("runtime busy after subprocess cancellation")
	}
}

// ── E. Double Ctrl+C force-exits with status 130 ──────────────────────────

// TestRegressionDoubleCtrlCHardExit130 asserts the first Ctrl+C performs a
// graceful cancellation and a second Ctrl+C while cancellation is in progress
// forces termination with status 130.
func TestRegressionDoubleCtrlCHardExit130(t *testing.T) {
	saved := hardExitFn
	defer func() { hardExitFn = saved }()

	type hardExit struct{ code int }
	exitStatus := -1
	hardExitFn = func(code int) {
		exitStatus = code
		panic(hardExit{code})
	}

	m := newTestModel()
	m.beginOperation(OpHotfix)

	// First Ctrl+C: graceful cancellation + grace window armed.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)
	if m2.cancelGraceDeadline.IsZero() {
		t.Fatal("grace window not armed after first Ctrl+C")
	}

	// Second Ctrl+C: hard exit 130. Call handleCtrlC directly — Update's
	// global panic recovery would otherwise swallow the forced exit.
	func() {
		defer func() {
			r := recover()
			if _, ok := r.(hardExit); !ok {
				t.Fatalf("second Ctrl+C did not force a hard exit (recover=%v)", r)
			}
		}()
		m2.handleCtrlC()
		t.Fatal("second Ctrl+C returned without hard exit")
	}()
	if exitStatus != 130 {
		t.Fatalf("exit status = %d, want 130", exitStatus)
	}
}

// ── Watchdog / stuck detection ────────────────────────────────────────────

// TestOperationWatchdogReportsStall asserts the watchdog emits a diagnostic
// warning for a stalled operation without ever killing it.
func TestOperationWatchdogReportsStall(t *testing.T) {
	m := newTestModel()
	m.beginOperation(OpHotfix)
	m.activeOp.LastProgress = time.Now().Add(-opWatchdogStuckAfter - time.Second)

	cmd := m.handleWatchdog(watchdogMsg{now: time.Now()})

	// The watchdog re-arms while the operation is active (it never kills).
	if cmd == nil {
		t.Fatal("watchdog stopped while an operation is active")
	}
	if !strings.Contains(recordsText(m), "watchdog") {
		t.Fatalf("watchdog did not report the stall:\n%s", recordsText(m))
	}
	// The operation survives — the watchdog only reports.
	if m.activeOp == nil {
		t.Fatal("watchdog killed the operation")
	}

	// An idle runtime stops the watchdog loop.
	m.finalizeOperation(OpOutcomeSuccess, nil)
	if m.opWatchdogCmd() != nil {
		t.Fatal("watchdog still armed when idle")
	}
}

// ── Operation lifecycle primitives ────────────────────────────────────────

// TestOperationLifecycleTransitions asserts the canonical state machine
// IDLE → DISPATCHED → RUNNING → terminal → FINALIZING → IDLE and single
// ownership.
func TestOperationLifecycleTransitions(t *testing.T) {
	m := newTestModel()
	if m.activeOp != nil {
		t.Fatal("model starts with an active operation")
	}

	op := m.beginOperation(OpHotfix)
	if op == nil || op.ID == "" {
		t.Fatal("beginOperation returned a malformed operation")
	}
	if op.State != OpStateDispatched {
		t.Fatalf("initial state = %s, want dispatched", op.State)
	}
	if m.activeOp != op {
		t.Fatal("beginOperation did not register the operation")
	}
	op.State = OpStateRunning

	// finalizeOperation is the single terminal cleanup path.
	m.finalizeOperation(OpOutcomeAmbiguous, nil)
	if op.State != OpStateTerminal {
		t.Fatalf("finalize did not reach terminal: %s", op.State)
	}
	if op.Outcome != OpOutcomeAmbiguous {
		t.Fatalf("outcome = %s, want ambiguous", op.Outcome)
	}
	if m.activeOp != nil {
		t.Fatal("finalize did not release active ownership")
	}
	if m.isWorkflowBusy() {
		t.Fatal("finalize did not clear busy flags")
	}
	if m.shimmerActive {
		t.Fatal("finalize did not stop the spinner")
	}

	// A new operation supersedes the previous one.
	op2 := m.beginOperation(OpHotfix)
	if m.activeOp != op2 {
		t.Fatal("new operation did not become the active one")
	}
	m.finalizeOperation(OpOutcomeSuccess, nil)
}

// TestOperationFinalizeCancelsContext proves finalizeOperation releases the
// operation cancellation resources (Section 8: operation cancellation
// resources).
func TestOperationFinalizeCancelsContext(t *testing.T) {
	m := newTestModel()
	op := m.beginOperation(OpHotfix)
	ctx := op.Ctx
	select {
	case <-ctx.Done():
		t.Fatal("context already cancelled at begin")
	default:
	}
	m.finalizeOperation(OpOutcomeCancelled, nil)
	select {
	case <-ctx.Done():
		// cancellation propagated
	case <-time.After(time.Second):
		t.Fatal("finalize did not cancel the operation context")
	}
}

// TestRegressionOSSignalCancelsOperation proves the root OS-signal bridge
// (SIGINT/SIGTERM delivered outside the TTY raw-mode key path) routes through
// the same graceful cancellation protocol as Ctrl+C.
func TestRegressionOSSignalCancelsOperation(t *testing.T) {
	m := newTestModel()
	m.beginOperation(OpHotfix)

	res, _ := m.Update(interruptSignalMsg{signal: os.Interrupt})
	m2 := res.(*model)

	if m2.activeOp != nil {
		t.Fatalf("active operation not cancelled by OS signal: %+v", m2.activeOp)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy after OS signal cancellation")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after OS signal", m2.state)
	}
}

// TestRegressionBubbleTeaInterruptMsgCancelsOperation proves Bubble Tea's
// built-in InterruptMsg (its own SIGINT forwarding for non-TTY input) also
// routes through the graceful cancellation protocol.
func TestRegressionBubbleTeaInterruptMsgCancelsOperation(t *testing.T) {
	m := newTestModel()
	m.beginOperation(OpHotfix)

	res, _ := m.Update(tea.InterruptMsg{})
	m2 := res.(*model)

	if m2.activeOp != nil {
		t.Fatalf("active operation not cancelled by InterruptMsg: %+v", m2.activeOp)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy after InterruptMsg cancellation")
	}
}

// TestRegressionCtrlCCancelsIdleChatKeepsInput asserts the unified Ctrl+C
// protocol does not hijack the idle-chat behavior: with no operation active,
// Ctrl+C clears the input buffer instead of cancelling or exiting.
func TestRegressionCtrlCCancelsIdleChatKeepsInput(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.ti.SetValue("stale text")

	// The unified protocol declines to handle an idle Ctrl+C (handled=false);
	// the chat handler then clears the input. handleKey is invoked directly so
	// the uninitialized-test-model init guard does not intercept the key.
	if handled, _ := m.handleCtrlC(); handled {
		t.Fatal("idle Ctrl+C must not be treated as a cancellation")
	}
	res, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)

	if m2.ti.Value() != "" {
		t.Fatalf("idle Ctrl+C did not clear the input buffer: %q", m2.ti.Value())
	}
	_ = cmd
	if m2.state != StateChat {
		t.Fatalf("idle Ctrl+C changed state to %v", m2.state)
	}
	if m2.cancelGraceDeadline != (time.Time{}) {
		t.Fatal("idle Ctrl+C must not arm the force-exit grace window")
	}
}
