package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// newHotfixBusyModel builds a model mid-$hot apply: the agentStartMsg has been
// processed (agentRunning set), hotfixActive is armed, and a PhaseChanged
// domain event has been projected onto the derived UI state — the exact
// sequence that previously froze the view in StateProcessing with the
// "Applying hotfix..." / "Processing file mutations... Please wait." spinner
// running forever even after a terminal message should have arrived.
func newHotfixBusyModel(t *testing.T) *model {
	t.Helper()
	m := newTestModel()
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
	m.hotfixActive = true

	m.agentRunning = true
	m.agentLabel = "hotfix"

	res, _ := m.Update(domainEventMsg{ev: events.NewPhaseChanged("idle", "building")})
	m2 := res.(*model)
	if m2.state != StateProcessing {
		t.Fatalf("precondition: state=%v, want StateProcessing (stuck spinner setup)", m2.state)
	}
	return m2
}

// TestHotfixApplyNilEngineGuaranteesTerminalMessage drives the real
// applyHotfixPatch closure (the "Applying hotfix..." mutation step) with a
// model that has no execution engine and asserts a terminal buildResultMsg is
// ALWAYS dispatched — the TUI can never hang on a silently-missing engine.
func TestHotfixApplyNilEngineGuaranteesTerminalMessage(t *testing.T) {
	m := newTestModel()
	// A nil workflow SM makes transitionToBuilding a no-op so the closure
	// deterministically reaches the execution-engine guard.
	m.workflowSM = nil
	m.execEng = nil

	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "file.txt"}
	msg := m.applyHotfixPatch(task, &execution.Patch{})()
	im, ok := msg.(buildResultMsg)
	if !ok {
		t.Fatalf("expected terminal buildResultMsg, got %T", msg)
	}
	if im.err == nil {
		t.Fatal("expected an error for a missing execution engine")
	}
	if !strings.Contains(im.err.Error(), "engine not configured") {
		t.Errorf("unexpected error: %v", im.err)
	}
}

// TestHotfixApplyPanicGuaranteesTerminalMessage drives applyHotfixPatch into
// a panic (a nil task dereferenced in the authorization argument) and asserts
// the panic is converted into an error-carrying buildResultMsg — the
// "Applying hotfix..." spinner can never be orphaned by a crash mid-apply.
// The resulting terminal message is then fed through Update to prove
// isProcessing is cleared and the spinner loop halts.
func TestHotfixApplyPanicGuaranteesTerminalMessage(t *testing.T) {
	m := newHotfixBusyModel(t)
	// A nil workflow SM makes transitionToBuilding a no-op, so the closure
	// reaches the nil-task dereference and panics deterministically.
	m.workflowSM = nil
	m.execEng = nil

	// nil task → task.Target panics inside the apply closure.
	msg := m.applyHotfixPatch(nil, &execution.Patch{})()
	im, ok := msg.(buildResultMsg)
	if !ok {
		t.Fatalf("expected terminal buildResultMsg after apply panic, got %T", msg)
	}
	if im.err == nil {
		t.Fatal("expected the apply panic to be surfaced as a terminal error")
	}
	if !strings.Contains(im.err.Error(), "panic") {
		t.Errorf("error does not report the panic: %v", im.err)
	}

	// Deliver the terminal message: isProcessing must be cleared and the tick
	// spinner loop must halt.
	res, _ := m.Update(im)
	m2 := res.(*model)
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after hotfix apply terminal error", m2.state)
	}
	if m2.agentRunning || m2.hotfixActive || m2.streaming || m2.reviewRunning || m2.pipelineRunning {
		t.Errorf("processing flags still set after hotfix apply terminal error: agent=%v hotfix=%v stream=%v review=%v pipeline=%v",
			m2.agentRunning, m2.hotfixActive, m2.streaming, m2.reviewRunning, m2.pipelineRunning)
	}
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick loop still alive after hotfix apply terminal error — spinner never stops")
	}
}

// TestHotfixProposalErrorReleasesStuckProcessing asserts the "[HOTFIX]
// Pipeline PAUSED" terminal event (an error-carrying hotfixProposalMsg)
// releases a stuck StateProcessing and halts the spinner immediately.
func TestHotfixProposalErrorReleasesStuckProcessing(t *testing.T) {
	m := newHotfixBusyModel(t)

	res, _ := m.Update(hotfixProposalMsg{Err: errors.New("hotfix patch generation failed")})
	m2 := res.(*model)

	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after hotfix pipeline PAUSED (spinner must not persist)", m2.state)
	}
	if m2.agentRunning || m2.hotfixActive || m2.reviewRunning || m2.streaming || m2.pipelineRunning {
		t.Errorf("processing flags still set after hotfix PAUSED: agent=%v hotfix=%v review=%v stream=%v pipeline=%v",
			m2.agentRunning, m2.hotfixActive, m2.reviewRunning, m2.streaming, m2.pipelineRunning)
	}
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick still re-dispatches a cmd after hotfix PAUSED — spinner loop never stops")
	}
}

// TestHotfixProposalSuccessClearsSpinnerEntersApproval asserts a healthy
// hotfixProposalMsg clears every transient flag (spinner halted) before
// freezing in StateAwaitingApproval for the developer's explicit sign-off.
func TestHotfixProposalSuccessClearsSpinnerEntersApproval(t *testing.T) {
	m := newHotfixBusyModel(t)

	res, _ := m.Update(hotfixProposalMsg{
		Task: &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "LICENSE"},
		Patch: &execution.Patch{
			ID:       "hotfix-1",
			File:     "LICENSE",
			Original: "old",
			Modified: "new",
		},
		Diff: "--- a/LICENSE\n+++ b/LICENSE\n@@ -1 +1 @@\n-old\n+new\n",
	})
	m2 := res.(*model)

	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval after hotfix proposal", m2.state)
	}
	if m2.agentRunning || m2.reviewRunning || m2.streaming || m2.pipelineRunning {
		t.Errorf("processing flags still set after hotfix proposal: agent=%v review=%v stream=%v pipeline=%v",
			m2.agentRunning, m2.reviewRunning, m2.streaming, m2.pipelineRunning)
	}
}

// TestHotfixApplyResultReleasesStuckProcessing asserts the hotfix apply
// completion terminal event (a clean buildResultMsg while hotfixActive is set,
// the "+ Applying hotfix..." handshake in keys.go) releases the stuck
// StateProcessing and halts the spinner.
func TestHotfixApplyResultReleasesStuckProcessing(t *testing.T) {
	m := newHotfixBusyModel(t)

	res, _ := m.Update(buildResultMsg{
		output:   "Applied hotfix patch to file.txt",
		exitCode: 0,
	})
	m2 := res.(*model)

	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after hotfix apply completion", m2.state)
	}
	if m2.agentRunning || m2.hotfixActive || m2.reviewRunning || m2.streaming || m2.pipelineRunning {
		t.Errorf("processing flags still set after hotfix apply completion: agent=%v hotfix=%v review=%v stream=%v pipeline=%v",
			m2.agentRunning, m2.hotfixActive, m2.reviewRunning, m2.streaming, m2.pipelineRunning)
	}
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick loop still alive after hotfix apply completion — spinner never stops")
	}
}

// TestHotfixEscDuringApplyAbortsCleanly asserts Esc during the "Applying
// hotfix..." stage routes through the emergency interrupt: it cancels the
// registered background context, clears the transient flags (including
// hotfixActive) and returns the view to interactive chat.
func TestHotfixEscDuringApplyAbortsCleanly(t *testing.T) {
	m := newHotfixBusyModel(t)

	cancelled := false
	m.backgroundCancels = append(m.backgroundCancels, func() { cancelled = true })

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := res.(*model)

	if !cancelled {
		t.Error("Esc did not cancel the registered hotfix apply context")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after Esc during hotfix apply", m2.state)
	}
	if m2.agentRunning || m2.hotfixActive {
		t.Errorf("processing flags still set after Esc: agent=%v hotfix=%v", m2.agentRunning, m2.hotfixActive)
	}
}
