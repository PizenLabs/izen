package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

// TestEnterApprovalStateDerivesFromWorkflowGate asserts the approval
// presentation state is derived from the canonical WorkflowStateMachine
// pending-approval signal (the single source of truth), not an independent UI
// flag.
func TestEnterApprovalStateDerivesFromWorkflowGate(t *testing.T) {
	m := newTestModel()
	m.state = StateChat

	m.enterApprovalState()

	if !m.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = false after enterApprovalState")
	}
	if m.state != StateAwaitingApproval {
		t.Errorf("state = %v, want StateAwaitingApproval (derived)", m.state)
	}
}

// TestResolveApprovalStateClearsWorkflowGate asserts approve/reject/cancel all
// funnel through the canonical gate and re-derive the presentation state.
func TestResolveApprovalStateClearsWorkflowGate(t *testing.T) {
	m := newTestModel()
	m.enterApprovalState()
	if m.state != StateAwaitingApproval {
		t.Fatalf("precondition: state = %v, want StateAwaitingApproval", m.state)
	}

	m.resolveApprovalState()

	if m.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = true after resolveApprovalState")
	}
	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat (idle, no in-flight operation)", m.state)
	}
}

// TestPhaseChangedEventDerivesProcessing asserts a workflow phase-change event
// projects onto StateProcessing while an operation is in flight and falls back
// to StateChat when resting.
func TestPhaseChangedEventDerivesProcessing(t *testing.T) {
	m := newTestModel()
	m.streaming = true
	m.agentRunning = true

	m.handleDomainEvent(events.NewPhaseChanged("ask", "plan"))

	if m.state != StateProcessing {
		t.Errorf("state = %v, want StateProcessing while in-flight", m.state)
	}

	// Operation completes: resting in a mode phase must not gate the input.
	m.streaming = false
	m.agentRunning = false
	m.syncUIState()
	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat when no operation is in flight", m.state)
	}
}

// TestApprovalRequestedEventDerivesState asserts a Tier 4 EventApprovalRequested
// on the shared bus drives the workflow pending-approval gate and the derived
// AwaitingApproval presentation state.
func TestApprovalRequestedEventDerivesState(t *testing.T) {
	m := newTestModel()
	m.state = StateChat

	m.handleDomainEvent(events.NewApprovalRequested("fix.go", "approval required", ""))

	if !m.workflowSM.PendingApproval() {
		t.Error("workflowSM.PendingApproval() = false after approval-requested event")
	}
	if m.state != StateAwaitingApproval {
		t.Errorf("state = %v, want StateAwaitingApproval (derived from event)", m.state)
	}
}

// TestWorkflowSMApprovalGateTransitions guards the canonical gate lifecycle:
// pending -> resolved -> pending round-trips and stays consistent with the
// presentation derivation. The SM is owned by the UI goroutine (like the rest
// of the WorkflowStateMachine), so access is single-threaded here.
func TestWorkflowSMApprovalGateTransitions(t *testing.T) {
	sm := workflow.NewWorkflowStateMachine()
	if sm.PendingApproval() {
		t.Fatal("fresh state machine should not be awaiting approval")
	}
	for i := 0; i < 100; i++ {
		sm.MarkApprovalPending()
		if !sm.PendingApproval() {
			t.Fatalf("iteration %d: pending flag not set", i)
		}
		if got := presentation.DeriveUIState(sm.State().String(), sm.PendingApproval(), false); got != StateAwaitingApproval {
			t.Fatalf("iteration %d: derived state = %v, want StateAwaitingApproval", i, got)
		}
		sm.MarkApprovalResolved()
		if sm.PendingApproval() {
			t.Fatalf("iteration %d: pending flag not cleared", i)
		}
	}
}
