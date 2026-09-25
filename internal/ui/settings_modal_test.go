package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/prompt"
	settings_widget "github.com/PizenLabs/izen/internal/ui/widgets/settings"
)

func TestStandaloneSettingsCommandUsesReducedSurface(t *testing.T) {
	m := newTestModel()
	m.Ready = false // keep the test independent of viewport/document layout
	m.cfg.Style = "balanced"
	m.cfg.UI = config.UIConfig{HideThinking: false, AutoScroll: config.AutoScrollSmart}

	oldPersist := persistGlobalConfigFn
	oldStyle := prompt.ActiveStyle()
	t.Cleanup(func() {
		persistGlobalConfigFn = oldPersist
		prompt.SetActiveStyle(oldStyle)
	})
	persistGlobalConfigFn = func(*config.Config) error { return nil }

	if cmd := m.handleCommand("/settings"); cmd != nil {
		t.Fatalf("/settings returned a startup command: %v", cmd)
	}
	if !m.showSettings || m.showModelPicker {
		t.Fatalf("settings state = %v, model picker = %v", m.showSettings, m.showModelPicker)
	}

	// Response style is the first pane and commits immediately.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("style key did not emit a settings commit")
	}
	commit, ok := cmd().(settings_widget.CommitMsg)
	if !ok {
		t.Fatalf("style command emitted %T", commit)
	}
	m.Update(commit)
	if got := m.cfg.Style; got != string(prompt.StyleVerbose) {
		t.Fatalf("persisted style = %q, want %q", got, prompt.StyleVerbose)
	}
	if got := prompt.ActiveStyle(); got != prompt.StyleVerbose {
		t.Fatalf("active prompt style = %q, want %q", got, prompt.StyleVerbose)
	}

	// Tab to visibility, toggle it, and then move to the viewport pane.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*model)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	commit = cmd().(settings_widget.CommitMsg)
	m.Update(commit)
	if !m.cfg.UI.HideThinking || !m.hideThinkingBlocks {
		t.Fatal("hide-thinking setting was not applied to config and runtime state")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*model)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	commit = cmd().(settings_widget.CommitMsg)
	m.Update(commit)
	if m.cfg.UI.AutoScroll != config.AutoScrollAlways || m.autoScrollMode() != config.AutoScrollAlways {
		t.Fatal("auto-scroll setting was not applied to config and workspace manager")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*model)
	if m.showSettings || !m.ti.Focused() {
		t.Fatal("Esc did not close settings and restore workspace focus")
	}
}

func TestStandaloneSettingsResizePropagatesAndRecentersOverlay(t *testing.T) {
	m := newTestModel()
	m.Ready = false
	m.viewRegistry = nil
	m.openSettings()

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = updated.(*model)
	if m.width != 60 || m.height != 20 {
		t.Fatalf("workspace size = %dx%d, want 60x20", m.width, m.height)
	}
	wantWidth, wantHeight := SettingsModalSize(60, 20)
	if width, height := m.settingsModel.Size(); width != wantWidth || height != wantHeight {
		t.Fatalf("settings outer size = %dx%d, want %dx%d", width, height, wantWidth, wantHeight)
	}

	rendered := m.renderSettingsModal()
	lines := strings.Split(rendered, "\n")
	if len(lines) != 20 {
		t.Fatalf("rendered height = %d, want 20", len(lines))
	}
	borderTop, borderBottom := -1, -1
	for i, line := range lines {
		plain := ansi.Strip(line)
		if width := lipgloss.Width(plain); width > 60 {
			t.Fatalf("line %d width = %d, exceeds terminal width 60", i, width)
		}
		if strings.Contains(plain, "╭") && borderTop < 0 {
			borderTop = i
		}
		if strings.Contains(plain, "╰") {
			borderBottom = i
		}
	}
	if borderTop != 2 || borderBottom != 17 {
		t.Fatalf("settings box rows = %d..%d, want 2..17", borderTop, borderBottom)
	}

	plainTop := ansi.Strip(lines[borderTop])
	left := strings.Index(plainTop, "╭")
	right := strings.LastIndex(plainTop, "╮")
	if left < 0 || right < left {
		t.Fatalf("settings top border missing: %q", plainTop)
	}
	leftColumn := lipgloss.Width(plainTop[:left])
	borderWidth := lipgloss.Width(plainTop[left : right+len("╮")])
	if leftColumn != 2 || borderWidth != wantWidth {
		t.Fatalf("settings top border columns = %d..%d width = %d, want left 2 width %d", leftColumn, right, borderWidth, wantWidth)
	}
}
