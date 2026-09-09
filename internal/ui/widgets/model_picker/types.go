package model_picker

import tea "github.com/charmbracelet/bubbletea"

// PickerState is the two-step state machine controlling the registry UX.
type PickerState int

const (
	StateBrowsing PickerState = iota // 0: Searching & Browsing Registry
	StateDetail                      // 1: Inspecting Model & Assigning Targets
)

// InputFocus toggles keyboard routing between the search input and the table.
type InputFocus int

const (
	FocusSearch InputFocus = iota // 0: Search input focused
	FocusList                     // 1: Table navigation focused
)

// FocusScope is the legacy alias of InputFocus for backward compatibility.
type FocusScope = InputFocus

// WorkspaceTarget is the workspace assignment drawer target.
type WorkspaceTarget string

const (
	TargetAsk         WorkspaceTarget = "ask"
	TargetInvestigate WorkspaceTarget = "investigate"
	TargetPlan        WorkspaceTarget = "plan"
	TargetBuild       WorkspaceTarget = "build"
	TargetReview      WorkspaceTarget = "review"
)

// AllWorkspaceTargets is the ordered set of assignable workspace targets.
var AllWorkspaceTargets = []WorkspaceTarget{
	TargetAsk,
	TargetInvestigate,
	TargetPlan,
	TargetBuild,
	TargetReview,
}

// InvocationPolicy carries the reasoning policy for an assignment.
type InvocationPolicy struct {
	Reasoning string `json:"reasoning"`
}

// ModelAssignmentRequestedMsg is emitted when the picker requests a model
// assignment to a workspace target (fast-path or detail confirmation).
type ModelAssignmentRequestedMsg struct {
	ModelID  string           `json:"model_id"`
	Provider string           `json:"provider"`
	Target   WorkspaceTarget  `json:"target"`
	Policy   InvocationPolicy `json:"policy"`
}

// CloseModalMsg signals the parent to close the modal (Esc with empty query).
type CloseModalMsg struct{}

// CloseModalCmd returns a tea.Cmd that emits CloseModalMsg.
func CloseModalCmd() tea.Cmd {
	return func() tea.Msg { return CloseModalMsg{} }
}
