package model_picker

import (
	"fmt"
	"strings"

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
		if string(msg.Role) != "" && msg.ModelID != "" {
			m = m.applyBindSuccess(string(msg.Role), msg.ModelID, msg.Seq)
		}
		return m, nil
	case ModelAssignmentRequestedMsg:
		// Echo: treat as confirmation for legacy tests (update roles)
		if string(msg.Target) != "" && msg.ModelID != "" {
			m = m.applyBindSuccess(string(msg.Target), msg.ModelID, 0)
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
		if m.focus == FocusSearch {
			m.focus = FocusList
			m.searchFocused = false
			m.searchInput.Blur()
		} else {
			m.focus = FocusSearch
			m.searchFocused = true
			m.searchInput.Focus()
		}
		return m, nil
	case "up", "ctrl+p", "k":
		m.moveCursor(-1)
		return m, nil
	case "down", "ctrl+n", "j":
		m.moveCursor(1)
		return m, nil
	case "pgup":
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-6)
			if budget <= 0 {
				budget = 5
			}
		}
		m.moveCursor(-budget)
		return m, nil
	case "pgdown":
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-6)
			if budget <= 0 {
				budget = 5
			}
		}
		m.moveCursor(budget)
		return m, nil
	case "enter":
		if sel := m.SelectedModel(); sel != nil {
			m.state = StateDetail
			m.targetCursor = m.getInitialTargetIndex()
		}
		return m, nil
	case "a", "shift+enter":
		if sel := m.SelectedModel(); sel != nil {
			return m, m.emitAssignmentCmd(sel, m.activeWorkspace)
		}
		return m, nil
	case "esc":
		if m.searchInput.Value() != "" || m.query != "" {
			m.clearSearch()
			return m, nil
		}
		return m, CloseModalCmd()
	}

	// Handle Ctrl+R globally (sync)
	if msg.Type == tea.KeyCtrlR {
		m.loading = true
		m.status = "syncing..."
		return m, func() tea.Msg { return modelapp.SyncRequestedMsg{} }
	}

	// Also handle Shift+Enter via type check (some terminals report as ctrl+enter)
	// Fallback: if msg.String() contains shift and enter, treat as fast path
	if k == "shift+enter" || (msg.Type == tea.KeyEnter && strings.Contains(strings.ToLower(k), "shift")) {
		if sel := m.SelectedModel(); sel != nil {
			return m, m.emitAssignmentCmd(sel, m.activeWorkspace)
		}
		return m, nil
	}

	// Direct input handling when FocusSearch is active
	if m.focus == FocusSearch {
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		m.query = m.searchInput.Value()
		m.applyFilter()
		return m, cmd
	}

	// When FocusList, printable runes should switch to Search and filter
	// (keeps legacy tests and typing fluid while still supporting Tab toggle)
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
		return m, nil
	case "up", "k":
		m.targetCursor = max(0, m.targetCursor-1)
		return m, nil
	case "down", "j":
		m.targetCursor = min(len(AllWorkspaceTargets)-1, m.targetCursor+1)
		return m, nil
	case "1", "2", "3", "4", "5":
		idx := int(k[0] - '1')
		if idx >= 0 && idx < len(AllWorkspaceTargets) {
			m.targetCursor = idx
			selectedTarget := AllWorkspaceTargets[m.targetCursor]
			sel := m.SelectedModel()
			if sel != nil {
				return m, m.emitAssignmentCmd(sel, selectedTarget)
			}
		}
		return m, nil
	case "r":
		m.cycleReasoningPolicy()
		return m, nil
	case "enter":
		if sel := m.SelectedModel(); sel != nil {
			selectedTarget := AllWorkspaceTargets[m.targetCursor]
			return m, m.emitAssignmentCmd(sel, selectedTarget)
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
