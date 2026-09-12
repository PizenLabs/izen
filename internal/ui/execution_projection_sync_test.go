package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

// TestSyncExecutionProjection_IdleResets Visibility and View verifies
// deterministic projection sync: when the canonical WorkflowStateMachine
// reaches StateIdle (or presentation StateChat), stale step trees and
// progress projections are cleared immediately.
func TestSyncExecutionProjection_IdleResetsVisibilityAndView(t *testing.T) {
	m := newTestModel()
	// Put workflow in a non-idle state initially.
	_ = m.workflowSM.SendEvent(workflow.EventInvestigate, workflow.TransitionContext{})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("req-idle-test")
	m.handleDomainEvent(events.NewExecutionStarted("req-idle-test", "build", "prompt", ""))
	m.handleDomainEvent(events.NewTargetResolved("req-idle-test", "index.html", true, "strategy"))
	m.execVisibility = presentation.VisibilityExpanded
	m.executionResolving = true

	if !m.execView.Active() {
		t.Fatal("precondition: execView should be active before idle reset")
	}
	if m.execVisibility != presentation.VisibilityExpanded {
		t.Fatalf("precondition: execVisibility = %v, want Expanded", m.execVisibility)
	}

	// Transition workflow to idle via EventUserInterrupt (swallow error if already idle).
	_ = m.workflowSM.SendEvent(workflow.EventUserInterrupt, workflow.TransitionContext{})
	// Direct unwind fallback if transition rejected (e.g., already idle).
	if m.workflowSM.State() != workflow.StateIdle {
		_ = m.workflowSM.SendEvent(workflow.EventReset, workflow.TransitionContext{})
	}
	m.syncUIState()

	if m.execVisibility != presentation.VisibilityNormal {
		t.Errorf("execVisibility = %v, want VisibilityNormal after StateIdle", m.execVisibility)
	}
	if m.execView != nil && m.execView.Active() {
		t.Errorf("execView still active after StateIdle: phase=%v step=%q", m.execView.State().Phase, m.execView.State().Step)
	}
	if m.executionResolving {
		t.Errorf("executionResolving = true, want false after StateIdle")
	}
}

// TestSyncExecutionProjection_EventUserInterruptClearsGhostTree verifies
// that emitting EventUserInterrupt (emergency interrupt) forces
// execVisibility to Normal and resets execView, preventing ghost step trees.
func TestSyncExecutionProjection_EventUserInterruptClearsGhostTree(t *testing.T) {
	m := newTestModel()
	_ = m.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("req-interrupt-test")
	m.handleDomainEvent(events.NewExecutionStarted("req-interrupt-test", "build", "prompt", ""))
	m.handleDomainEvent(events.NewTargetResolved("req-interrupt-test", "main.go", true, "strategy"))
	m.execVisibility = presentation.VisibilityDebug
	m.executionResolving = true

	// Simulate emergency interrupt via workflow interrupt event.
	if err := m.workflowSM.SendEvent(workflow.EventUserInterrupt, workflow.TransitionContext{}); err != nil {
		t.Fatalf("EventUserInterrupt rejected: %v", err)
	}
	m.syncUIState()

	if m.execVisibility != presentation.VisibilityNormal {
		t.Errorf("execVisibility = %v, want VisibilityNormal after EventUserInterrupt", m.execVisibility)
	}
	if m.execView != nil && m.execView.Active() {
		t.Errorf("execView still active after EventUserInterrupt: phase=%v", m.execView.State().Phase)
	}
	if m.executionResolving {
		t.Errorf("executionResolving = true, want false after EventUserInterrupt")
	}
}

// TestSyncExecutionProjection_ExecutionFinishedRetainsTerminal verifies
// that a terminal ExecutionFinishedPayload is not considered stale: the
// projection stays terminal (completed) after idle sync, but the resolving
// marker is cleared so no ghost spinner remains. A stale RUNNING tree is
// cleared; a terminal result survives until the next Begin.
func TestSyncExecutionProjection_ExecutionFinishedRetainsTerminal(t *testing.T) {
	m := newTestModel()
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("req-finished-test")
	m.execVisibility = presentation.VisibilityExpanded
	m.executionResolving = true
	m.handleDomainEvent(events.NewExecutionStarted("req-finished-test", "build", "prompt", ""))
	m.handleDomainEvent(events.NewTargetResolved("req-finished-test", "index.html", true, "strategy"))

	// Terminal finished event.
	m.handleDomainEvent(events.NewExecutionFinished("req-finished-test", true, "completed"))
	if m.execView.State().Phase != presentation.PhaseCompleted {
		t.Fatalf("precondition: phase = %v, want completed after finished", m.execView.State().Phase)
	}
	// Workflow still not idle; projection is terminal but not yet cleared.
	// Now unwind to idle (simulates post-command cleanup).
	_ = m.workflowSM.SendEvent(workflow.EventUserInterrupt, workflow.TransitionContext{})
	if m.workflowSM.State() != workflow.StateIdle {
		_ = m.workflowSM.SendEvent(workflow.EventReset, workflow.TransitionContext{})
	}
	m.syncUIState()

	// Terminal result survives idle with its visibility layer intact — only
	// the in-flight spinner is cleared. This preserves the completed
	// narrative for inspection until the next Begin.
	if m.execVisibility != presentation.VisibilityExpanded {
		t.Errorf("execVisibility = %v, want VisibilityExpanded (terminal visibility survives idle)", m.execVisibility)
	}
	if m.execView.State().Phase != presentation.PhaseCompleted {
		t.Errorf("execView phase = %v, want completed (terminal survives idle)", m.execView.State().Phase)
	}
	if m.executionResolving {
		t.Errorf("executionResolving = true, want false after terminal+idle (spinner cleared)")
	}
}
