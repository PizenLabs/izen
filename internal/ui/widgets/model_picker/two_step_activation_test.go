package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// 2-step activation: Enter in the list view opens Model Details / Variant
// configuration instead of activating directly.
func TestEnterInListOpensDetails(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.SetPaneFocus(PaneModels)

	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("Enter in list view must NOT emit a command, got %T", cmd())
	}
	if updated.State() != StateDetail {
		t.Fatalf("Enter in list view must move picker to StateDetail, got %v", updated.State())
	}
	if updated.DetailModel() == nil {
		t.Fatal("detail pin must be set after Enter")
	}
	if got := updated.SelectedModel().ID; got != "openrouter/deepseek/deepseek-r1" {
		t.Fatalf("detail model = %q, want highlighted deepseek-r1", got)
	}
	// Browsing footer advertises the 2-step contract.
	view := New(seedSnapshot(testModels())).View()
	if !strings.Contains(view, "configure & activate") {
		t.Fatalf("browsing footer must show 'Enter configure & activate', got:\n%s", view)
	}
	if strings.Contains(view, "activate / save key") {
		t.Fatalf("browsing footer must NOT show legacy 'activate / save key':\n%s", view)
	}
}

// 2-step activation confirm: Enter in StateDetails activates the pinned model
// with the selected reasoning variant (e.g. medium) via a bare assignment.
func TestEnterInDetailsActivatesWithVariant(t *testing.T) {
	openai := registry.ModelDescriptor{ID: "openai/o1", Provider: "openai", Name: "o1"}
	m := New(seedSnapshot([]registry.ModelDescriptor{openai}))
	m = m.SetPaneFocus(PaneModels)

	// Step 1: Enter opens details.
	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("list Enter must NOT emit, got %T", cmd())
	}
	if updated.State() != StateDetail {
		t.Fatalf("list Enter must move to StateDetail, got %v", updated.State())
	}

	// Step 2: press r twice to reach medium (default -> low -> medium).
	updated, _ = updated.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	updated, _ = updated.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if opt, ok := updated.CurrentReasoningOption(); !ok || opt != "medium" {
		t.Fatalf("reasoning option = %q,%v, want medium,true", opt, ok)
	}

	// Step 3: Enter confirms with the selected variant.
	_, confirmCmd := updated.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if confirmCmd == nil {
		t.Fatal("detail Enter must emit an assignment command")
	}
	msg := confirmCmd()
	assign, ok := msg.(ModelAssignmentRequestedMsg)
	if !ok {
		t.Fatalf("detail Enter cmd = %T, want bare ModelAssignmentRequestedMsg", msg)
	}
	if assign.ModelID != "openai/o1" {
		t.Errorf("assigned model = %q, want openai/o1", assign.ModelID)
	}
	if assign.Policy.Reasoning != "medium" {
		t.Errorf("assigned variant = %q, want medium", assign.Policy.Reasoning)
	}

	// Same path via the spec-named helper.
	helperCmd := updated.activateModelWithVariantCmd(updated.SelectedModel(), "medium")
	if helperCmd == nil {
		t.Fatal("activateModelWithVariantCmd must return a command")
	}
	if hAssign, ok := helperCmd().(ModelAssignmentRequestedMsg); !ok || hAssign.Policy.Reasoning != "medium" {
		t.Fatalf("helper assignment = %#v, want reasoning=medium", helperCmd())
	}
}
