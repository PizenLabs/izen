package settings

import tea "github.com/charmbracelet/bubbletea"

// The settings modal has a deliberately small, non-printing key contract.
//
// Tab / Shift+Tab move between horizontal preference panes. In the reduced
// one-row-per-pane schema, Up / Down move the focused row between panes as
// well. Space / Enter change the selected value. Esc closes the modal. In
// particular, j and k are intentionally absent: the host may
// forward printable runes to another surface, and a modal must never silently
// reinterpret them as navigation.
func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case " ":
		return m.changeActive()
	case "esc":
		m.closed = true
		return m, closeCmd()
	case "tab":
		m.activeTab = (m.activeTab + 1) % len(settingRowsByTab)
		m.activeRow = m.activeTab
		return m, nil
	case "shift+tab":
		m.activeTab = (m.activeTab - 1 + len(settingRowsByTab)) % len(settingRowsByTab)
		m.activeRow = m.activeTab
		return m, nil
	}

	switch msg.Type {
	case tea.KeyUp:
		m.activeTab = (m.activeTab - 1 + len(settingRowsByTab)) % len(settingRowsByTab)
		m.activeRow = m.activeTab
		return m, nil
	case tea.KeyDown:
		m.activeTab = (m.activeTab + 1) % len(settingRowsByTab)
		m.activeRow = m.activeTab
		return m, nil
	case tea.KeyEnter:
		return m.changeActive()
	case tea.KeySpace:
		return m.changeActive()
	}

	// Some terminals report a literal space as a rune rather than KeySpace.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ' {
		return m.changeActive()
	}

	// No printable rune is a settings navigation key. In particular, do not
	// bind j/k or forward arbitrary runes to a host text input.
	return m, nil
}

func (m Model) changeActive() (Model, tea.Cmd) {
	m.normalizeActive()
	switch m.activeTab {
	case 0:
		idx := indexOfStyle(m.values.ResponseStyle)
		m.values.ResponseStyle = ResponseStyles[(idx+1)%len(ResponseStyles)]
		return m, commitCmd(FieldResponseStyle, m.values)
	case 1:
		m.values.HideThinking = !m.values.HideThinking
		return m, commitCmd(FieldHideThinking, m.values)
	case 2:
		idx := indexOfAutoScroll(m.values.AutoScroll)
		m.values.AutoScroll = AutoScrollModes[(idx+1)%len(AutoScrollModes)]
		return m, commitCmd(FieldAutoScroll, m.values)
	default:
		return m, nil
	}
}

func indexOfStyle(style ResponseStyle) int {
	for i, candidate := range ResponseStyles {
		if candidate == style {
			return i
		}
	}
	return 0
}

func indexOfAutoScroll(mode AutoScrollMode) int {
	for i, candidate := range AutoScrollModes {
		if candidate == mode {
			return i
		}
	}
	return 0
}
