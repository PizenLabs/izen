package model_picker

// End-to-end lifecycle regression tests for the Model Picker assignment
// pipeline: browse -> detail -> assign -> Runtime Authority payload.
//
// These tests exist because isolated struct tests masked a lifecycle failure:
// the Detail View rebound its selection against the mutable cursor on every
// access, assignments raced modal teardown via tea.Batch (dropping the commit
// when CloseModalMsg won), and the generic provider reasoning fallback
// fabricated options for known non-reasoning models. Each test below fails
// on the pre-fix behavior.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

func integrityModels() []registry.ModelDescriptor {
	return []registry.ModelDescriptor{
		{ID: "cohere/north-mini-code:free", Provider: "openrouter", Name: "north-mini-code", ContextWindow: 128000, InputCostPerM: 0, OutputCostPerM: 0},
		{ID: "inclusionai/ling-3.0-flash-fin:free", Provider: "openrouter", Name: "ling-3.0-flash-fin", ContextWindow: 128000, InputCostPerM: 0, OutputCostPerM: 0},
		{ID: "google/gemini-2.5-flash", Provider: "gemini", Name: "gemini-2.5-flash", ContextWindow: 1000000, InputCostPerM: 0.3, OutputCostPerM: 2.5},
	}
}

// Selection integrity: navigating to a non-default model and pressing Enter
// must open details for the EXACT model — never the index-0 default — and
// confirming in details must carry that ID into the assignment payload.
func TestDetailAssignmentCarriesExactModelID(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.SetPaneFocus(PaneModels).SetCursor(1)
	if got := m.SelectedModel().ID; got != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("highlight = %q, want inclusionai", got)
	}

	// Enter in PaneModels opens details (2-step, no direct commit)
	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter in PaneModels must NOT emit directly (opens details)")
	}
	if updated.State() != StateDetail {
		t.Fatalf("Enter in PaneModels must move to StateDetail, got %v", updated.State())
	}
	if got := updated.SelectedModel().ID; got != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("detail model = %q, want inclusionai", got)
	}
	_, cmd2 := updated.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("Enter in StateDetail must emit an assignment command")
	}
	assign := unwrapAssignmentMsg(t, cmd2())
	if assign.ModelID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("assigned model = %q, want inclusionai", assign.ModelID)
	}
	if assign.Provider != "openrouter" {
		t.Fatalf("assigned provider = %q, want openrouter", assign.Provider)
	}
}

// Key 1 maps to search input (workspace matrix removed); no-op for assignment.
func TestDetailHotkeyOneAssignsPinnedModel(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.SetPaneFocus(PaneModels).SetCursor(1)

	_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if cmd != nil {
		t.Fatalf("key 1 must not emit a command (goes to search), got %T", cmd())
	}
}

// Ordered teardown contract: assignment emissions must be a BARE
// ModelAssignmentRequestedMsg, never a tea.BatchMsg bundling CloseModalCmd.
// Batch delivery is unordered: when CloseModalMsg wins the race the closed
// modal no longer routes the assignment and the commit is silently dropped
// (status bar keeps the stale default). The parent closes the modal AFTER
// the Runtime Authority commit lands.
func TestAssignmentEmissionIsUnbatched(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.SetPaneFocus(PaneModels)
	m = m.SetCursor(1)

	// Enter in PaneModels opens details (no command).
	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter in PaneModels must NOT emit (opens details)")
	}
	if updated.State() != StateDetail {
		t.Fatalf("Enter in PaneModels must move to StateDetail, got %v", updated.State())
	}
	// Detail Enter confirms with a bare assignment message.
	if _, cmd2 := updated.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter}); cmd2 == nil {
		t.Fatal("Enter in StateDetail must emit an assignment command")
	} else if _, ok := cmd2().(ModelAssignmentRequestedMsg); !ok {
		t.Fatalf("Enter cmd = %T, want bare ModelAssignmentRequestedMsg", cmd2())
	}
}

// Background snapshot refresh mid-detail re-sorts the filtered list by
// workspace relevance (a thinking model jumps to index 0 for the plan
// workspace, shifting the pinned model down). The detail assignment must
// still target the PINNED instance even though the cursor now highlights a
// different model.
func TestDetailPinSurvivesBackgroundResort(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.SetPaneFocus(PaneModels).SetCursor(1) // inclusionai
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i"), Alt: true})
	if m.State() != StateDetail {
		t.Fatalf("state = %v, want StateDetail", m.State())
	}

	refreshed := append([]registry.ModelDescriptor{
		{ID: "anthropic/claude-opus-4-thinking", Provider: "anthropic", Name: "opus-thinking",
			ContextWindow: 200000, Capabilities: []registry.ModelCapability{registry.CapThinking}},
	}, integrityModels()...)
	m, _ = m.UpdateModel(SnapshotMsg{Snap: seedSnapshot(refreshed)})

	// Prove the test exercises real drift: the cursor highlight moved.
	if hl := m.Highlighted(); hl == nil || hl.ID == "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("test setup invalid: expected cursor drift, highlight = %+v", hl)
	}
	// The detail selection must NOT have followed the cursor.
	if got := m.SelectedModel().ID; got != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("detail selection drifted to %q, want pinned inclusionai", got)
	}

	var cmd tea.Cmd
	_, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("detail Enter must emit an assignment command")
	}
	if assign := unwrapAssignmentMsg(t, cmd()); assign.ModelID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("assigned model = %q, want pinned inclusionai", assign.ModelID)
	}
}

// Esc from detail releases the pin and returns to live-highlight binding.
func TestEscClearsDetailPin(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.SetPaneFocus(PaneModels).SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i"), Alt: true})
	if m.DetailModel() == nil {
		t.Fatal("detail pin must be set after inspect")
	}
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEsc})
	if m.State() != StateBrowsing {
		t.Fatalf("state = %v, want StateBrowsing", m.State())
	}
	if m.DetailModel() != nil {
		t.Fatalf("detail pin must be cleared after Esc, got %+v", m.DetailModel())
	}
}

// Truthful detail: the non-reasoning inclusionai model must render
// "Reasoning: Not supported by model" with zero variant pills. The generic
// OpenRouter provider fallback must not fabricate [default] low medium ...
// options for explicitly non-reasoning families.
func TestDetailReasoningTruthfulForNonReasoningModel(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.SetPaneFocus(PaneModels).SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i"), Alt: true})

	view := m.View()
	if !strings.Contains(view, "Not supported by model") {
		t.Fatalf("detail must render 'Not supported by model':\n%s", view)
	}
	if strings.Contains(view, "[default]") {
		t.Fatalf("detail must not render static [default] variants:\n%s", view)
	}
}
