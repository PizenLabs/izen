package app

// Control-plane regression tests for the ModelAssignmentRequestedMsg
// transaction pipeline: the event payload's ModelID must flow verbatim into
// PrepareTransition, PersistActiveBinding, and the Runtime Authority commit —
// no fallback layer may override it with a default model.

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

type stubConfigRepo struct {
	calls []struct{ provider, model, variant string }
	err   error
}

func (s *stubConfigRepo) PersistActiveBinding(provider, model, variant string) error {
	s.calls = append(s.calls, struct{ provider, model, variant string }{provider, model, variant})
	return s.err
}

// The exact chosen model (not the index-0 default) must land in Runtime
// Authority, persistence, the transition event, and the viewport — the
// status bar derives exclusively from ActiveModel().
func TestHandleAssignmentPersistsExactPayloadModel(t *testing.T) {
	repo := &stubConfigRepo{}
	a := NewApp()
	a.SetConfigRepo(repo)
	a.SetCurrentMode(runtime.TargetAsk)

	msg := model_picker.ModelAssignmentRequestedMsg{
		ModelID:  "inclusionai/ling-3.0-flash-fin:free",
		Provider: "openrouter",
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
	if len(repo.calls) != 1 || repo.calls[0].model != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("persist calls = %+v, want exact inclusionai model", repo.calls)
	}
	if repo.calls[0].provider != "openrouter" {
		t.Errorf("persist provider = %q, want openrouter", repo.calls[0].provider)
	}
	got := a.EffectiveModel(runtime.TargetAsk)
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
	a.SetCurrentMode(runtime.TargetAsk)

	msg := model_picker.ModelAssignmentRequestedMsg{
		ModelID:  "inclusionai/ling-3.0-flash-fin:free",
		Provider: "openrouter",
	}
	if _, _, err := a.HandleModelAssignmentRequestedMsg(msg); err == nil {
		t.Fatal("must surface the persistence failure")
	}
	if got := a.EffectiveModel(runtime.TargetAsk); got.ID != "" {
		t.Errorf("EffectiveModel(ask) = %q after failed persist, want empty (no mutation)", got.ID)
	}
}

// Assignment activates the binding for the current mode. Under the
// single-binding model, all assignments activate since there is only one
// active binding.
func TestHandleAssignmentActivatesForCurrentMode(t *testing.T) {
	repo := &stubConfigRepo{}
	a := NewApp()
	a.SetConfigRepo(repo)
	a.SetCurrentMode(runtime.TargetAsk)

	msg := model_picker.ModelAssignmentRequestedMsg{
		ModelID:  "google/gemini-2.5-flash",
		Provider: "gemini",
	}
	event, _, err := a.HandleModelAssignmentRequestedMsg(msg)
	if err != nil {
		t.Fatalf("HandleModelAssignmentRequestedMsg: %v", err)
	}
	if !event.Activated {
		t.Error("assignment to the current mode must be Activated")
	}
	if got := a.ActiveModel(); got.ID != "google/gemini-2.5-flash" {
		t.Errorf("ActiveModel = %q, want google/gemini-2.5-flash", got.ID)
	}
}
