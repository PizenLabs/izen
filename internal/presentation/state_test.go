package presentation

import (
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// TestDeriveUIState asserts the pure projection from canonical workflow signals
// onto the presentation UIState: a pending approval always freezes (even over
// an explicit processing signal), an explicit processing signal processes, an
// active execution phase processes, and every resting phase yields chat.
func TestDeriveUIState(t *testing.T) {
	tests := []struct {
		name            string
		phase           string
		approvalPending bool
		isProcessing    bool
		want            UIState
	}{
		{"idle rest", "idle", false, false, StateChat},
		{"ask rest", "ask", false, false, StateChat},
		{"verified terminal", "verified", false, false, StateChat},
		{"failed terminal", "failed", false, false, StateChat},
		{"unknown phase", "", false, false, StateChat},
		{"investigating processes", "investigating", false, false, StateProcessing},
		{"planning processes", "planning", false, false, StateProcessing},
		{"building processes", "building", false, false, StateProcessing},
		{"reviewing processes", "reviewing", false, false, StateProcessing},
		{"repairing processes", "repairing", false, false, StateProcessing},
		{"explicit processing overrides idle phase", "idle", false, true, StateProcessing},
		{"explicit processing overrides ask phase", "ask", false, true, StateProcessing},
		{"explicit processing overrides execution phase", "planning", false, true, StateProcessing},
		{"approval overrides processing", "building", true, true, StateAwaitingApproval},
		{"approval overrides explicit processing", "idle", true, true, StateAwaitingApproval},
		{"approval overrides chat", "idle", true, false, StateAwaitingApproval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveUIState(tt.phase, tt.approvalPending, tt.isProcessing); got != tt.want {
				t.Errorf("DeriveUIState(%q, %v, %v) = %v, want %v",
					tt.phase, tt.approvalPending, tt.isProcessing, got, tt.want)
			}
		})
	}
}

// TestWorkflowViewStateProjectsPhaseChange asserts the projection derives
// StateProcessing from an EventPhaseChanged event alone — no independent UI
// flags involved.
func TestWorkflowViewStateProjectsPhaseChange(t *testing.T) {
	w := NewWorkflowViewState()

	changed := w.Project(events.NewPhaseChanged("ask", "plan"))
	if !changed {
		t.Fatal("expected Project to report a state change")
	}
	if w.Phase() != "plan" {
		t.Errorf("Phase() = %q, want plan", w.Phase())
	}
	if w.ApprovalPending() {
		t.Error("ApprovalPending() = true, want false")
	}
	if got := w.UIState(); got != StateProcessing {
		t.Errorf("UIState() = %v, want StateProcessing (derived from phase)", got)
	}
}

// TestWorkflowViewStateProjectsApprovalRequest asserts the projection derives
// StateAwaitingApproval from an EventApprovalRequested event — the workflow is
// the single source of truth for proposals awaiting human approval.
func TestWorkflowViewStateProjectsApprovalRequest(t *testing.T) {
	w := NewWorkflowViewState()
	w.Project(events.NewPhaseChanged("idle", "building"))

	changed := w.Project(events.NewApprovalRequested("fix.go", "human-in-the-loop approval", "--- a/fix.go"))
	if !changed {
		t.Fatal("expected Project to report a state change")
	}
	if !w.ApprovalPending() {
		t.Error("ApprovalPending() = false, want true")
	}
	if got := w.UIState(); got != StateAwaitingApproval {
		t.Errorf("UIState() = %v, want StateAwaitingApproval (derived from approval request)", got)
	}
}

// TestWorkflowViewStateResolveApproval asserts resolving the approval gate
// re-derives from the workflow phase (back to processing while an execution
// phase is active).
func TestWorkflowViewStateResolveApproval(t *testing.T) {
	w := NewWorkflowViewState()
	w.Project(events.NewPhaseChanged("idle", "building"))
	w.Project(events.NewApprovalRequested("fix.go", "approval", ""))

	if got := w.UIState(); got != StateAwaitingApproval {
		t.Fatalf("UIState() = %v, want StateAwaitingApproval", got)
	}

	w.ResolveApproval()
	if w.ApprovalPending() {
		t.Error("ApprovalPending() = true after ResolveApproval")
	}
	if got := w.UIState(); got != StateProcessing {
		t.Errorf("UIState() after resolve = %v, want StateProcessing (phase still building)", got)
	}
}

// TestWorkflowViewStateIgnoresUnrelatedEvents asserts unrelated domain events
// never disturb the derived projection.
func TestWorkflowViewStateIgnoresUnrelatedEvents(t *testing.T) {
	w := NewWorkflowViewState()
	if w.Project(events.NewPatchApplied("x.go", 1, 1, 0)) {
		t.Error("unrelated event reported a state change")
	}
	if w.Project(events.NewActivity("noise")) {
		t.Error("activity event reported a state change")
	}
	if w.Project(nil) {
		t.Error("nil event reported a state change")
	}
	if got := w.UIState(); got != StateChat {
		t.Errorf("UIState() = %v, want StateChat", got)
	}
}

// TestUIStateString asserts the presentation state names.
func TestUIStateString(t *testing.T) {
	tests := []struct {
		s    UIState
		want string
	}{
		{StateChat, "chat"},
		{StateAwaitingApproval, "awaiting-approval"},
		{StateProcessing, "processing"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("%v.String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}
