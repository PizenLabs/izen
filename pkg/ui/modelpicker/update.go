package modelpicker

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Init implements tea.Model. The picker schedules no background command.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model. Use UpdateModel to keep a concrete Model when
// embedding the component in a parent Bubble Tea model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.UpdateModel(msg)
	return updated, cmd
}

// UpdateModel is the typed variant of Update for hosts that embed the
// component and need a concrete Model back.
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg), nil
	default:
		return m, nil
	}
}

// handleKey routes a keystroke. Effort navigation is only honored when the
// highlighted model exposes effort options.
func (m Model) handleKey(msg tea.KeyMsg) Model {
	switch msg.Type {
	case tea.KeyUp, tea.KeyShiftUp:
		return m.MoveCursor(-1)
	case tea.KeyDown, tea.KeyShiftDown:
		return m.MoveCursor(1)
	case tea.KeyLeft, tea.KeyShiftLeft:
		return m.MoveEffort(-1)
	case tea.KeyRight, tea.KeyShiftRight:
		return m.MoveEffort(1)
	case tea.KeyEnter:
		m, _, _ = m.Select()
		return m
	case tea.KeyRunes:
		if !m.done {
			m = m.SetFilter(m.filter + string(msg.Runes))
		}
		return m
	case tea.KeyBackspace:
		if len(m.filter) > 0 {
			m = m.SetFilter(m.filter[:len(m.filter)-1])
		}
		return m
	default:
		return m
	}
}
