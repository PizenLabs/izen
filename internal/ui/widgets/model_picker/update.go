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
// role hotkeys (d/p/s/v/a) stamp a monotonic Seq, record saving... pending
// state, and emit modelapp.BindModelToRoleCommand via tea.Cmd for the app
// layer to persist; arrow keys cycle the provider-native reasoning options;
// "g" toggles local/global scope; Enter dispatches ActivateModelCommand;
// Ctrl+R emits SyncRequestedMsg. Badges mutate only on persistence-authority
// confirmation (BindingResultMsg / Succeeded / Failed with matching Seq).
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	if m.done {
		// Terminal state: still honor persistence confirmations so late
		// BindingResultMsg updates badges even after ACTIVATE.
		switch msg := msg.(type) {
		case modelapp.BindingResultMsg:
			if msg.Err != nil {
				m = m.applyBindFailure(msg.Role, msg.Seq, msg.Err)
			} else {
				m = m.applyBindSuccess(msg.Role, msg.ModelID, msg.Seq)
			}
			return m, nil
		case BindingSucceededMsg:
			m = m.applyBindSuccess(msg.Role, msg.ModelID, msg.Seq)
			return m, nil
		case BindingFailedMsg:
			if msg.Err != nil {
				m = m.applyBindFailure(msg.Role, msg.Seq, msg.Err)
			} else {
				m = m.applyBindFailure(msg.Role, msg.Seq, fmt.Errorf("bind %s failed", msg.Role))
			}
			return m, nil
		default:
			return m, nil
		}
	}
	switch msg := msg.(type) {
	case SnapshotMsg:
		if msg.Snap != nil {
			m = m.SetSnapshot(msg.Snap)
			m.loading = false
			m.err = nil
		}
		return m, nil
	case modelapp.RegistryUpdatedMsg:
		// Cross-layer alias of SnapshotMsg emitted by background workers
		// that import modelapp instead of the widget. Identical
		// re-hydration: pointer swap + re-filter + bounds clamp.
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
	case modelapp.BindingResultMsg:
		if msg.Err != nil {
			m = m.applyBindFailure(msg.Role, msg.Seq, msg.Err)
		} else {
			m = m.applyBindSuccess(msg.Role, msg.ModelID, msg.Seq)
		}
		return m, nil
	case BindingSucceededMsg:
		m = m.applyBindSuccess(msg.Role, msg.ModelID, msg.Seq)
		return m, nil
	case BindingFailedMsg:
		if msg.Err != nil {
			m = m.applyBindFailure(msg.Role, msg.Seq, msg.Err)
		} else {
			m = m.applyBindFailure(msg.Role, msg.Seq, fmt.Errorf("bind %s failed", msg.Role))
		}
		return m, nil
	case modelapp.BindModelToRoleCommand:
		// Echo path: the command bubbled to the parent and back (e.g. in
		// tests without an app layer). Treat as persistence confirmation
		// with the command's Seq so pending saving... resolves truthfully.
		if string(msg.Role) != "" && msg.ModelID != "" {
			m = m.applyBindSuccess(string(msg.Role), msg.ModelID, msg.Seq)
		}
		return m, nil
	case modelapp.SyncRequestedMsg:
		m.loading = true
		m.status = "syncing..."
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
// it; tab/esc moves focus to the list. List focus: up/down navigate (SELECT),
// d/p/s/v/a queue role bindings via Seq-stamped command emission (BIND),
// left/right cycle reasoning options, g toggles scope, tab or "/" returns to
// search, enter marks done and dispatches ActivateModelCommand (ACTIVATE),
// Ctrl+R requests background sync.
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
		m = m.Select()
		return m, m.EmitActivateCommand()
	case tea.KeyCtrlR:
		m.loading = true
		m.status = "syncing..."
		return m, func() tea.Msg { return modelapp.SyncRequestedMsg{} }
	case tea.KeyBackspace:
		if m.searchFocused && len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.refilter()
			m.resetReasoning()
		}
		return m, nil
	case tea.KeySpace:
		// Space in search focus appends to the multi-token query
		// (strings.Fields AND logic in MatchesQuery); it must never
		// trigger table selection or page scrolling.
		if m.searchFocused {
			m.query += " "
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
		// List focus: single-char role hotkeys emit Seq-stamped commands
		// (no persistence, no badge mutation until confirmation).
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
