package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCtrlPTogglesGlobalSettingsFromWorkspace(t *testing.T) {
	m := newTestModel()
	m.Ready = false
	m.showModelPicker = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*model)
	if !m.showSettings {
		t.Fatal("Ctrl+P did not open the standalone settings overlay")
	}
	if m.showModelPicker {
		t.Fatal("Ctrl+P left the model picker active underneath settings")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*model)
	if m.showSettings {
		t.Fatal("second Ctrl+P did not close settings")
	}
	if !m.ti.Focused() {
		t.Fatal("closing settings with Ctrl+P did not restore workspace input focus")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*model)
	if m.showSettings {
		t.Fatal("Esc did not close settings")
	}
	if !m.ti.Focused() {
		t.Fatal("closing settings with Esc did not restore workspace input focus")
	}
}
