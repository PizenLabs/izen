package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	autonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
)

// testProposalSurface builds a realistic zero-token DecisionSurface for a
// closed-gate barrier (corrupt AST), the payload the event bus delivers.
func testProposalSurface() DecisionSurface {
	return fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        autonomy.ASTCorrupt,
		DependencyStatus: autonomy.DependenciesUnresolved,
		BudgetStatus:     autonomy.BudgetWithinLimits,
	}, "$prompt"))
}

// TestTUIBridge_HandlesPointerAndWrappedEvents pins the event-bus bridge
// contract: a HumanBoundaryProposalMsg may cross the bus as a VALUE, a
// POINTER, or a generic EngineEvent wrapper whose Payload holds either shape.
// The dispatcher MUST activate the interactive proposal modal in every case —
// a dropped or mis-typed proposal event would strand the run at
// awaiting_human with no keyboard-resolvable decision surface.
func TestTUIBridge_HandlesPointerAndWrappedEvents(t *testing.T) {
	cases := []struct {
		name string
		msg  interface{}
	}{
		{
			name: "value HumanBoundaryProposalMsg",
			msg:  HumanBoundaryProposalMsg{DecisionSurface: ptr(testProposalSurface())},
		},
		{
			name: "pointer *HumanBoundaryProposalMsg",
			msg:  &HumanBoundaryProposalMsg{DecisionSurface: ptr(testProposalSurface())},
		},
		{
			name: "EngineEvent wrapping a value payload",
			msg:  EngineEvent{Payload: HumanBoundaryProposalMsg{DecisionSurface: ptr(testProposalSurface())}},
		},
		{
			name: "EngineEvent wrapping a pointer payload",
			msg:  EngineEvent{Payload: &HumanBoundaryProposalMsg{DecisionSurface: ptr(testProposalSurface())}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModel(nil)
			updated, cmd := model.Update(tc.msg.(tea.Msg))
			if updated == nil {
				t.Fatal("Update must return the dispatcher")
			}
			if updated.activeModal != ModalProposal {
				t.Fatalf("activeModal = %v, want ModalProposal — proposal event was dropped by the bridge", updated.activeModal)
			}
			if updated.proposalModel == nil {
				t.Fatal("proposalModel must be instantiated on a proposal event")
			}
			if !updated.ProposalActive() {
				t.Fatal("ProposalActive() must report the interactive modal")
			}
			// The modal must actually render the interactive menu, proving the
			// DecisionSurface payload survived the unwrap.
			if out := updated.View(); out == "" || !containsAll(out, "PROPOSAL STRATEGY", "Esc cancel") {
				t.Fatalf("modal must render the interactive proposal menu, got %q", out)
			}
			// Activating a pure value modal schedules no background command.
			if cmd != nil {
				t.Fatal("activating the proposal modal must schedule no background command")
			}
		})
	}
}

// TestTUIBridge_EngineEventIgnoresForeignPayloads pins the wrapper contract:
// an EngineEvent whose Payload is NOT a proposal message (activity telemetry,
// other engine events) must leave the modal untouched — never a fake surface.
func TestTUIBridge_EngineEventIgnoresForeignPayloads(t *testing.T) {
	model := NewModel(nil)
	updated, cmd := model.Update(EngineEvent{Payload: 42})
	if updated == nil {
		t.Fatal("Update must return the dispatcher")
	}
	if updated.activeModal != ModalNone {
		t.Fatalf("activeModal = %v, want ModalNone for a non-proposal payload", updated.activeModal)
	}
	if updated.ProposalActive() {
		t.Fatal("a non-proposal payload must not activate the modal")
	}
	if cmd != nil {
		t.Fatal("a non-proposal payload must not schedule a command")
	}

	// A wrapped nil pointer payload is equally inert.
	model.Update(EngineEvent{Payload: (*HumanBoundaryProposalMsg)(nil)})
	if model.activeModal != ModalNone {
		t.Fatal("a nil wrapped pointer must not activate the modal")
	}
}

func ptr(s DecisionSurface) *DecisionSurface { return &s }

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
