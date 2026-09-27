package model_picker

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
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

// ── FALLBACK CHAIN TOGGLING ──────────────────────────────────────────────────

// toggleFallbackChain stages an add/remove of the highlighted model in the
// current role's chain and reports what happened in the status line.
//
// It stages; it does not persist. The confirming Enter emits
// FallbackChainChangedMsg and the parent writes ~/.izen/config.yml, so a user
// who toggles four models and then presses Esc has not silently rewritten their
// config file.
//
// It NEVER falls through to the search input. That is the property the whole
// binding exists for, and it is why this is a named case in the switch above
// rather than a branch of the search handler: a key that is not handled here
// reaches handleSearchInput, and there it becomes a query mutation. The list
// filtering is the most-used thing on this surface; a feature key that quietly
// feeds it is a feature nobody can reach.
func (m Model) toggleFallbackChain() (Model, tea.Cmd) {
	sel := m.Highlighted()
	if sel == nil {
		sel = m.SelectedModel()
	}
	if sel == nil {
		m.status = "No model highlighted"
		return m, nil
	}
	roleKey := m.effectiveFallbackRole()
	slug := fallbackSlug(*sel)
	updated, added := m.ToggleFallbackForHighlighted()
	if updated.fallbackRole != roleKey {
		updated.fallbackRole = roleKey
	}
	updated.status = describeFallbackToggle(roleKey, slug, added)
	return updated, nil
}

// describeFallbackToggle is the one-line status the user reads immediately after
// pressing Alt+F.
//
// It names the ROLE, not just the model, because the role is the part a user is
// most likely to have wrong: the same key on the same model edits different
// chains depending on which pane they were on, and a message that said only
// "added llama3.2" would leave a user who meant the plan chain with no way to
// tell that they edited the default one.
func describeFallbackToggle(roleKey, slug string, added bool) string {
	verb := "Removed"
	if added {
		verb = "Added"
	}
	return fmt.Sprintf("%s %s from the %s fallback chain — Enter to save, Esc to discard", verb, slug, roleKey)
}

func (m Model) handleBrowsingKeys(msg tea.KeyMsg) (Model, tea.Cmd) {
	// While the secure inline API-key overlay is open, every browsing key
	// routes to the textinput (EchoPassword). Esc cancels, Enter submits.
	if m.apiKeyInput != nil {
		return m.handleApiKeyInput(msg)
	}
	k := msg.String()

	switch k {
	case "tab":
		m.cyclePane()
		return m, nil
	case "up":
		switch m.paneFocus {
		case PaneProviders:
			m.moveProviderCursor(-1)
		case PaneRoles:
			m.moveRoleCursor(-1)
		default:
			m.moveCursor(-1)
		}
		return m, nil
	case "down":
		switch m.paneFocus {
		case PaneProviders:
			m.moveProviderCursor(1)
		case PaneRoles:
			m.moveRoleCursor(1)
		default:
			m.moveCursor(1)
		}
		return m, nil
	case "ctrl+n":
		// Keep non-arrow navigation available in list focus, but never let a
		// search-focused picker consume it as a filter mutation or row move.
		if m.searchInputActive() {
			return m, nil
		}
		switch m.paneFocus {
		case PaneProviders:
			m.moveProviderCursor(1)
		case PaneRoles:
			m.moveRoleCursor(1)
		default:
			m.moveCursor(1)
		}
		return m, nil
	case "j", "k":
		if m.searchInputActive() {
			if m.paneFocus == PaneProviders {
				m.paneFocus = PaneModels
			}
			return m.handleSearchInput(msg)
		}
		if k == "j" {
			switch m.paneFocus {
			case PaneProviders:
				m.moveProviderCursor(1)
			case PaneRoles:
				m.moveRoleCursor(1)
			default:
				m.moveCursor(1)
			}
		} else {
			switch m.paneFocus {
			case PaneProviders:
				m.moveProviderCursor(-1)
			case PaneRoles:
				m.moveRoleCursor(-1)
			default:
				m.moveCursor(-1)
			}
		}
		return m, nil
	case "pgup":
		if m.searchInputActive() {
			return m, nil
		}
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-10)
			if budget <= 0 {
				budget = 5
			}
		}
		switch m.paneFocus {
		case PaneModels:
			m.moveCursor(-budget)
		case PaneRoles:
			m.moveRoleCursor(-1)
		default:
			m.moveProviderCursor(-budget)
		}
		return m, nil
	case "pgdown":
		if m.searchInputActive() {
			return m, nil
		}
		budget := m.listRowBudget
		if budget <= 0 {
			budget = max(5, m.innerHeight-10)
			if budget <= 0 {
				budget = 5
			}
		}
		switch m.paneFocus {
		case PaneModels:
			m.moveCursor(budget)
		case PaneRoles:
			m.moveRoleCursor(1)
		default:
			m.moveProviderCursor(budget)
		}
		return m, nil
	case "enter":
		// A STAGED FALLBACK-CHAIN EDIT IS CONFIRMED BEFORE ANYTHING ELSE. Enter
		// on this surface is ambiguous by construction — it activates a model,
		// and it writes the config file — and resolving it in favour of the
		// unsaved edit is the only order that is not data loss: activating a
		// model closes the modal, and a closed modal cannot confirm anything.
		// The alternative (activate, and silently drop the chain) means a user
		// who pressed Enter to save four staged models gets a model switch
		// instead.
		if m.fallbackDirty {
			return m, m.EmitFallbackChainConfirm()
		}
		switch m.paneFocus {
		case PaneRoles:
			// Choose the highlighted role override; switch to the models
			// pane to pick the binding model.
			m.paneFocus = PaneModels
			return m, nil
		case PaneProviders:
			// All models / configured provider: jump to the models pane.
			if m.isAllModelsSelected() || m.isProviderConfigured(m.highlightedProvider()) {
				m.paneFocus = PaneModels
				return m, nil
			}
			// Unconfigured provider: open the secure API-key overlay.
			return m.openApiKeyInput(m.highlightedProvider())
		case PaneModels:
			// Roles-target assignment: bind highlighted model to the role.
			if m.showingRoles {
				return m.emitRoleOverride()
			}
			// 2-step activation: Enter opens Model Details / Variant
			// configuration instead of activating directly. Confirmation
			// happens in StateDetail via activateModelWithVariantCmd.
			if sel := m.SelectedModel(); sel != nil {
				m.pinDetail()
				m.state = StateDetail
				m = m.FocusList()
				return m, nil
			}
		}
		return m, nil
	case FallbackToggleKey, FallbackToggleKeyAlt, FallbackToggleKeyCtrl:
		// THE CONFLICT-FREE BINDING. A modifier is not a printable rune in any
		// terminal's default mode, so it cannot land in the fuzzy search field
		// — which is the whole point: on this surface the bare runes belong to
		// the search box, always.
		return m.toggleFallbackChain()
	case "alt+i", "alt+I":
		// Inspect: pin the highlighted model into StateDetail. Enables
		// detail view + reasoning policy cycling without committing.
		// Any browsing focus enters detail for the highlighted model.
		if sel := m.SelectedModel(); sel != nil {
			m.pinDetail()
			m.state = StateDetail
			m = m.FocusList()
		}
		return m, nil
	case "esc":
		return m, CloseModalCmd()
	}

	// Enter or Alt+A on the providers pane: open the secure API-key overlay
	// for the highlighted provider (masks input with EchoPassword, emits
	// SaveProviderKeyMsg on submit). Supersedes the previous ConfigureProviderMsg.
	if (k == "enter" || k == "alt+a") && m.paneFocus == PaneProviders {
		prov := m.highlightedProvider()
		if prov != "" && !m.isAllModelsSelected() {
			return m.openApiKeyInput(prov)
		}
		return m, nil
	}

	// Ctrl+R: background sync.
	if msg.Type == tea.KeyCtrlR {
		m.loading = true
		m.status = "syncing..."
		return m, func() tea.Msg { return modelapp.SyncRequestedMsg{} }
	}

	// Typing: auto-switch to models pane and filter. Non-printable control
	// keys (including the globally-owned control shortcut) must not mutate
	// pane focus.
	if m.paneFocus == PaneProviders && isSearchEditKey(msg) {
		m.paneFocus = PaneModels
	}
	return m.handleSearchInput(msg)
}

// cyclePane advances the pane focus Providers -> Models -> Roles -> Providers,
// toggling the Roles surface on the left pane on entry/exit.
func (m *Model) cyclePane() {
	switch m.paneFocus {
	case PaneProviders:
		m.paneFocus = PaneModels
	case PaneModels:
		m.paneFocus = PaneRoles
		m.showingRoles = true
	case PaneRoles:
		m.paneFocus = PaneProviders
		m.showingRoles = false
	}
}

// openApiKeyInput opens the secure inline API-key overlay for a provider,
// announcing it via ApiKeyInputOpenedMsg. Zero secrets cross the message
// boundary until SaveProviderKeyMsg is submitted by the user on Enter.
func (m Model) openApiKeyInput(provider string) (Model, tea.Cmd) {
	if provider == "" {
		return m, nil
	}
	in := textinput.New()
	in.Placeholder = "sk-..."
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	in.CharLimit = 256
	in.Focus()
	in.Width = max(16, m.innerWidth-16)
	m.apiKeyInput = &in
	m.apiKeyProvider = provider
	return m, func() tea.Msg { return ApiKeyInputOpenedMsg{Provider: provider} }
}

// handleApiKeyInput routes keys while the API-key overlay is open.
func (m Model) handleApiKeyInput(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		prov := m.apiKeyProvider
		m.apiKeyInput = nil
		return m, func() tea.Msg { return ApiKeyInputClosedMsg{Provider: prov} }
	case tea.KeyEnter:
		key := strings.TrimSpace(m.apiKeyInput.Value())
		if key == "" {
			m.status = "API key cannot be empty"
			return m, nil
		}
		prov := m.apiKeyProvider
		m.apiKeyInput = nil
		return m, func() tea.Msg { return SaveProviderKeyMsg{Provider: prov, APIKey: key} }
	default:
		in := *m.apiKeyInput
		updated, cmd := in.Update(msg)
		m.apiKeyInput = &updated
		return m, cmd
	}
}

// emitRoleOverride emits RolePolicyOverrideMsg binding the highlighted model
// to the highlighted top-level role, carrying the active reasoning effort.
func (m Model) emitRoleOverride() (Model, tea.Cmd) {
	sel := m.SelectedModel()
	if sel == nil {
		return m, nil
	}
	role := m.HighlightedRole()
	effort := ""
	if opt, ok := m.CurrentReasoningOption(); ok && opt != DefaultReasoningOption {
		effort = opt
	}
	return m, func() tea.Msg {
		return RolePolicyOverrideMsg{Role: role, ModelID: sel.ID, Provider: sel.Provider, Effort: effort}
	}
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
		// Esc from detail returns to browsing (back-stack). Do NOT emit
		// CloseModalCmd — the overlay stays open in browsing mode.
		m.state = StateBrowsing
		m.clearDetail()
		return m, nil
	case ReasoningCycleKey, ReasoningCycleKeyAlt:
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
	case FallbackToggleKey, FallbackToggleKeyAlt, FallbackToggleKeyCtrl:
		// The chain editor is reachable from the detail view too, on the same
		// modifier as in the list — a user who is already inspecting a model is
		// exactly the user who wants to add it to a fallback chain, and making
		// them go back out to do it would make the feature discoverable only by
		// people who already know it exists.
		return m.toggleFallbackChain()
	case "enter":
		// A staged chain edit is confirmed before the activation, for the same
		// reason as in the list view: activating closes the modal, and a closed
		// modal cannot save anything.
		if m.fallbackDirty {
			return m, m.EmitFallbackChainConfirm()
		}
		// 2-step activation confirm: commit the pinned detail selection
		// with its selected reasoning variant into the runtime authority.
		// The parent closes the modal after the commit lands.
		if sel := m.SelectedModel(); sel != nil {
			variant, _ := m.CurrentReasoningOption()
			assign := m.activateModelWithVariantCmd(sel, variant)
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

// isSearchEditKey reports whether a browsing key is an input mutation that
// should move focus from the provider pane to the model search field. Control
// keys (including the workspace-owned Ctrl+P) are deliberately excluded.
func isSearchEditKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace, tea.KeyBackspace:
		return true
	default:
		return false
	}
}

// isListHotkey reports whether s is an explicit FocusList keybinding that
// must NOT be forwarded to the search input.
//
//nolint:unused // retained for spec compatibility
func isListHotkey(s string) bool {
	switch s {
	case RoleDefaultKey, RolePlanKey, RoleSmolKey, RoleVisionKey, RoleAdviserKey,
		FallbackToggleKey, FallbackToggleKeyAlt, FallbackToggleKeyCtrl,
		ReasoningCycleKey, ReasoningCycleKeyAlt:
		return true
	}
	return false
}
