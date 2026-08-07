package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// TestQuitCommandOpensConfirmModal is the primary DoD: /quit and /q must set
// pendingQuitConfirm instead of invoking process shutdown directly.
func TestQuitCommandOpensConfirmModal(t *testing.T) {
	for _, input := range []string{"/quit", "/q"} {
		m := newTestModel()
		m.state = StateChat
		cmd := m.handleCommand(input)
		if !m.pendingQuitConfirm {
			t.Errorf("handleCommand(%q) must open the quit-confirm modal", input)
		}
		if m.quitConfirmYes {
			t.Errorf("handleCommand(%q) must default focus to [ No ]", input)
		}
		if cmd != nil {
			t.Errorf("handleCommand(%q) must not call shutdown directly (got non-nil cmd)", input)
		}
	}
}

// TestQuitConfirmEscapeCancels verifies Esc dismisses the modal cleanly.
func TestQuitConfirmEscapeCancels(t *testing.T) {
	m := newTestModel()
	m.pendingQuitConfirm = true
	m.quitConfirmYes = true // even a highlighted [ Yes ] is cancelled by Esc

	cmd := m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.pendingQuitConfirm {
		t.Error("Esc must cancel the quit sequence")
	}
	if m.quitConfirmYes {
		t.Error("cancel must reset the [ Yes ] selection to [ No ]")
	}
	if cmd != nil {
		t.Error("cancel must not return a shutdown command")
	}
}

// TestQuitConfirmEnterOnNoCancels verifies Enter with [ No ] selected cancels
// the quit sequence cleanly (the default and the safe path).
func TestQuitConfirmEnterOnNoCancels(t *testing.T) {
	m := newTestModel()
	m.pendingQuitConfirm = true
	m.quitConfirmYes = false

	cmd := m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingQuitConfirm {
		t.Error("Enter on [ No ] must cancel the quit sequence")
	}
	if cmd != nil {
		t.Error("Enter on [ No ] must not return a shutdown command")
	}
}

// TestQuitConfirmNAndCtrlCCancel verifies the n/N and Ctrl+C cancel paths.
func TestQuitConfirmNAndCtrlCCancel(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("n")},
		{Type: tea.KeyRunes, Runes: []rune("N")},
		{Type: tea.KeyCtrlC},
	} {
		m := newTestModel()
		m.pendingQuitConfirm = true
		m.handleQuitConfirmKey(key)
		if m.pendingQuitConfirm {
			t.Errorf("%q must cancel the quit sequence", key.String())
		}
	}
}

// TestQuitConfirmNavigateAndYes verifies the DoD confirm flow: navigating →
// selects [ Yes ], and Enter then triggers the shutdown sequence.
func TestQuitConfirmNavigateAndYes(t *testing.T) {
	m := newTestModel()
	m.pendingQuitConfirm = true
	m.quitConfirmYes = false

	// → toggles to [ Yes ]; h/l and Tab toggle too.
	m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyRight})
	if !m.quitConfirmYes {
		t.Fatal("→ must select [ Yes ]")
	}
	m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.quitConfirmYes {
		t.Fatal("← must toggle back to [ No ]")
	}
	m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if !m.quitConfirmYes {
		t.Fatal("l must select [ Yes ]")
	}
	m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.quitConfirmYes {
		t.Fatal("Tab must toggle selection")
	}
	m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyTab})
	if !m.quitConfirmYes {
		t.Fatal("second Tab must restore [ Yes ]")
	}

	cmd := m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on [ Yes ] must return the shutdown command")
	}
	if m.pendingQuitConfirm {
		t.Error("confirming the quit must close the modal")
	}
}

// TestQuitConfirmYConfirms verifies the y/Y shortcut confirms regardless of
// the current selection.
func TestQuitConfirmYConfirms(t *testing.T) {
	m := newTestModel()
	m.pendingQuitConfirm = true
	m.quitConfirmYes = false
	if cmd := m.handleQuitConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}); cmd == nil {
		t.Fatal("y must trigger the shutdown command")
	}
	if m.pendingQuitConfirm {
		t.Error("y must close the modal")
	}
}

// TestQuitConfirmFreezesInput verifies the modal swallows unrelated keys: text
// typed while the modal is open never reaches the input buffer.
func TestQuitConfirmFreezesInput(t *testing.T) {
	m := newTestModel()
	m.pendingQuitConfirm = true
	m.ti.SetValue("still here")

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := m.ti.Value(); got != "still here" {
		t.Errorf("input must be frozen while the modal is open, got %q", got)
	}
	if cmd != nil {
		t.Error("unrelated keys must be swallowed without emitting commands")
	}
	if !m.pendingQuitConfirm {
		t.Error("unrelated keys must not dismiss the modal")
	}
}

// TestQuitConfirmModalRenders verifies the modal markup: title, question, and
// a [ No ]/ [ Yes ] choice with [ No ] highlighted by default.
func TestQuitConfirmModalRenders(t *testing.T) {
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prevProfile)

	m := newTestModel()
	m.pendingQuitConfirm = true
	m.quitConfirmYes = false

	modal := ansi.Strip(m.renderQuitConfirmModal(40))
	for _, want := range []string{"QUIT IZEN", "Are you sure you want to exit", "► [ No ]", "[ Yes ]"} {
		if !strings.Contains(modal, want) {
			t.Errorf("modal must contain %q:\n%s", want, modal)
		}
	}

	m.quitConfirmYes = true
	modalYes := ansi.Strip(m.renderQuitConfirmModal(40))
	if !strings.Contains(modalYes, "► [ Yes ]") {
		t.Errorf("selected modal must highlight [ Yes ]:\n%s", modalYes)
	}
}

// TestQuitConfirmOverlayDimsBackground verifies the overlay keeps the dimmed
// workspace behind the centered dialog.
func TestQuitConfirmOverlayDimsBackground(t *testing.T) {
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prevProfile)

	m := newTestModel()
	m.width = 80
	m.height = 24
	m.pendingQuitConfirm = true

	out := m.renderQuitConfirmOverlay("session content line\nsecond line")
	stripped := ansi.Strip(out)
	if !strings.Contains(stripped, "QUIT IZEN") {
		t.Error("overlay must render the modal title")
	}
	if !strings.Contains(stripped, "session content line") {
		t.Error("overlay must keep the dimmed workspace behind the modal")
	}
	// Faint renders as "\x1b[2m" alone or "\x1b[2;...m" combined with color.
	if !strings.Contains(out, "\x1b[2") {
		t.Error("overlay must dim (faint) the background workspace")
	}
}
