package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
)

// ── Control Plane Event Messages ──────────────────────────────────────────
//
// These messages carry state change notifications from the Control Plane
// (WorkflowStateMachine, Artifact lifecycle, Budget consumption,
// Authorization) to the Bubble Tea event loop. The UI subscribes by
// listening for these messages in its Update method.

// domainEventMsg carries a DomainEvent published on the engine event bus into
// the Bubble Tea event loop. The UI is a pure projection of the domain event
// stream: engines publish headlessly and never call UI routines directly.
type domainEventMsg struct{ ev events.DomainEvent }

// presentationEventMsg carries a runtime.PresentationEvent — a domain event
// already translated into a UI-ready, decoupled projection by the Application
// layer EventTranslator — into the Bubble Tea event loop. The UI renders the
// view strictly from these payloads; it never touches the original domain
// event vocabulary.
type presentationEventMsg struct {
	ev appruntime.PresentationEvent
}

// runtimeResultMsg reports the outcome of a RuntimeCommand executed through
// the Application-layer facade on a background goroutine. It never carries
// state: the model is only ever mutated on the UI goroutine via the message
// stream.
type runtimeResultMsg struct {
	typ appruntime.CommandType
	err error
}

// WorkflowStateChangedMsg is emitted when the WorkflowStateMachine transitions.
type WorkflowStateChangedMsg struct {
	From  workflow.WorkflowState
	To    workflow.WorkflowState
	Event workflow.WorkflowEvent
}

// ArtifactStateChangedMsg is emitted when an artifact transitions lifecycle.
type ArtifactStateChangedMsg struct {
	ID   artifact.ArtifactID
	From artifact.LifecycleState
	To   artifact.LifecycleState
}

// BudgetConsumedMsg is emitted when the MutationBudget is consumed.
type BudgetConsumedMsg struct {
	Delta budget.BudgetDelta
	Err   error
}

// AuthorizationStatusMsg carries the result of an authorization evaluation.
type AuthorizationStatusMsg struct {
	Approved bool
	Reason   string
}

// ── Event Emitters ────────────────────────────────────────────────────────
//
// These functions create tea.Msg values for the Control Plane to send.
// They are called by engine code, not by the UI layer.

// NewWorkflowStateChanged creates a WorkflowStateChanged message.
func NewWorkflowStateChanged(from, to workflow.WorkflowState, event workflow.WorkflowEvent) tea.Msg {
	return WorkflowStateChangedMsg{From: from, To: to, Event: event}
}

// NewArtifactStateChanged creates an ArtifactStateChanged message.
func NewArtifactStateChanged(id artifact.ArtifactID, from, to artifact.LifecycleState) tea.Msg {
	return ArtifactStateChangedMsg{ID: id, From: from, To: to}
}

// NewBudgetConsumed creates a BudgetConsumed message.
func NewBudgetConsumed(delta budget.BudgetDelta, err error) tea.Msg {
	return BudgetConsumedMsg{Delta: delta, Err: err}
}

// NewAuthorizationStatus creates an AuthorizationStatus message.
func NewAuthorizationStatus(approved bool, reason string) tea.Msg {
	return AuthorizationStatusMsg{Approved: approved, Reason: reason}
}

// ── Tick message for event polling ────────────────────────────────────────
// eventPollTickMsg drives the periodic event subscription check. It does not
// replace the existing spinner/tick loops; it runs alongside them at a lower
// frequency (500ms) so the event bus never dominates the render loop.
//
//nolint:unused
type eventPollTickMsg time.Time

//nolint:unused
func eventPollTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return eventPollTickMsg(t)
	})
}
