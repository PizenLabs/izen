package model_picker

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.UpdateModel(msg)
	return updated, cmd
}

// UpdateModel is the typed transition for hosts embedding the picker. It
// never performs blocking I/O: search input calls Registry.Filter against the
// RAM slice, and role hotkeys persist a single small JSON file synchronously
// (a sub-millisecond local write, never network).
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case ModelsLoadedMsg:
		m.models = append([]registry.ModelDescriptor(nil), msg.Models...)
		m.filtered = append([]registry.ModelDescriptor(nil), msg.Models...)
		m.cursor = 0
		m.loading = false
		m.err = nil
		// Re-apply any query typed while loading.
		if m.query != "" || m.provider != "" {
			m.refilter()
		}
		return m, nil
	case ModelsErrMsg:
		m.loading = false
		m.err = msg.Err
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg), nil
	default:
		return m, nil
	}
}

// handleKey routes keystrokes by focus. Search focus: printable runes append
// to the query and re-filter RAM-side with zero cursor lag; backspace shrinks
// it; tab/esc moves focus to the list. List focus: up/down navigate, d/p/s
// bind default/plan/smol, tab or "/" returns to search, enter selects.
func (m Model) handleKey(msg tea.KeyMsg) Model {
	switch msg.Type {
	case tea.KeyUp:
		return m.MoveCursor(-1)
	case tea.KeyDown:
		return m.MoveCursor(1)
	case tea.KeyEnter:
		return m.Select()
	case tea.KeyBackspace:
		if m.searchFocused && len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.refilter()
		}
		return m
	case tea.KeyTab, tea.KeyEsc:
		m.searchFocused = !m.searchFocused
		return m
	case tea.KeyRunes:
		return m.handleRunes(string(msg.Runes))
	default:
		return m
	}
}

func (m Model) handleRunes(s string) Model {
	if s == "" {
		return m
	}
	if !m.searchFocused {
		// List focus: single-char role hotkeys.
		if len([]rune(s)) == 1 {
			switch s {
			case RoleDefaultKey:
				m, _ = m.BindHighlighted("default")
				return m
			case RolePlanKey:
				m, _ = m.BindHighlighted("plan")
				return m
			case RoleSmolKey:
				m, _ = m.BindHighlighted("smol")
				return m
			case "/":
				m.searchFocused = true
				return m
			}
		}
		return m
	}
	// Search focus: every rune filters RAM-side with no I/O.
	if s == "/" && m.query == "" {
		return m
	}
	m.query += s
	m.refilter()
	return m
}
