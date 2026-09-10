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
