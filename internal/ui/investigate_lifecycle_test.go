package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/modes"
)

// newInvestigateBusyModel builds a model mid-investigation: the async
// agentStartMsg has been processed (agentRunning + investigateRunning set) and
// a PhaseChanged domain event has been projected onto the derived UI state —
// exactly the sequence that previously left the viewport stuck in
// StateProcessing even after the terminal investigateResultMsg was delivered,
// leaving an orphaned spinner.
func newInvestigateBusyModel(t *testing.T) *model {
	t.Helper()
	m := newTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	m.resolveApprovalState()
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.state = StateChat
	m.streaming = false
	m.agentRunning = false
	m.reviewRunning = false
	m.investigateRunning = false
	m.pipelineRunning = false
	m.shellRunning = false

	// agentStartMsg processed.
	m.agentRunning = true
	m.investigateRunning = true

	// PhaseChanged → investigate arrives while the run is busy: the derived
	// state freezes into StateProcessing (the stuck-spinner precondition).
	res, _ := m.Update(domainEventMsg{ev: events.NewPhaseChanged("idle", "investigating")})
	m2 := res.(*model)
	if m2.state != StateProcessing {
		t.Fatalf("precondition: state=%v, want StateProcessing (stuck spinner setup)", m2.state)
	}
	return m2
}

// TestInvestigateResultMsgErrorReleasesStuckProcessing asserts the terminal
// investigateResultMsg carrying an engine/model error (a simulated failed
// /investigate run) releases the stuck StateProcessing and halts the spinner:
// every transient flag is cleared, the derived state returns to interactive
// chat, and the next smooth-stream tick no longer re-dispatches itself.
func TestInvestigateResultMsgErrorReleasesStuckProcessing(t *testing.T) {
	m := newInvestigateBusyModel(t)

	res, _ := m.Update(investigateResultMsg{err: errors.New("model error: provider unreachable")})
	m2 := res.(*model)

	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after investigateResultMsg error (spinner must not persist)", m2.state)
	}
	if m2.investigateRunning || m2.agentRunning || m2.reviewRunning || m2.streaming || m2.pipelineRunning {
		t.Errorf("processing flags still set after investigate error: investigate=%v agent=%v review=%v stream=%v pipeline=%v",
			m2.investigateRunning, m2.agentRunning, m2.reviewRunning, m2.streaming, m2.pipelineRunning)
	}
	if m2.spinnerFrame != 0 {
		t.Errorf("spinnerFrame = %d, want 0 after investigate error", m2.spinnerFrame)
	}
	// The unified tick loop must now halt: no active work, no re-dispatched cmd.
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick still re-dispatches a cmd after investigate error — spinner loop never stops")
	}
}

// TestInvestigateResultMsgSuccessReleasesStuckProcessing asserts the success
// branch of investigateResultMsg performs the same state release (a healthy
// completion must never leave the spinner up either).
func TestInvestigateResultMsgSuccessReleasesStuckProcessing(t *testing.T) {
	m := newInvestigateBusyModel(t)

	res, _ := m.Update(investigateResultMsg{
		records:    []record{{role: roleAI, text: "Problem: sample"}},
		sessionKey: "sample",
	})
	m2 := res.(*model)

	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after investigateResultMsg success", m2.state)
	}
	if m2.investigateRunning || m2.agentRunning || m2.reviewRunning {
		t.Errorf("processing flags still set after investigate success: investigate=%v agent=%v review=%v",
			m2.investigateRunning, m2.agentRunning, m2.reviewRunning)
	}
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick loop still alive after investigate success — spinner never stops")
	}
}

// TestInvestigateEscCancelsActivePipelineContext asserts Esc during an active
// /investigate (investigateRunning set, view frozen in StateProcessing) routes
// through the emergency interrupt: it cancels the registered background
// context from the central Emergency Interrupt Registry, clears
// investigateRunning via syncUIState, restores input focus and dispatches a
// CancelCmd — without killing the app.
func TestInvestigateEscCancelsActivePipelineContext(t *testing.T) {
	m := newInvestigateBusyModel(t)

	// Simulate the already-registered investigate watchdog so the interrupt
	// can actually cancel the in-flight pipeline context.
	cancelled := false
	m.backgroundCancels = append(m.backgroundCancels, func() { cancelled = true })

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := res.(*model)

	if !cancelled {
		t.Error("Esc did not cancel the registered investigate pipeline context")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after Esc cancel", m2.state)
	}
	if m2.investigateRunning || m2.agentRunning || m2.streaming || m2.reviewRunning {
		t.Errorf("processing flags still set after Esc cancel: investigate=%v agent=%v stream=%v review=%v",
			m2.investigateRunning, m2.agentRunning, m2.streaming, m2.reviewRunning)
	}
	if !m2.ti.Focused() {
		t.Error("input focus not restored after Esc cancel of /investigate")
	}
	if cmd == nil {
		t.Fatal("Esc must return a command (CancelCmd) so the runtime observes the cancellation")
	}
	// The tick loop must not be re-spun by the cancelled state.
	_, cmd2 := m2.Update(smoothStreamTickMsg{})
	if cmd2 != nil {
		t.Fatal("tick loop still alive after Esc cancellation — spinner never stops")
	}
}

// TestInvestigateRunningSetSynchronously asserts runInvestigateCmd flips
// investigateRunning synchronously on the event-loop thread so the spinner
// renders immediately and Esc/Ctrl+C can interrupt before the async engine
// even starts (mirrors runReviewCmd).
func TestInvestigateRunningSetSynchronously(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	m.resolveApprovalState()
	m.investigateRunning = false
	m.agentRunning = false

	cmd := m.runInvestigateCmd("why is the build failing")
	if cmd == nil {
		t.Fatal("runInvestigateCmd returned nil cmd")
	}
	if !m.investigateRunning {
		t.Error("investigateRunning = false after runInvestigateCmd returned; must be set synchronously")
	}
	if m.lastActionTime.IsZero() {
		t.Error("lastActionTime not stamped by runInvestigateCmd")
	}
}

// TestInvestigateAsyncCmdGuaranteesTerminalMessageOnCapabilityGate drives the
// real async investigate closure through an EARLY error exit (the current
// mode violates the read-only capability contract) and asserts the terminal
// investigateResultMsg is still dispatched — an error log must never return
// early without emitting it.
func TestInvestigateAsyncCmdGuaranteesTerminalMessageOnCapabilityGate(t *testing.T) {
	m := newTestModel() // resolver defaults to ModeBuild (CanWrite=true)
	cmd := m.runInvestigateAsyncCmd("debug the failing test")
	if cmd == nil {
		t.Fatal("runInvestigateAsyncCmd returned nil cmd")
	}
	msg := cmd()
	im, ok := msg.(investigateResultMsg)
	if !ok {
		t.Fatalf("expected terminal investigateResultMsg, got %T", msg)
	}
	if im.err == nil {
		t.Fatal("expected capability-denial error to be surfaced in the terminal message")
	}
	if !strings.Contains(im.err.Error(), "capability") {
		t.Errorf("error does not mention the capability violation: %v", im.err)
	}
}

// TestInvestigateAsyncCmdDispatchesTerminalMessageOnSuccess drives the real
// async investigate closure through a deterministic engine run (feature intent
// short-circuit, no go.mod → instant) and asserts the terminal message is
// dispatched with a nil error on normal completion.
func TestInvestigateAsyncCmdDispatchesTerminalMessageOnSuccess(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	m.resolveApprovalState()

	cmd := m.runInvestigateAsyncCmd("add a new feature to the app")
	if cmd == nil {
		t.Fatal("runInvestigateAsyncCmd returned nil cmd")
	}
	msg := cmd()
	im, ok := msg.(investigateResultMsg)
	if !ok {
		t.Fatalf("expected terminal investigateResultMsg, got %T", msg)
	}
	if im.err != nil {
		t.Fatalf("expected nil error on short-circuit success, got: %v", im.err)
	}
}

// panicProvider is a provider whose Execute always panics, simulating a
// catastrophic engine failure mid-run. It proves the inner-goroutine panic
// guard converts the crash into an error outcome instead of freezing the
// spinner for the full 60s deadline.
type panicProvider struct{}

func (panicProvider) Name() string { return "panic" }

func (panicProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	panic("simulated provider failure")
}

func (panicProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	panic("simulated provider failure")
}

// TestInvestigateAsyncCmdEnginePanicDeliversErrorResult drives the real async
// investigate closure into an engine panic (a provider that panics during the
// dispatch classifier) and asserts the panic is converted into an
// error-carrying investigateResultMsg — the spinner can never be orphaned.
func TestInvestigateAsyncCmdEnginePanicDeliversErrorResult(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	m.provider = panicProvider{}
	m.resolveApprovalState()

	// Bug/regression intent → full forensic state machine → the dispatcher
	// hits the panicking provider.
	cmd := m.runInvestigateAsyncCmd("cmd/api/main.go:7: undefined: Router — why is this build failing")
	if cmd == nil {
		t.Fatal("runInvestigateAsyncCmd returned nil cmd")
	}
	msg := cmd()
	im, ok := msg.(investigateResultMsg)
	if !ok {
		t.Fatalf("expected terminal investigateResultMsg, got %T", msg)
	}
	if im.err == nil {
		t.Fatal("expected the engine panic to be surfaced as an error result")
	}
	if !strings.Contains(im.err.Error(), "panic") {
		t.Errorf("error does not report the panic: %v", im.err)
	}
}
