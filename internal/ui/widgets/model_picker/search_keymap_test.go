package model_picker

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

func TestSearchFocusCapturesJAndKRunes(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{
		{ID: "vendor/alpha", Provider: "vendor", Name: "Alpha"},
		{ID: "vendor/beta", Provider: "vendor", Name: "Beta"},
		{ID: "vendor/gamma", Provider: "vendor", Name: "Gamma"},
	})).FocusSearch().SetPaneFocus(PaneProviders)

	var cmd tea.Cmd
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if cmd != nil {
		t.Fatal("typing j in search focus must not emit a picker command")
	}
	if got := m.Query(); got != "j" {
		t.Fatalf("query after j = %q, want %q", got, "j")
	}

	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if cmd != nil {
		t.Fatal("typing k in search focus must not emit a picker command")
	}
	if got := m.Query(); got != "jk" {
		t.Fatalf("query after jk = %q, want %q", got, "jk")
	}
	if m.PaneFocus() != PaneModels {
		t.Fatalf("typing in search should focus models pane, got %v", m.PaneFocus())
	}
}

func TestSearchFocusUsesArrowsForModelNavigation(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{
		{ID: "vendor/alpha", Provider: "vendor", Name: "Alpha"},
		{ID: "vendor/beta", Provider: "vendor", Name: "Beta"},
		{ID: "vendor/gamma", Provider: "vendor", Name: "Gamma"},
	})).FocusSearch().SetPaneFocus(PaneModels)

	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.Cursor(); got != 1 {
		t.Fatalf("down cursor = %d, want 1", got)
	}
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.Cursor(); got != 0 {
		t.Fatalf("up cursor = %d, want 0", got)
	}
}

func TestCtrlPIsNotModelPickerNavigation(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{
		{ID: "vendor/alpha", Provider: "vendor", Name: "Alpha"},
		{ID: "vendor/beta", Provider: "vendor", Name: "Beta"},
	})).SetPaneFocus(PaneModels)
	m = m.MoveCursor(1)

	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyCtrlP})
	if cmd != nil {
		t.Fatal("Ctrl+P must not emit a model-picker navigation command")
	}
	if got := updated.Cursor(); got != 1 {
		t.Fatalf("Ctrl+P changed model cursor to %d", got)
	}
	if updated.PaneFocus() != PaneModels {
		t.Fatalf("Ctrl+P changed pane focus to %v", updated.PaneFocus())
	}
	if got := updated.Query(); got != "" {
		t.Fatalf("Ctrl+P changed search query to %q", got)
	}
}
