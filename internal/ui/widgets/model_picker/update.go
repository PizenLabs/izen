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

// UpdateModel is the typed transition for hosts embedding the picker.
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	if m.done {
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
		case ModelAssignmentRequestedMsg:
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
		if msg.Snap != nil {
			m = m.SetSnapshot(msg.Snap)
			m.loading = false
			m.err = nil
		}
		return m, nil
	case ModelsLoadedMsg:
		m.snap = snapshotFromDescriptors(m.snap, msg.Models)
		m.refilter()
		m.resetReasoning()
		if m.state != StateDetail {
			// Anchor fresh loads at the top only while browsing. In
			// StateDetail the pinned selection owns truth; the cursor
			// must not be yanked to index 0 under the user.
			m.cursor = 0
		}
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
		if string(msg.Role) != "" && msg.ModelID != "" {
			m = m.applyBindSuccess(string(msg.Role), msg.ModelID, msg.Seq)
		}
		return m, nil
	case ModelAssignmentRequestedMsg:
		// Echo activation (target removed per Phase 3 control surface redesign).
		if msg.ModelID != "" {
			m.activatedModelID = msg.ModelID
			m.activatedProvider = msg.Provider
		}
		return m, nil
	case modelapp.SyncRequestedMsg:
		m.loading = true
		m.status = "syncing..."
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Also update inner geometry via SetSize
		m = m.SetSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		if m.state == StateDetail {
			return m.handleDetailKeys(msg)
		}
		return m.handleBrowsingKeys(msg)
	default:
		return m, nil
	}
}

func snapshotFromDescriptors(prev *registry.ModelSnapshot, models []registry.ModelDescriptor) *registry.ModelSnapshot {
	out := &registry.ModelSnapshot{Models: append([]registry.ModelDescriptor(nil), models...)}
	if prev != nil {
		out.Providers = prev.Providers
		out.Version = prev.Version + 1
		out.UpdatedAt = prev.UpdatedAt
	}
	return out
}

func (m Model) handleBrowsingKeys(msg tea.KeyMsg) (Model, tea.Cmd) {
	k := msg.String()

	switch k {
	case "tab":
		if m.paneFocus == PaneProviders {
			m.paneFocus = PaneModels
		} else {
			m.paneFocus = PaneProviders
		}
		return m, nil
	case "up", "ctrl+p", "k":
		if m.paneFocus == PaneProviders {
			m.moveProviderCursor(-1)
		} else {
			m.moveCursor(-1)
		}
		return m, nil
	case "down", "ctrl+n", "j":
		if m.paneFocus == PaneProviders {
			m.moveProviderCursor(1)
		} else {
			m.moveCursor(1)
		}
		return m, nil
	case "pgup":
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-6)
			if budget <= 0 {
				budget = 5
			}
		}
		if m.paneFocus == PaneModels {
			m.moveCursor(-budget)
		} else {
			m.moveProviderCursor(-budget)
		}
		return m, nil
	case "pgdown":
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-6)
			if budget <= 0 {
				budget = 5
			}
		}
		if m.paneFocus == PaneModels {
			m.moveCursor(budget)
		} else {
			m.moveProviderCursor(budget)
		}
		return m, nil
	case "enter":
		// Enter commits the highlighted model and closes the overlay.
		// Only active in the models pane.
		if m.paneFocus == PaneModels {
			if sel := m.SelectedModel(); sel != nil {
				m = m.Select()
				assign := m.emitAssignmentCmd(sel, "")
				if assign == nil {
					return m, nil
				}
				return m, assign
			}
		}
		return m, nil
	case "i":
		// Inspect: pin the highlighted model into StateDetail. Enables
		// detail view + reasoning policy cycling without committing.
		// Any browsing focus enters detail for the highlighted model.
		if sel := m.SelectedModel(); sel != nil {
			m.pinDetail()
			m.state = StateDetail
			m.focus = FocusList
			m.searchInput.Blur()
		}
		return m, nil
	case "esc":
		return m, CloseModalCmd()
	}

	// Alt+A: credential entry for the highlighted provider.
	if k == "alt+a" && m.paneFocus == PaneProviders {
		prov := m.highlightedProvider()
		if prov != "" {
			return m, func() tea.Msg { return ConfigureProviderMsg{Provider: prov} }
		}
		return m, nil
	}

	// Ctrl+R: background sync.
	if msg.Type == tea.KeyCtrlR {
		m.loading = true
		m.status = "syncing..."
		return m, func() tea.Msg { return modelapp.SyncRequestedMsg{} }
	}

	// Typing: auto-switch to models pane and filter.
	if m.paneFocus == PaneProviders {
		m.paneFocus = PaneModels
	}
	return m.handleSearchInput(msg)
}

// handleSearchInput processes printable runes, backspace, and space as
// search query mutations when the models pane has focus.
func (m Model) handleSearchInput(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyBackspace:
		m.focus = FocusSearch
		m.searchFocused = true
		m.searchInput.Focus()
		if len(m.query) > 0 {
			r := []rune(m.query)
			m.query = string(r[:len(r)-1])
			m.searchInput.SetValue(m.query)
			m.applyFilter()
		}
		return m, nil
	case tea.KeySpace:
		m.focus = FocusSearch
		m.searchFocused = true
		m.searchInput.Focus()
		m.query += " "
		m.searchInput.SetValue(m.query)
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
			m.searchInput.SetValue(m.query)
			m.applyFilter()
		}
		return m, nil
	default:
		k := msg.String()
		if len(k) == 1 && isPrintableString(k) {
			m.focus = FocusSearch
			m.searchFocused = true
			m.searchInput.Focus()
			m.query += k
			m.searchInput.SetValue(m.query)
			m.applyFilter()
			return m, nil
		}
		return m, nil
	}
}

func (m Model) handleDetailKeys(msg tea.KeyMsg) (Model, tea.Cmd) {
	k := msg.String()

	switch k {
	case "esc":
		m.state = StateBrowsing
		m.clearDetail()
		return m, nil
	case "r":
		// Dynamic capability guard: ONLY cycle through caps.Options when the
		// model actually supports configurable reasoning. Non-configurable
		// models are a NO-OP with zero state change.
		sel := m.SelectedModel()
		if sel == nil {
			return m, nil
		}
		caps := sel.GetReasoningCapability()
		if !caps.Supported || !caps.Configurable || len(caps.Options) == 0 {
			return m, nil // Guard: NO-OP for non-configurable models
		}
		// Cycle strictly through valid model options
		m.cycleReasoningPolicy()
		return m, nil
	case "enter":
		// Strict single model activation (Phase 3): commit active binding
		// directly to runtime ModelState without workspace sub-menu.
		if sel := m.SelectedModel(); sel != nil {
			assign := m.emitAssignmentCmd(sel, "")
			if assign == nil {
				return m, nil
			}
			return m, assign
		}
		return m, nil
	}

	// Allow Ctrl+R in detail as well
	if msg.Type == tea.KeyCtrlR {
		m.loading = true
		m.status = "syncing..."
		return m, func() tea.Msg { return modelapp.SyncRequestedMsg{} }
	}

	return m, nil
}

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
