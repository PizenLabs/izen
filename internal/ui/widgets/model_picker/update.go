package model_picker

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.UpdateModel(msg)
	return updated, cmd
}

// UpdateModel is the typed transition for hosts embedding the picker. Pure
// view: zero I/O. Search input filters the snapshot RAM slice synchronously;
// role hotkeys (d/p/s/v/a) emit modelapp.BindModelToRoleCommand via tea.Cmd
// for the app layer to persist; arrow keys cycle the provider-native
// reasoning options; "g" toggles local/global scope.
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case SnapshotMsg:
		if msg.Snap != nil {
			m = m.SetSnapshot(msg.Snap)
			m.loading = false
			m.err = nil
		}
		return m, nil
	case ModelsLoadedMsg:
		// Legacy RAM push from the parent (background sync result). Rebuild
		// the derived view without touching disk.
		m.snap = snapshotFromDescriptors(m.snap, msg.Models)
		m.refilter()
		m.resetReasoning()
		m.cursor = 0
		m.loading = false
		m.err = nil
		if m.query != "" || m.provider != "" {
			m.refilter()
		}
		return m, nil
	case ModelsErrMsg:
		m.loading = false
		m.err = msg.Err
		return m, nil
	case BindingSucceededMsg:
		if msg.Role != "" && msg.ModelID != "" {
			if m.roles == nil {
				m.roles = make(map[string]string)
			}
			m.roles[msg.Role] = msg.ModelID
			m.refreshBadges()
			m.status = fmt.Sprintf("bind %s → %s", msg.Role, msg.ModelID)
		}
		return m, nil
	case BindingFailedMsg:
		if msg.Err != nil {
			m.status = fmt.Sprintf("bind %s failed: %v", msg.Role, msg.Err)
			m.err = msg.Err
		}
		return m, nil
	case modelapp.BindModelToRoleCommand:
		// Echo path: the command bubbled to the parent and back (e.g. in
		// tests without an app layer). Apply optimistically to badges only;
		// persistence already happened (or is stubbed) upstream.
		if string(msg.Role) != "" && msg.ModelID != "" {
			if m.roles == nil {
				m.roles = make(map[string]string)
			}
			m.roles[string(msg.Role)] = msg.ModelID
			m.refreshBadges()
			m.status = fmt.Sprintf("bind %s → %s", string(msg.Role), msg.ModelID)
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		return m, nil
	}
}

// snapshotFromDescriptors rebuilds an immutable snapshot value from a pushed
// descriptor list, preserving providers/version metadata when present.
func snapshotFromDescriptors(prev *registry.ModelSnapshot, models []registry.ModelDescriptor) *registry.ModelSnapshot {
	out := &registry.ModelSnapshot{Models: append([]registry.ModelDescriptor(nil), models...)}
	if prev != nil {
		out.Providers = prev.Providers
		out.Version = prev.Version + 1
		out.UpdatedAt = prev.UpdatedAt
	}
	return out
}

// handleKey routes keystrokes by focus. Search focus: printable runes append
// to the query and re-filter RAM-side with zero cursor lag; backspace shrinks
// it; tab/esc moves focus to the list. List focus: up/down navigate, d/p/s/v/a
// queue role bindings via command emission, left/right cycle reasoning
// options, g toggles scope, tab or "/" returns to search, enter selects.
func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		return m.MoveCursor(-1), nil
	case tea.KeyDown:
		return m.MoveCursor(1), nil
	case tea.KeyLeft:
		return m.CycleReasoning(-1), nil
	case tea.KeyRight:
		return m.CycleReasoning(1), nil
	case tea.KeyEnter:
		return m.Select(), nil
	case tea.KeyBackspace:
		if m.searchFocused && len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.refilter()
			m.resetReasoning()
		}
		return m, nil
	case tea.KeyTab, tea.KeyEsc:
		m.searchFocused = !m.searchFocused
		return m, nil
	case tea.KeyRunes:
		return m.handleRunes(string(msg.Runes))
	default:
		return m, nil
	}
}

func (m Model) handleRunes(s string) (Model, tea.Cmd) {
	if s == "" {
		return m, nil
	}
	if !m.searchFocused {
		// List focus: single-char role hotkeys emit commands (no persistence).
		if len([]rune(s)) == 1 {
			switch s {
			case RoleDefaultKey:
				return m.QueueBind("default")
			case RolePlanKey:
				return m.QueueBind("plan")
			case RoleSmolKey:
				return m.QueueBind("smol")
			case RoleVisionKey:
				return m.QueueBind("vision")
			case RoleAdviserKey:
				return m.QueueBind("adviser")
			case ScopeToggleKey:
				m = m.ToggleScope()
				scope := "local"
				if m.isGlobal {
					scope = "global"
				}
				m.status = fmt.Sprintf("scope → %s", scope)
				return m, nil
			case "/":
				m.searchFocused = true
				return m, nil
			}
		}
		return m, nil
	}
	// Search focus: every rune filters RAM-side with no I/O.
	if s == "/" && m.query == "" {
		return m, nil
	}
	m.query += s
	m.refilter()
	m.resetReasoning()
	return m, nil
}
