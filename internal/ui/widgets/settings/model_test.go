package settings

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func standaloneSettingsTestKey(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func settingsViewText(view string) string {
	plain := ansi.Strip(view)
	plain = strings.NewReplacer("│", "", "╭", "", "╮", "", "╰", "", "╯", "").Replace(plain)
	return strings.Join(strings.Fields(plain), " ")
}

func TestStandaloneSettingsCyclesReducedSchema(t *testing.T) {
	m := New()
	updated, cmd := m.Update(standaloneSettingsTestKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("response-style change must emit a commit command")
	}
	msg, ok := cmd().(CommitMsg)
	if !ok {
		t.Fatalf("commit message = %T, want CommitMsg", msg)
	}
	if msg.Field != FieldResponseStyle || msg.Values.ResponseStyle != ResponseStyleVerbose {
		t.Fatalf("style commit = %+v", msg)
	}
	m = updated.(Model)

	updated, _ = m.Update(standaloneSettingsTestKey(tea.KeyTab))
	m = updated.(Model)
	if m.ActiveTab() != 1 {
		t.Fatalf("Tab active tab = %d, want 1", m.ActiveTab())
	}
	updated, cmd = m.Update(standaloneSettingsTestKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("hide-thinking change must emit a commit command")
	}
	msg = cmd().(CommitMsg)
	if msg.Field != FieldHideThinking || !msg.Values.HideThinking {
		t.Fatalf("visibility commit = %+v", msg)
	}
	m = updated.(Model)

	updated, _ = m.Update(standaloneSettingsTestKey(tea.KeyTab))
	m = updated.(Model)
	_, cmd = m.Update(standaloneSettingsTestKey(tea.KeySpace))
	if cmd == nil {
		t.Fatal("auto-scroll change must emit a commit command")
	}
	msg = cmd().(CommitMsg)
	if msg.Field != FieldAutoScroll || msg.Values.AutoScroll != AutoScrollAlways {
		t.Fatalf("auto-scroll commit = %+v", msg)
	}
}

func TestStandaloneSettingsIgnoresPrintableNavigationRunes(t *testing.T) {
	m := New()
	for _, r := range []rune{'j', 'k'} {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd != nil {
			t.Fatalf("rune %q unexpectedly emitted command", r)
		}
		got := updated.(Model)
		if got.ActiveRow() != m.ActiveRow() || got.ActiveTab() != m.ActiveTab() {
			t.Fatalf("rune %q changed navigation: row=%d tab=%d", r, got.ActiveRow(), got.ActiveTab())
		}
	}
}

func TestStandaloneSettingsViewScopesRowsAndDescriptions(t *testing.T) {
	m := New().SetSize(72, 18)
	assertView := func(t *testing.T, m Model, own []string, forbidden []string) {
		t.Helper()
		view := m.View()
		viewText := settingsViewText(view)
		for _, want := range append(own, settingDescriptions[m.ActiveRow()]) {
			if !strings.Contains(viewText, want) {
				t.Errorf("view missing %q:\n%s", want, view)
			}
		}
		for _, label := range forbidden {
			if strings.Contains(view, label) {
				t.Errorf("tab %d leaked setting %q:\n%s", m.ActiveTab(), label, view)
			}
		}
		for _, reducedSchemaTerm := range []string{"Provider", "Model", "Variant", "Role", "API Key"} {
			if strings.Contains(view, reducedSchemaTerm) {
				t.Errorf("view exposes forbidden settings term %q:\n%s", reducedSchemaTerm, view)
			}
		}
	}

	assertView(t, m,
		[]string{"Response Style", "Balanced"},
		[]string{"Hide Thinking Blocks", "Auto-Scroll Behavior"},
	)

	updated, _ := m.Update(standaloneSettingsTestKey(tea.KeyTab))
	m = updated.(Model)
	assertView(t, m,
		[]string{"Hide Thinking Blocks", "false"},
		[]string{"Response Style", "Auto-Scroll Behavior"},
	)

	updated, _ = m.Update(standaloneSettingsTestKey(tea.KeyTab))
	m = updated.(Model)
	assertView(t, m,
		[]string{"Auto-Scroll Behavior", "Smart"},
		[]string{"Response Style", "Hide Thinking Blocks"},
	)

	updated, _ = m.Update(standaloneSettingsTestKey(tea.KeyShiftTab))
	m = updated.(Model)
	if m.ActiveTab() != 1 {
		t.Fatalf("Shift+Tab active tab = %d, want 1", m.ActiveTab())
	}
}

func TestStandaloneSettingsDescriptionTracksArrowNavigation(t *testing.T) {
	m := New()
	updated, _ := m.Update(standaloneSettingsTestKey(tea.KeyDown))
	m = updated.(Model)
	if got := m.ActiveTab(); got != 1 {
		t.Fatalf("Down active tab = %d, want 1", got)
	}
	view := settingsViewText(m.View())
	if !strings.Contains(view, settingDescriptions[1]) {
		t.Fatalf("Down did not update contextual description:\n%s", view)
	}

	updated, _ = m.Update(standaloneSettingsTestKey(tea.KeyUp))
	m = updated.(Model)
	if got := m.ActiveTab(); got != 0 {
		t.Fatalf("Up active tab = %d, want 0", got)
	}
	view = settingsViewText(m.View())
	if !strings.Contains(view, settingDescriptions[0]) {
		t.Fatalf("Up did not update contextual description:\n%s", view)
	}
}

func TestStandaloneSettingsWindowSizeUsesResponsiveModalBounds(t *testing.T) {
	tests := []struct {
		name         string
		windowWidth  int
		windowHeight int
		wantWidth    int
		wantHeight   int
		wantInnerW   int
		wantInnerH   int
	}{
		{name: "large terminal caps bounds", windowWidth: 120, windowHeight: 50, wantWidth: 72, wantHeight: 18, wantInnerW: 68, wantInnerH: 16},
		{name: "split pane shrinks bounds", windowWidth: 60, windowHeight: 20, wantWidth: 56, wantHeight: 16, wantInnerW: 52, wantInnerH: 14},
		{name: "small pane remains positive", windowWidth: 20, windowHeight: 10, wantWidth: 16, wantHeight: 6, wantInnerW: 12, wantInnerH: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, cmd := New().Update(tea.WindowSizeMsg{Width: tt.windowWidth, Height: tt.windowHeight})
			if cmd != nil {
				t.Fatal("WindowSizeMsg must not emit a command")
			}
			m := updated.(Model)
			width, height := m.Size()
			if width != tt.wantWidth || height != tt.wantHeight {
				t.Fatalf("outer size = %dx%d, want %dx%d", width, height, tt.wantWidth, tt.wantHeight)
			}
			innerWidth, innerHeight := m.InnerSize()
			if innerWidth != tt.wantInnerW || innerHeight != tt.wantInnerH {
				t.Fatalf("inner size = %dx%d, want %dx%d", innerWidth, innerHeight, tt.wantInnerW, tt.wantInnerH)
			}
			view := m.View()
			if lines := strings.Count(view, "\n") + 1; lines > tt.wantInnerH {
				t.Fatalf("view rendered %d lines inside %d-line content bounds", lines, tt.wantInnerH)
			}
			if tt.windowHeight <= 10 && !strings.Contains(settingsViewText(view), settingLabels[0]) {
				t.Fatalf("small pane dropped the focused setting:\n%s", view)
			}
		})
	}
}

func TestStandaloneSettingsEscEmitsClose(t *testing.T) {
	updated, cmd := New().Update(standaloneSettingsTestKey(tea.KeyEsc))
	if !updated.(Model).Done() || cmd == nil {
		t.Fatal("Esc must close the settings widget and emit CloseMsg")
	}
	if _, ok := cmd().(CloseMsg); !ok {
		t.Fatalf("Esc command emitted %T, want CloseMsg", cmd())
	}
}
