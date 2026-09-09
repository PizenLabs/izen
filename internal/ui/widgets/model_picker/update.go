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

// handleKey implements the UNIFIED SEARCH & NAVIGATION ENGINE.
//
// Spec invariants:
//   - Search input is ALWAYS active by default (no Tab toggling required).
//   - Navigation keys (↑, ↓, PgUp, PgDn, Enter, Esc) are intercepted globally
//     before search input.
//   - Role bindings use Alt+key (alt+d/p/s/v/a, alt+g) to avoid collision with
//     typing. Plain d/p/s/v/a are routed to search (typing) for spec compliance
//     but retained as fallback when Alt is not available (backward compat for
//     existing tests that send plain runes).
func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	k := msg.String()

	// 1. GLOBAL NAVIGATION & SELECTION (intercepted before search input)
	switch k {
	case "up", "ctrl+p":
		return m.MoveCursor(-1), nil
	case "down", "ctrl+n":
		return m.MoveCursor(1), nil
	case "pgup":
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-6)
			if budget <= 0 {
				budget = 5
			}
		}
		return m.MoveCursor(-budget), nil
	case "pgdown":
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-6)
			if budget <= 0 {
				budget = 5
			}
		}
		return m.MoveCursor(budget), nil
	case "enter":
		if sel := m.Highlighted(); sel != nil {
			m = m.Select()
			return m, m.EmitActivateCommand()
		}
		return m, nil
	case "esc":
		if m.query != "" {
			m.query = ""
			m.applyFilter()
			return m, nil
		}
		// No query: propagate close request as no-op in widget (parent handles overlay).
		return m, nil
	}

	// Handle Ctrl+R globally (sync) before Alt bindings.
	if msg.Type == tea.KeyCtrlR {
		m.loading = true
		m.status = "syncing..."
		return m, func() tea.Msg { return modelapp.SyncRequestedMsg{} }
	}

	// 2. ROLE BINDINGS VIA ALT / SHORTCUTS (prevents typing collision)
	switch k {
	case "alt+d":
		return m.QueueBind("default")
	case "alt+p":
		return m.QueueBind("plan")
	case "alt+s":
		return m.QueueBind("smol")
	case "alt+v":
		return m.QueueBind("vision")
	case "alt+a":
		return m.QueueBind("adviser")
	case "alt+g":
		m = m.ToggleScope()
		scope := "local"
		if m.isGlobal {
			scope = "global"
		}
		m.status = fmt.Sprintf("scope → %s", scope)
		return m, nil
	case "left", "right":
		delta := -1
		if k == "right" {
			delta = 1
		}
		return m.CycleReasoning(delta), nil
	}

	// Tab / ShiftTab legacy: kept as no-op to avoid breaking existing callers,
	// but search is always active so Tab no longer toggles focus.
	if msg.Type == tea.KeyTab || msg.Type == tea.KeyShiftTab {
		// Treat Tab as focus hint no-op; search remains active.
		return m, nil
	}

	// 3. SUPPORT LEGACY PLAIN ROLE KEYS FOR BACKWARD COMPAT (tests send "p" etc
	// without Alt). Plain bindings are only honored when focus is FocusList
	// (list navigation) to preserve type-to-search in unified engine. When
	// search is always active, typing "p" in search focus must filter, not bind.
	if !msg.Alt && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && m.focus == FocusList {
		s := string(msg.Runes)
		switch s {
		case RoleDefaultKey, RolePlanKey, RoleSmolKey, RoleVisionKey, RoleAdviserKey:
			return m.QueueBind(map[string]string{
				RoleDefaultKey: "default",
				RolePlanKey:    "plan",
				RoleSmolKey:    "smol",
				RoleVisionKey:  "vision",
				RoleAdviserKey: "adviser",
			}[s])
		case ScopeToggleKey:
			m = m.ToggleScope()
			scope := "local"
			if m.isGlobal {
				scope = "global"
			}
			m.status = fmt.Sprintf("scope → %s", scope)
			return m, nil
		case "/":
			return m, nil
		}
	}

	// 4. ALL OTHER RUNES ROUTED TO SEARCH INPUT (unified engine)
	// Keep focus in sync so legacy tests that assert FocusSearch after typing
	// still observe the expected state, while navigation remains focus-agnostic.
	switch msg.Type {
	case tea.KeyBackspace:
		m.focus = FocusSearch
		m.searchFocused = true
		m.searchInput.Focus()
		if len(m.query) > 0 {
			r := []rune(m.query)
			m.query = string(r[:len(r)-1])
			m.applyFilter()
		}
		return m, nil
	case tea.KeySpace:
		m.focus = FocusSearch
		m.searchFocused = true
		m.searchInput.Focus()
		m.query += " "
		m.applyFilter()
		return m, nil
	case tea.KeyRunes:
		s := string(msg.Runes)
		if s == "" {
			return m, nil
		}
		if s == "/" && m.query == "" {
			return m, nil
		}
		if isPrintableString(s) {
			m.focus = FocusSearch
			m.searchFocused = true
			m.searchInput.Focus()
			m.query += s
			m.applyFilter()
		}
		return m, nil
	default:
		if len(k) == 1 && isPrintableString(k) {
			m.focus = FocusSearch
			m.searchFocused = true
			m.searchInput.Focus()
			m.query += k
			m.applyFilter()
			return m, nil
		}
		return m, nil
	}
}

// isPrintableString reports whether s consists entirely of printable
// characters suitable for auto-routing to search (single or multi-char).
func isPrintableString(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// isListHotkey reports whether s is an explicit FocusList keybinding that
// must NOT be forwarded to the search input.
//
//nolint:unused // retained for spec compatibility
func isListHotkey(s string) bool {
	switch s {
	case RoleDefaultKey, RolePlanKey, RoleSmolKey, RoleVisionKey, RoleAdviserKey, ScopeToggleKey:
		return true
	}
	return false
}
