package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/workspace"
)

func TestStatusModalResizePropagatesAndRecentersOverlay(t *testing.T) {
	m := newTestModel()
	m.Ready = false
	m.viewRegistry = nil
	if cmd := m.handleCommand("/status"); cmd == nil {
		t.Fatal("/status did not dispatch a collector command")
	}
	if !m.showStatus {
		t.Fatal("/status did not open the modal")
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 28})
	m = updated.(*model)
	wantWidth, wantHeight := StatusModalSize(60, 28)
	if width, height := m.statusView.Size(); width != wantWidth || height != wantHeight {
		t.Fatalf("status outer size = %dx%d, want %dx%d", width, height, wantWidth, wantHeight)
	}
	if m.width != 60 || m.height != 28 {
		t.Fatalf("workspace size = %dx%d, want 60x28", m.width, m.height)
	}
	m.statusView = m.statusView.SetSnapshot(workspace.Status{
		WorkspaceRoot: "/tmp/workspace",
		VCS: workspace.VCSStatus{
			HasGit:         true,
			Available:      true,
			Branch:         "feature/status",
			ShortSHA:       "abc1234",
			IsDirty:        true,
			ModifiedFiles:  2,
			UntrackedFiles: 1,
		},
		Indexer: workspace.IndexerStatus{Status: "indexed", Indexed: true, SymbolCount: 42, ASTStatus: "ready"},
		Session: workspace.SessionStatus{SlotID: "A", Title: "Status test", Goal: "inspect", Tokens: workspace.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, TurnCount: 3},
		Engine:  workspace.EngineStatus{Provider: "ollama", Model: "qwen", PolicyGate: "Read-Only", Mode: "ask"},
	})

	rendered := m.renderStatusModal()
	lines := strings.Split(rendered, "\n")
	if len(lines) != 28 {
		t.Fatalf("rendered height = %d, want 28", len(lines))
	}
	borderTop, borderBottom := -1, -1
	for i, line := range lines {
		plain := ansi.Strip(line)
		if width := lipgloss.Width(plain); width > 60 {
			t.Fatalf("line %d width = %d, exceeds terminal width 60", i, width)
		}
		if (strings.Contains(plain, "┌") || strings.Contains(plain, "╭")) && borderTop < 0 {
			borderTop = i
		}
		if strings.Contains(plain, "└") || strings.Contains(plain, "╰") {
			borderBottom = i
		}
	}
	if borderTop < 0 || borderBottom < 0 {
		t.Fatalf("status box borders missing: top=%d bottom=%d\n%s", borderTop, borderBottom, rendered)
	}
	plain := ansi.Strip(rendered)
	for _, value := range []string{
		"WORKSPACE & VCS", "SYMBOL ENGINE", "SESSION CONTEXT", "ENGINE & AUTHORITY",
		"feature/status", "abc1234", "2 modified", "1 untracked", "42", "Status test", "ollama", "qwen", "Read-Only",
	} {
		if !strings.Contains(plain, value) {
			t.Fatalf("narrow status modal missing %q:\n%s", value, rendered)
		}
	}
	plainTop := ansi.Strip(lines[borderTop])
	left := -1
	for _, corner := range []string{"┌", "╭"} {
		if idx := strings.Index(plainTop, corner); idx >= 0 {
			left = idx
			break
		}
	}
	right := strings.LastIndex(plainTop, "┐")
	if right < 0 {
		right = strings.LastIndex(plainTop, "╮")
	}
	if left < 0 || right < left {
		t.Fatalf("status top border missing: %q", plainTop)
	}
	// The top border run ends at the last corner rune; slice by byte length
	// of the multi-byte box-drawing corner (both ┐ and ╮ are 3 bytes).
	end := right + len("╮")
	if got := lipgloss.Width(plainTop[left:end]); got != wantWidth {
		t.Fatalf("status top border width = %d, want %d", got, wantWidth)
	}
}

func TestStatusModalConsumesTypingAndDismisses(t *testing.T) {
	m := newTestModel()
	m.Ready = false
	m.handleCommand("/status")
	if !m.showStatus {
		t.Fatal("status did not open")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !m.showStatus || m.ti.Value() != "" {
		t.Fatalf("typing leaked through status modal: open=%v value=%q", m.showStatus, m.ti.Value())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showStatus || !m.ti.Focused() {
		t.Fatal("Esc did not close status and restore prompt focus")
	}

	m.handleCommand("/status")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.showStatus || !m.ti.Focused() {
		t.Fatal("q did not close status and restore prompt focus")
	}

	m.handleCommand("/status")
	for _, r := range "/status" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.showStatus || !m.ti.Focused() {
		t.Fatal("re-entered /status did not close status and restore prompt focus")
	}
}
