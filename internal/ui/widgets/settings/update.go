package settings

import tea "github.com/charmbracelet/bubbletea"

// Update implements tea.Model for the standalone settings modal.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		width := min(settingsModalMaxWidth, msg.Width-settingsWindowMargin)
		height := min(settingsModalMaxHeight, msg.Height-settingsWindowMargin)
		return m.SetSize(width, height), nil
	case CommitMsg:
		// Hosts may echo a successful commit back into the widget to restore
		// canonical normalization. It is not a second commit.
		m.values = normalizeValues(msg.Values)
		m.status = ""
		return m, nil
	case CloseMsg:
		m.closed = true
		return m, nil
	default:
		return m, nil
	}
}
