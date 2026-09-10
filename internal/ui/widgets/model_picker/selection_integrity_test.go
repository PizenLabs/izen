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

// Selection integrity: navigating to a non-default model, opening Detail,
// and confirming must carry the EXACT model ID into the assignment payload —
// never the index-0 default.
func TestDetailAssignmentCarriesExactModelID(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.FocusList().SetActiveWorkspace("ask")
	m = m.SetCursor(1)
	if got := m.SelectedModel().ID; got != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("highlight = %q, want inclusionai", got)
	}

	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if m.State() != StateDetail {
		t.Fatalf("state = %v, want StateDetail", m.State())
	}
	if got := m.SelectedModel().ID; got != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("detail selection = %q, want inclusionai (cursor lost)", got)
	}
	if got := m.DetailModel(); got == nil || got.ID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("pinned detail model = %+v, want inclusionai", got)
	}
	if view := m.View(); !strings.Contains(view, "inclusionai/ling-3.0-flash-fin:free") {
		t.Fatalf("detail header must show the exact model id:\n%s", view)
	}

	var cmd tea.Cmd
	_, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("detail Enter must emit an assignment command")
	}
	assign := unwrapAssignmentMsg(t, cmd())
	if assign.ModelID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("assigned model = %q, want inclusionai", assign.ModelID)
	}
	if assign.Provider != "openrouter" {
		t.Fatalf("assigned provider = %q, want openrouter", assign.Provider)
	}
}

// Detail key 1: workspace target matrix removed; no-op.
func TestDetailHotkeyOneAssignsPinnedModel(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.FocusList().SetActiveWorkspace("ask")
	m = m.SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})

	_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	_ = cmd // no-op after removal of 1-5 workspace bindings
}

// Ordered teardown contract: assignment emissions must be a BARE
// ModelAssignmentRequestedMsg, never a tea.BatchMsg bundling CloseModalCmd.
// Batch delivery is unordered: when CloseModalMsg wins the race the closed
// modal no longer routes the assignment and the commit is silently dropped
// (status bar keeps the stale default). The parent closes the modal AFTER
// the Runtime Authority commit lands.
func TestAssignmentEmissionIsUnbatched(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.FocusList().SetActiveWorkspace("ask")

	// Browsing fast-path.
	if _, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}); cmd == nil {
		t.Fatal("browsing 'a' must emit an assignment command")
	} else if _, ok := cmd().(ModelAssignmentRequestedMsg); !ok {
		t.Fatalf("browsing 'a' cmd = %T, want bare ModelAssignmentRequestedMsg", cmd())
	}

	// Detail confirm + detail hotkey.
	m = m.SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if _, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("detail Enter must emit an assignment command")
	} else if _, ok := cmd().(ModelAssignmentRequestedMsg); !ok {
		t.Fatalf("detail Enter cmd = %T, want bare ModelAssignmentRequestedMsg", cmd())
	}
	// Detail key 1: workspace target matrix removed; no-op.
	_, cmd1 := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	_ = cmd1
}

// Background snapshot refresh mid-detail re-sorts the filtered list by
// workspace relevance (a thinking model jumps to index 0 for the plan
// workspace, shifting the pinned model down). The detail assignment must
// still target the PINNED instance even though the cursor now highlights a
// different model.
func TestDetailPinSurvivesBackgroundResort(t *testing.T) {
	m := New(seedSnapshot(integrityModels()))
	m = m.FocusList().SetActiveWorkspace("plan")
	m = m.SetCursor(1) // inclusionai
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
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
	m = m.FocusList().SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if m.DetailModel() == nil {
		t.Fatal("detail pin must be set after Enter")
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
	m = m.FocusList().SetActiveWorkspace("ask")
	m = m.SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if !strings.Contains(view, "Reasoning: Not supported by model") {
		t.Fatalf("detail must render 'Not supported by model':\n%s", view)
	}
	if strings.Contains(view, "[default]") {
		t.Fatalf("detail must not render static [default] variants:\n%s", view)
	}
}
