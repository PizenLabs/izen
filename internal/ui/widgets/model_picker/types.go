package model_picker

import tea "github.com/charmbracelet/bubbletea"

// PickerState is the two-step state machine controlling the registry UX.
type PickerState int

const (
	StateBrowsing PickerState = iota // 0: Searching & Browsing Registry
	StateDetail                      // 1: Inspecting Model & Activating
)

// InputFocus toggles keyboard routing between the search input and the table.
type InputFocus int

const (
	FocusSearch InputFocus = iota // 0: Search input focused
	FocusList                     // 1: Table navigation focused
)

// FocusScope is the legacy alias of InputFocus for backward compatibility.
type FocusScope = InputFocus

// PaneFocus selects the control surface focus scope.
type PaneFocus int

const (
	PaneProviders PaneFocus = iota // Left pane: provider selection
	PaneModels                     // Right pane: model selection
	PaneRoles                      // Left pane: role policy overrides
)

// ProviderState tracks activation status for the provider-centric surface.
type ProviderState struct {
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Configured bool   `json:"configured"`
	ModelCount int    `json:"model_count"`
}

// InvocationPolicy carries the reasoning policy for activation.
type InvocationPolicy struct {
	Reasoning string `json:"reasoning"`
}

// ModelAssignmentRequestedMsg is emitted when the picker activates a model.
type ModelAssignmentRequestedMsg struct {
	ModelID  string           `json:"model_id"`
	Provider string           `json:"provider"`
	Policy   InvocationPolicy `json:"policy"`
}

// CloseModalMsg signals the parent to close the modal (Esc with empty query).
type CloseModalMsg struct{}

// CloseModalCmd returns a tea.Cmd that emits CloseModalMsg.
func CloseModalCmd() tea.Cmd {
	return func() tea.Msg { return CloseModalMsg{} }
}

// ConfigureProviderMsg is emitted on Alt+A when focused on the providers
// pane. The parent should open a credential-entry overlay for the named
// provider.
type ConfigureProviderMsg struct {
	Provider string
}

// SaveProviderKeyMsg is emitted when the secure inline API-key overlay is
// submitted (Enter). The parent persists the key into the config store and
// immediately triggers dynamic model catalog discovery for that provider.
type SaveProviderKeyMsg struct {
	Provider string
	APIKey   string
}

// ApiKeyInputOpenedMsg announces that the secure inline API-key overlay is
// now active for a provider. The parent may use it to pause background
// activity; it carries no secrets.
type ApiKeyInputOpenedMsg struct {
	Provider string
}

// ApiKeyInputClosedMsg announces that the API-key overlay was dismissed
// (Esc) or submitted (Enter). No secrets travel through it.
type ApiKeyInputClosedMsg struct {
	Provider string
}

// Roles lists the top-level role policy overrides offered by the Roles pane.
// Each override maps onto an authority.ModelPolicy slot.
const (
	// RoleOverridePlan is the Plan/Thinking policy override (deep analysis
	// workflows: /plan, /investigate, /review). It drives ModelPolicy.Thinking.
	RoleOverridePlan = "plan"
	// RoleOverrideCommit is the Commit/Fast policy override (quick commit
	// messages/summaries: /commit). It drives ModelPolicy.Fast.
	RoleOverrideCommit = "commit"
)

// RolePolicyOverrideMsg is emitted from the Roles pane when the user binds
// the highlighted model to a top-level role policy override. The parent
// wires it onto authority.ModelPolicy (Thinking/Fast) in the runtime
// authority and persists the config change.
type RolePolicyOverrideMsg struct {
	Role     string // RoleOverridePlan | RoleOverrideCommit
	ModelID  string
	Provider string
	Effort   string // reasoning effort (low/medium/high/max); "" = default
}

// OverrideBinding is the picker-local read model of a role policy override
// (seeded by the parent from the persisted config). It is display-only in the
// widget; the parent owns all persistence.
type OverrideBinding struct {
	ModelID  string
	Provider string
	Effort   string // reasoning effort; "" = provider default
}
