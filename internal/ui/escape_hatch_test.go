package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/presentation"
)

// newProcessingModel builds a model frozen in StateProcessing with every
// transient processing flag set, simulating the reported deadlock where the
// spinner froze at "Processing file mutations... Please wait." and the keyboard
// became unresponsive.
func newProcessingModel() *model {
	m := newTestModel()
	m.state = StateProcessing
	m.streaming = true
	m.agentRunning = true
	m.planPending = true
	m.pipelineRunning = true
	m.reviewRunning = true
	m.spinnerFrame = 3
	return m
}

// TestEmergencyEscapeHatchCtrlCUnfreezesStateProcessing asserts Ctrl+C is
// processed at the very top of the Update loop and immediately returns a frozen
// StateProcessing viewport to interactive StateChat, clearing every transient
// processing flag so the spinner can never stay up.
func TestEmergencyEscapeHatchCtrlCUnfreezesStateProcessing(t *testing.T) {
	m := newProcessingModel()

	resModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := resModel.(*model)

	if m2.state != StateChat {
		t.Errorf("state = %v, want StateChat after Ctrl+C", m2.state)
	}
	if m2.streaming || m2.agentRunning || m2.planPending || m2.pipelineRunning || m2.reviewRunning {
		t.Errorf("processing flags still set after Ctrl+C: streaming=%v agent=%v planPending=%v pipeline=%v review=%v",
			m2.streaming, m2.agentRunning, m2.planPending, m2.pipelineRunning, m2.reviewRunning)
	}
	if m2.spinnerFrame != 0 {
		t.Errorf("spinnerFrame = %d, want 0 after Ctrl+C", m2.spinnerFrame)
	}
	if cmd == nil {
		t.Fatal("Ctrl+C must return a command (interrupt record)")
	}
}

// TestEmergencyEscapeHatchEscUnfreezesStateProcessing asserts Esc is an
// unblockable escape hatch while frozen in StateProcessing.
func TestEmergencyEscapeHatchEscUnfreezesStateProcessing(t *testing.T) {
	m := newProcessingModel()

	resModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := resModel.(*model)

	if m2.state != StateChat {
		t.Errorf("state = %v, want StateChat after Esc", m2.state)
	}
	if m2.agentRunning || m2.streaming || m2.planPending {
		t.Errorf("processing flags still set after Esc")
	}
	if cmd == nil {
		t.Fatal("Esc must return a command (interrupt record)")
	}
}

// TestEmergencyEscapeHatchCtrlDUnfreezesStateProcessing asserts Ctrl+D is an
// unblockable escape hatch while frozen in StateProcessing (it must NOT trigger
// a clean shutdown — it aborts back to chat).
func TestEmergencyEscapeHatchCtrlDUnfreezesStateProcessing(t *testing.T) {
	m := newProcessingModel()

	resModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m2 := resModel.(*model)

	if m2.state != StateChat {
		t.Errorf("state = %v, want StateChat after Ctrl+D", m2.state)
	}
	if m2.agentRunning || m2.planPending {
		t.Errorf("processing flags still set after Ctrl+D")
	}
	if cmd == nil {
		t.Fatal("Ctrl+D must return a command (interrupt record)")
	}
}

// TestEmergencyEscapeHatchClearsApprovalGate asserts the emergency hatch also
// releases an outstanding canonical approval gate and drops in-flight approval
// state so the user is never trapped.
func TestEmergencyEscapeHatchClearsApprovalGate(t *testing.T) {
	m := newTestModel()
	m.enterApprovalState()
	if m.state != StateAwaitingApproval {
		t.Fatalf("precondition: state = %v, want StateAwaitingApproval", m.state)
	}

	resModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := resModel.(*model)

	if m2.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = true after emergency interrupt")
	}
	if m2.state != StateChat {
		t.Errorf("state = %v, want StateChat after emergency interrupt", m2.state)
	}
	if m2.awaitingConfirmation {
		t.Error("awaitingConfirmation = true after emergency interrupt")
	}
	if len(m2.pendingProposals) != 0 {
		t.Errorf("pendingProposals = %d, want 0 after emergency interrupt", len(m2.pendingProposals))
	}
}

// TestEscInAwaitingApprovalPreservesRejectSemantics asserts Esc is NOT hijacked
// by the emergency hatch in StateAwaitingApproval: it keeps its normal role of
// rejecting the pending proposal ("changes rejected"), not a hard interrupt.
func TestEscInAwaitingApprovalPreservesRejectSemantics(t *testing.T) {
	m := newTestModel() // StateAwaitingApproval with one pendingProposal
	m.execEng = nil

	resModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := resModel.(*model)

	// The approval gate must be resolved through the normal reject path.
	if m2.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = true after Esc reject")
	}
	if m2.state != StateChat {
		t.Errorf("state = %v, want StateChat after Esc reject", m2.state)
	}
	rejected := false
	for _, r := range m2.records {
		if strings.Contains(r.text, "changes rejected") {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Error("Esc during approval must reject the proposal via handleKey, not emergency-interrupt")
	}
}

// TestPlanResultMsgReleasesStaleProcessingState asserts a staged plan result
// (e.g. Microkernel) re-derives the presentation state away from a stale
// StateProcessing, so the spinner stops and Alt+P / Alt+R respond immediately.
func TestPlanResultMsgReleasesStaleProcessingState(t *testing.T) {
	m := newTestModel()
	m.microkernel = plan.NewMicrokernelPlanner(t.TempDir())
	m.state = StateProcessing
	m.streaming = true
	m.agentRunning = true
	m.planPending = true

	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "CREATE index.html"},
	}
	m.Update(planResultMsg{Tasks: tasks, Microkernel: true})

	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat after planResultMsg (chip activation requires StateChat)", m.state)
	}
	if m.streaming || m.agentRunning || m.planPending {
		t.Errorf("processing flags still set after planResultMsg: streaming=%v agent=%v planPending=%v",
			m.streaming, m.agentRunning, m.planPending)
	}
	if m.uiNotice == "" || !strings.Contains(m.uiNotice, "Microkernel") {
		t.Errorf("uiNotice = %q, want microkernel staging announcement", m.uiNotice)
	}
}

// TestSyncUIStateApprovalOverridesProcessing asserts the fallback safeguard:
// when an approval gate is pending, a stuck processing signal must NOT win — the
// presentation derives StateAwaitingApproval and the user keeps control.
func TestSyncUIStateApprovalOverridesProcessing(t *testing.T) {
	m := newTestModel()
	m.streaming = true
	m.agentRunning = true
	m.planPending = true

	m.enterApprovalState()

	if m.state != StateAwaitingApproval {
		t.Errorf("state = %v, want StateAwaitingApproval (approval overrides processing)", m.state)
	}
	if got := presentation.DeriveUIState("planning", true, true); got != StateAwaitingApproval {
		t.Errorf("DeriveUIState(planning, approval, processing) = %v, want StateAwaitingApproval", got)
	}
}
