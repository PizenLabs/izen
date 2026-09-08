package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/ui/diff"
)

// diffActive reports whether the unified diff viewer modal is open.
func (m *model) diffActive() bool { return m.diffView != nil }

// openDiffView parses raw unified diff text and opens the viewer modal.
// Parse errors are surfaced as an error activity line; an empty diff
// still opens (showing headers only) so the caller gets visual feedback.
func (m *model) openDiffView(raw, title string) {
	files, err := diff.ParseUnifiedDiff(raw)
	if err != nil {
		m.push(roleError, "diff parse error: "+err.Error())
		m.refreshViewportContent()
		return
	}
	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height
	if h <= 0 {
		h = 24
	}
	// Reserve chrome: header(2) + footer(2) + input(3) + margins.
	vh := h - 4
	if vh < 8 {
		vh = 8
	}
	dv := diff.NewModel(files, title, w-2, vh, diff.DefaultCollapseThreshold)
	m.diffView = &dv
}

// closeDiffView dismisses the diff viewer and returns focus to chat.
func (m *model) closeDiffView() {
	m.diffView = nil
	m.refreshViewportContent()
}

// handleDiffKey routes keybindings while the diff viewer is open.
// Returns true when the key was consumed.
func (m *model) handleDiffKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if m.diffView == nil {
		return false, nil
	}
	switch {
	case msg.Type == tea.KeyEscape || msg.String() == "q" || msg.String() == "Q":
		m.closeDiffView()
		return true, nil
	case msg.String() == "c" || msg.String() == "C":
		m.diffView.ToggleFocused()
		return true, nil
	case msg.String() == "a" || msg.String() == "A":
		m.diffView.ToggleAll()
		return true, nil
	case msg.Type == tea.KeyUp || msg.String() == "k":
		updated, cmd := m.diffView.Update(msg)
		*m.diffView = updated
		return true, cmd
	case msg.Type == tea.KeyDown || msg.String() == "j":
		updated, cmd := m.diffView.Update(msg)
		*m.diffView = updated
		return true, cmd
	case msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown ||
		msg.Type == tea.KeyHome || msg.Type == tea.KeyEnd:
		updated, cmd := m.diffView.Update(msg)
		*m.diffView = updated
		return true, cmd
	}
	return false, nil
}

// renderDiffOverlay projects the diff viewer full-screen over the workspace.
func (m *model) renderDiffOverlay() string {
	if m.diffView == nil {
		return ""
	}
	w := m.width
	if w < 20 {
		w = 80
	}
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(0, 1).
		Width(w - 4)
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Render(
		"diff view · j/k or ↑/↓ scroll · c fold/unfold hunk · a fold all · q/Esc close")
	body := strings.Join([]string{m.diffView.View(), hint}, "\n")
	return border.Render(body)
}
