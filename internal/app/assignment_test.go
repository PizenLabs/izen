package app

// Control-plane regression tests for the ModelAssignmentRequestedMsg
// transaction pipeline: the event payload's ModelID must flow verbatim into
// PrepareTransition, PersistAssignment, and the Runtime Authority commit —
// no fallback layer may override it with a default model.

import (
	"errors"
	"testing"

	coredomain "github.com/PizenLabs/izen/internal/core/domain"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

type stubConfigRepo struct {
	calls []struct{ target, modelID string }
	err   error
}

func (s *stubConfigRepo) PersistAssignment(target string, modelID string) error {
	s.calls = append(s.calls, struct{ target, modelID string }{target, modelID})
	return s.err
}

// The exact chosen model (not the index-0 default) must land in Runtime
// Authority, persistence, the transition event, and the viewport — the
// status bar derives exclusively from ActiveModel().
func TestHandleAssignmentPersistsExactPayloadModel(t *testing.T) {
	repo := &stubConfigRepo{}
	a := NewApp()
	a.SetConfigRepo(repo)
	a.SetCurrentMode(coredomain.WorkspaceAsk)

	msg := model_picker.ModelAssignmentRequestedMsg{
		ModelID:  "inclusionai/ling-3.0-flash-fin:free",
		Provider: "openrouter",
		Target:   model_picker.TargetAsk,
		Policy:   model_picker.InvocationPolicy{Reasoning: "default"},
	}
	event, closeCmd, err := a.HandleModelAssignmentRequestedMsg(msg)
	if err != nil {
		t.Fatalf("HandleModelAssignmentRequestedMsg: %v", err)
	}
	if closeCmd == nil {
		t.Error("must return a modal-teardown command")
	}
	if event.Current.ID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Errorf("event model = %q, want inclusionai", event.Current.ID)
	}
	if !event.Activated {
		t.Error("assignment to the current mode must be Activated")
	}
	if len(repo.calls) != 1 || repo.calls[0].modelID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("persist calls = %+v, want exact inclusionai model", repo.calls)
	}
	if repo.calls[0].target != "ask" {
		t.Errorf("persist target = %q, want ask", repo.calls[0].target)
	}
	got := a.EffectiveModel(coredomain.WorkspaceAsk)
	if got.ID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Errorf("EffectiveModel(ask) = %q, want inclusionai", got.ID)
	}
	if active := a.ActiveModel(); active.ID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Errorf("ActiveModel = %q, want inclusionai", active.ID)
	}
}

// Persistence failure must abort WITHOUT mutating runtime (I3): the status
// surface keeps the prior truth instead of a half-committed model.
func TestHandleAssignmentAbortsRuntimeOnPersistFailure(t *testing.T) {
	repo := &stubConfigRepo{err: errors.New("disk full")}
	a := NewApp()
	a.SetConfigRepo(repo)
	a.SetCurrentMode(coredomain.WorkspaceAsk)

	msg := model_picker.ModelAssignmentRequestedMsg{
		ModelID:  "inclusionai/ling-3.0-flash-fin:free",
		Provider: "openrouter",
		Target:   model_picker.TargetAsk,
	}
	if _, _, err := a.HandleModelAssignmentRequestedMsg(msg); err == nil {
		t.Fatal("must surface the persistence failure")
	}
	if got := a.EffectiveModel(coredomain.WorkspaceAsk); got.ID != "" {
		t.Errorf("EffectiveModel(ask) = %q after failed persist, want empty (no mutation)", got.ID)
	}
}

// Assignment to an inactive target persists + commits to that target only;
// the active model's status truth is untouched (I2).
func TestHandleAssignmentInactiveTargetLeavesActiveModel(t *testing.T) {
	repo := &stubConfigRepo{}
	a := NewApp()
	a.SetConfigRepo(repo)
	a.SetCurrentMode(coredomain.WorkspaceAsk)

	msg := model_picker.ModelAssignmentRequestedMsg{
		ModelID:  "google/gemini-2.5-flash",
		Provider: "gemini",
		Target:   model_picker.TargetPlan,
	}
	event, _, err := a.HandleModelAssignmentRequestedMsg(msg)
	if err != nil {
		t.Fatalf("HandleModelAssignmentRequestedMsg: %v", err)
	}
	if event.Activated {
		t.Error("assignment to an inactive target must not be Activated")
	}
	if got := a.EffectiveModel(coredomain.WorkspacePlan); got.ID != "google/gemini-2.5-flash" {
		t.Errorf("EffectiveModel(plan) = %q, want gemini", got.ID)
	}
	if got := a.ActiveModel(); got.ID != "" {
		t.Errorf("ActiveModel = %q, want empty (ask untouched)", got.ID)
	}
}
