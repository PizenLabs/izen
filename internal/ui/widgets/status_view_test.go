package widgets

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/workspace"
)

func TestRenderStatusContainsAllSections(t *testing.T) {
	panel := RenderStatus(workspace.Status{
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
		Engine:  workspace.EngineStatus{Provider: "ollama", Model: "qwen", PolicyGate: "Read-Only"},
	})

	for _, section := range []string{"WORKSPACE STATUS", "WORKSPACE & VCS", "SYMBOL ENGINE", "SESSION CONTEXT", "ENGINE & AUTHORITY"} {
		if !strings.Contains(panel, section) {
			t.Errorf("panel missing %q:\n%s", section, panel)
		}
	}
	for _, value := range []string{"/tmp/workspace", "feature/status", "abc1234", "2 modified", "1 untracked", "42", "Status test", "ollama", "qwen", "Policy:", "Read-Only", "Path:", "Git:", "Lynx:", "42 symbols", "AST", "Slot:", "Tokens:", "↑10", "↓5", "15 total", "3 turns", "Model:", "ollama / qwen", "────────", "Esc / q: Close status modal"} {
		if !strings.Contains(panel, value) {
			t.Errorf("panel missing %q:\n%s", value, panel)
		}
	}
	// The redundant "Branch:" prefix was removed: Git carries branch/SHA directly.
	if strings.Contains(panel, "Branch:") {
		t.Errorf("panel should not contain redundant %q:\n%s", "Branch:", panel)
	}

	lines := strings.Split(panel, "\n")
	// Shrink-wrap: every row shares the fixed outer width, the rounded frame
	// encloses the content, and the divider/footer are pinned at the bottom
	// with no trailing empty gap.
	for i, line := range lines {
		if width := lipgloss.Width(ansi.Strip(line)); width != statusModalMaxWidth {
			t.Fatalf("line %d width = %d, want %d", i, width, statusModalMaxWidth)
		}
	}
	plain := ansi.Strip(panel)
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Fatalf("panel missing rounded borders:\n%s", panel)
	}
	// Strip the frame and the 1-line vertical padding, then assert the last
	// two content rows are divider + footer with no intervening blanks.
	inner := innerStatusContent(t, panel)
	if len(inner) < 2 {
		t.Fatalf("panel content too short:\n%s", panel)
	}
	trimmedDivider := strings.TrimSpace(inner[len(inner)-2])
	if trimmedDivider == "" || strings.Trim(trimmedDivider, "─") != "" {
		t.Errorf("second-last content row should be the divider, got %q:\n%s", inner[len(inner)-2], panel)
	}
	if !strings.Contains(inner[len(inner)-1], "Esc / q: Close status modal") {
		t.Errorf("last content row should be the footer, got %q:\n%s", inner[len(inner)-1], panel)
	}
	for i, row := range inner {
		if strings.TrimSpace(row) == "" && i == len(inner)-1 {
			t.Errorf("trailing empty gap after footer:\n%s", panel)
		}
	}
	// Section spacing: exactly one blank line before each section header
	// except the first.
	for _, hdr := range []string{"SYMBOL ENGINE", "SESSION CONTEXT", "ENGINE & AUTHORITY"} {
		idx := -1
		for i, row := range inner {
			if strings.Contains(row, hdr) {
				idx = i
				break
			}
		}
		if idx < 1 {
			t.Errorf("missing section %q:\n%s", hdr, panel)
			continue
		}
		if strings.TrimSpace(inner[idx-1]) != "" {
			t.Errorf("section %q should be preceded by one blank line:\n%s", hdr, panel)
		}
	}
}

func innerStatusContent(t *testing.T, panel string) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(panel), "\n")
	if len(lines) < 4 {
		return nil
	}
	// Drop top/bottom rounded borders, then strip the side borders so each
	// row is the padded content width.
	content := make([]string, 0, len(lines)-2)
	for _, row := range lines[1 : len(lines)-1] {
		runes := []rune(row)
		if len(runes) >= 2 && runes[0] == '│' && runes[len(runes)-1] == '│' {
			runes = runes[1 : len(runes)-1]
		}
		content = append(content, string(runes))
	}
	// Drop the 1-line top/bottom internal padding.
	if len(content) >= 2 && strings.TrimSpace(content[0]) == "" {
		content = content[1:]
	}
	if len(content) >= 1 && strings.TrimSpace(content[len(content)-1]) == "" {
		content = content[:len(content)-1]
	}
	// Drop the 2-cell horizontal padding on each row for assertions.
	for i, row := range content {
		if len([]rune(row)) >= 4 {
			runes := []rune(row)
			content[i] = string(runes[2 : len(runes)-2])
		}
	}
	return content
}

func TestStatusViewResponsiveBounds(t *testing.T) {
	view := NewStatusView()
	updated, _ := view.Update(tea.WindowSizeMsg{Width: 72, Height: 24})
	resized, ok := updated.(StatusView)
	if !ok {
		t.Fatalf("status resize returned %T", updated)
	}
	if width, height := resized.Size(); width != 68 || height != 20 {
		t.Fatalf("resized status bounds = %dx%d, want 68x20", width, height)
	}
}
