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

// ── ROLE FALLBACK CHAIN EDITING ──────────────────────────────────────────────
//
// The fallback chain is a per-ROLE, per-TURN retry order: when the model that
// failed refuses for a network-transient reason, the runtime MAY retry the turn
// on the next model in the chain, and the switch is always reported in the
// trace. It is the only model-switch mechanism Izen has — nothing reverts
// implicitly.
//
// # WHY THE EDITING LIVES BEHIND Alt+F AND NOT A BARE KEY
//
// The models pane is also a fuzzy search field. Every printable rune typed in it
// narrows the list, which is the fastest thing a user does with this surface —
// so a bare `f` binding does not "add a fallback", it makes the word "fallback"
// impossible to type, and it does so silently: the list just empties. The same
// argument retires bare `a` and bare `r` on the list surface.
//
// So the chain editor is on a MODIFIER (Alt+F, or Ctrl+F) and, in the detail
// view, on the same key. A bare rune in the models pane is and stays search
// input. That is the entire reason the binding looks unusual: the ordinary key
// is the ordinary key.

// FallbackChainChangedMsg is emitted when the user CONFIRMS a fallback-chain
// edit with Enter. The widget stages edits locally; nothing is persisted until
// this message reaches the parent and the parent writes ~/.izen/config.yml.
//
// Staging is not a formality. config.Save rewrites the user's whole config file,
// and a user who has toggled four models into a chain and then pressed Esc has
// expressed a clear opinion: not that. One write, on one explicit keypress, is
// the only shape of this that is reversible by a second keypress.
type FallbackChainChangedMsg struct {
	// Role is the semantic role key whose chain changed (plan | default | ...).
	Role string
	// Chain is the complete ordered chain for that role AFTER the edit. It
	// replaces the stored chain wholesale; an empty slice means the chain was
	// cleared.
	Chain []string
	// Added reports whether the confirming keypress ADDED the highlighted model
	// (true) or removed it (false). It is carried for the trace line, because
	// "Fallback chain updated" is not a message a user can act on and
	// "Added ollama/llama3.2 to the default fallback chain (2 hops)" is.
	Added bool
	// Model is the model the edit was about, as a "provider/model" slug.
	Model string
}

// RoleParamsChangedMsg is emitted when the user CONFIRMS a role's operational
// parameters with Enter. It follows the same staged-not-persisted contract as
// FallbackChainChangedMsg, and for the same reason: the widget stages, the
// parent writes ~/.izen/config.yml.
//
// The message carries the COMPLETE parameter set rather than the field the user
// last touched. A partial update would leave the parent unable to distinguish
// "the user set the 5xx trigger off" from "the user set it off and left the
// other four at whatever they were a moment ago", and would make the persisted
// file depend on which key was pressed last.
type RoleParamsChangedMsg struct {
	// Role is the semantic role key whose parameters changed.
	Role string
	// Params is the role's complete, effective parameter block AFTER the edit.
	Params RoleParams
}
