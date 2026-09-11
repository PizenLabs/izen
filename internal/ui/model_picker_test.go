package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

func twoStepTestSnapshot() *registry.ModelSnapshot {
	return &registry.ModelSnapshot{
		Models: []registry.ModelDescriptor{
			{ID: "cohere/north-mini-code:free", Provider: "openrouter", Name: "north-mini-code", ContextWindow: 128000},
			{ID: "openai/o1", Provider: "openai", Name: "o1", ContextWindow: 200000},
		},
	}
}

// Enter in the Model Registry list view opens Model Details instead of
// activating directly (2-step activation).
func TestPickerEnterInListOpensDetails(t *testing.T) {
	m := model_picker.New(twoStepTestSnapshot())
	m = m.SetPaneFocus(model_picker.PaneModels)

	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("Enter in list view must NOT activate directly, got %T", cmd())
	}
	if updated.State() != model_picker.StateDetail {
		t.Fatalf("Enter in list view must move picker to StateDetail, got %v", updated.State())
	}
}

// Enter in StateDetails activates the pinned model with the selected
// reasoning variant (e.g. medium).
func TestPickerEnterInDetailsActivatesWithVariant(t *testing.T) {
	openai := registry.ModelDescriptor{ID: "openai/o1", Provider: "openai", Name: "o1"}
	m := model_picker.New(&registry.ModelSnapshot{Models: []registry.ModelDescriptor{openai}})
	m = m.SetPaneFocus(model_picker.PaneModels)

	updated, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.State() != model_picker.StateDetail {
		t.Fatalf("list Enter must open details, got %v", updated.State())
	}
	// Cycle default -> low -> medium via the r key.
	updated, _ = updated.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	updated, _ = updated.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if opt, ok := updated.CurrentReasoningOption(); !ok || opt != "medium" {
		t.Fatalf("reasoning option = %q,%v, want medium,true", opt, ok)
	}
	_, confirmCmd := updated.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if confirmCmd == nil {
		t.Fatal("detail Enter must emit an activation command")
	}
	assign, ok := confirmCmd().(model_picker.ModelAssignmentRequestedMsg)
	if !ok {
		t.Fatalf("detail Enter cmd = %T, want ModelAssignmentRequestedMsg", confirmCmd())
	}
	if assign.ModelID != "openai/o1" {
		t.Errorf("assigned model = %q, want openai/o1", assign.ModelID)
	}
	if assign.Policy.Reasoning != "medium" {
		t.Errorf("assigned variant = %q, want medium", assign.Policy.Reasoning)
	}
}
