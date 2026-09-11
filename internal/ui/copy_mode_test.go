package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCopyMode_SlashCommandsPurged(t *testing.T) {
	m := newViTestModel()
	m.state = StateChat
	m.inViMode = false
	m.records = []record{{role: roleAI, text: "a"}, {role: roleAI, text: "b"}}
	for _, cmd := range []string{"/copy-mode", "/copy_mode", "/inspect"} {
		_ = m.handleCommand(cmd)
		if m.inViMode {
			t.Fatalf("%s must NOT enter copy mode after purge (triple-Esc is canonical)", cmd)
		}
	}
}

func TestCopyMode_TripleEscCanonical(t *testing.T) {
	m := newViTestModel()
	m.state = StateChat
	m.inViMode = false
	m.streaming = false
	m.agentRunning = false
	// Simulate 3 consecutive Esc presses through Update (canonical trigger).
	var updated tea.Model
	updated = m
	for i := 0; i < 3; i++ {
		var cmd tea.Cmd
		updated, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
		_ = cmd
	}
	mm := updated.(*model)
	if !mm.inViMode {
		t.Fatal("triple-Esc must enter copy mode (canonical trigger)")
	}
}

func TestCopyMode_ExitViaHandler(t *testing.T) {
	m := newViTestModel()
	_ = m.enterViMode()
	if !m.inViMode {
		t.Fatal("enterViMode failed")
	}
	// Mouse is globally enabled, so exit no longer disables it.
	_, cmd := m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if cmd != nil {
		_ = cmd()
	}
	if m.inViMode {
		t.Fatal("should have exited vi mode")
	}
}

func TestCopyMode_ExitViaEsc(t *testing.T) {
	m := newViTestModel()
	_ = m.enterViMode()
	_, cmd := m.handleViModeKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		_ = cmd()
	}
	if m.inViMode {
		t.Fatal("Esc should exit copy mode")
	}
}

func TestCopyMode_BlockedWhileProcessing(t *testing.T) {
	m := newViTestModel()
	m.state = StateProcessing
	m.inViMode = false
	// Slash entry is purged: /copy-mode is unknown and never enters vi mode.
	_ = m.handleCommand("/copy-mode")
	if m.inViMode {
		t.Fatal("purged /copy-mode must not enter vi mode while processing")
	}
}

func TestCopyMode_WheelScrollsViewport(t *testing.T) {
	m := newViTestModel()
	// Build long transcript so scrolling is needed.
	m.records = make([]record, 30)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: strings.Repeat("line content ", 5)}
	}
	m.height = 15
	m.Ready = true
	// Enter copy mode to enable mouse handling.
	_ = m.enterViMode()
	// Sync to bottom first.
	m.cursorLine = 29
	m.syncViewportToCursor()
	initialOffset := m.Viewport.YOffset
	// Simulate wheel up (scroll up).
	newModel, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m2 := newModel.(*model)
	// Viewport should have moved (YOffset changed or userIsScrollingUp set).
	if !m2.userIsScrollingUp && m2.Viewport.YOffset == initialOffset {
		t.Fatalf("wheel up should set userIsScrollingUp or move viewport: initial %d, got %d, scrollingUp=%v", initialOffset, m2.Viewport.YOffset, m2.userIsScrollingUp)
	}
	// Left drag now starts a mouse selection (presentation-only) rather than
	// being ignored. Verify that a left press activates selection.
	m3 := newViTestModel()
	m3.records = m.records
	m3.height = 15
	m3.Ready = true
	// Outside vi mode, left press should start native selection state.
	updated, _ := m3.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 2})
	m4 := updated.(*model)
	if !m4.mouseSel.Active {
		t.Fatal("left press should start mouse selection")
	}
}

func TestCopy_StillWorksAfterCopyMode(t *testing.T) {
	cb := &fakeClipboard{}
	m := newViTestModel()
	m.records = []record{{role: roleUser, text: "hello"}, {role: roleAI, text: "world"}}
	m.clipboard = cb
	// Enter and exit copy mode, then copy.
	_ = m.enterViMode()
	_, exitCmd := m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if exitCmd != nil {
		_ = exitCmd()
	}
	m.handleCopy()
	if cb.content == "" || !strings.Contains(cb.content, "hello") {
		t.Fatalf("/copy after copy-mode should still copy transcript, got %q", cb.content)
	}
}

func TestCopyMode_ViSearchAndYankPreserved(t *testing.T) {
	m := newViTestModel()
	m.records = []record{
		{role: roleAI, text: "first error: foo"},
		{role: roleAI, text: "second error: bar"},
		{role: roleAI, text: "third: baz"},
	}
	_ = m.enterViMode()
	// Search via '/' in vi mode.
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.viCmdMode || m.viCmdBuf != "/" {
		t.Fatalf("expected search cmd mode, got viCmdMode=%v buf=%q", m.viCmdMode, m.viCmdBuf)
	}
}
