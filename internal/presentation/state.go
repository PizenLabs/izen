// Presentation state projection: the modal UI states (Chat, AwaitingApproval,
// Processing) are DERIVED — they are a pure function of the canonical workflow
// state, never independent mutable flags in the view layer. The
// WorkflowViewState projection observes the workflow event stream
// (EventPhaseChanged, EventApprovalRequested) and exposes the derived state;
// the UI layer reads it and must not hand-compute approval/processing states.
package presentation

import (
	"sync"

	"github.com/PizenLabs/izen/internal/events"
)

// UIState is the derived modal presentation state. It mirrors the view's
// input-blocking modes but its value is always computed from the canonical
// workflow phase plus the pending-approval signal.
type UIState uint8

const (
	// StateChat is the resting presentation state: the workflow is idle or in
	// the ask phase and the input line is available.
	StateChat UIState = iota
	// StateAwaitingApproval is derived from the WorkflowStateMachine's
	// pending-approval signal: the workflow is blocked on an explicit human
	// approval gate (queued proposal, hotfix, or intent disambiguation).
	StateAwaitingApproval
	// StateProcessing is derived from the active workflow phase: the workflow
	// is in an execution phase (investigating/planning/building/reviewing/
	// repairing).
	StateProcessing
)

// String renders the presentation state name.
func (s UIState) String() string {
	switch s {
	case StateChat:
		return "chat"
	case StateAwaitingApproval:
		return "awaiting-approval"
	case StateProcessing:
		return "processing"
	default:
		return "chat"
	}
}

// DeriveUIState is the single pure projection from canonical workflow signals
// onto presentation state. It is deterministic and side-effect free: given the
// same signals it always yields the same UIState.
//
//   - A pending approval ALWAYS freezes into StateAwaitingApproval. It takes
//     precedence over every processing signal — if a plan is awaiting approval
//     the user MUST be given control, even if a background process forgot to
//     clear its processing flag (fallback safeguard).
//   - An explicit isProcessing signal yields StateProcessing. It is the
//     in-flight override callers use when they track busy-ness explicitly.
//   - Otherwise the workflow phase maps: active execution phases
//     (investigating/planning/building/reviewing/repairing) yield
//     StateProcessing, every other phase yields StateChat.
//
// The phase names may come from either canonical vocabulary: the workflow
// state-machine names (core/workflow.WorkflowState.String(): "investigating",
// "planning", "building", ...) or the orchestrator phase names
// (orchestrator.Phase.String(): "plan", "build", ...). Both are mapped.
func DeriveUIState(phase string, approvalPending bool, isProcessing bool) UIState {
	if approvalPending {
		return StateAwaitingApproval
	}
	if isProcessing {
		return StateProcessing
	}
	switch phase {
	case "investigating", "investigate",
		"planning", "plan",
		"building", "build",
		"reviewing", "review",
		"repairing":
		return StateProcessing
	default:
		return StateChat
	}
}

// WorkflowViewState is a stateful projection of the workflow event stream. It
// consumes EventPhaseChanged and EventApprovalRequested events and exposes the
// derived UIState. It holds no independent approval/phase logic: every value
// is a reflection of an observed canonical event.
type WorkflowViewState struct {
	mu sync.RWMutex

	// phase is the canonical workflow phase name ("" until the first
	// EventPhaseChanged is observed).
	phase string
	// approvalPending is true while a Tier 4 approval request is outstanding.
	approvalPending bool
}

// NewWorkflowViewState returns an empty projection. It is safe for concurrent
// use; the bus dispatch goroutines feed it via Project while the UI goroutine
// reads UIState.
func NewWorkflowViewState() *WorkflowViewState {
	return &WorkflowViewState{}
}

// Project consumes one domain event and updates the derived projection. It
// recognizes EventPhaseChanged and EventApprovalRequested; all other event
// types are ignored. It reports whether the projected state changed.
func (w *WorkflowViewState) Project(ev events.DomainEvent) bool {
	if w == nil || ev == nil {
		return false
	}
	switch p := ev.Payload().(type) {
	case events.PhaseChangedPayload:
		w.mu.Lock()
		defer w.mu.Unlock()
		if p.To == "" || p.To == w.phase {
			return false
		}
		w.phase = p.To
		return true
	case events.ApprovalRequestedPayload:
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.approvalPending {
			return false
		}
		w.approvalPending = true
		return true
	default:
		return false
	}
}

// Sync aligns the projection with the canonical workflow signals directly (the
// WorkflowStateMachine's phase + pending-approval). It is the read-back seam
// the view layer uses after mutating the canonical source, keeping the
// projection a single authoritative mirror.
func (w *WorkflowViewState) Sync(phase string, approvalPending bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.phase = phase
	w.approvalPending = approvalPending
}

// ResolveApproval clears a previously observed approval request. It is the
// projection of the human resolution action (approve/reject/cancel).
func (w *WorkflowViewState) ResolveApproval() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.approvalPending = false
}

// Phase returns the last observed canonical workflow phase name.
func (w *WorkflowViewState) Phase() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.phase
}

// ApprovalPending reports whether a Tier 4 approval request is outstanding.
func (w *WorkflowViewState) ApprovalPending() bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.approvalPending
}

// UIState derives the presentation state from the projected signals. It is the
// read-only entry point the view layer uses.
func (w *WorkflowViewState) UIState() UIState {
	if w == nil {
		return StateChat
	}
	return DeriveUIState(w.Phase(), w.ApprovalPending(), false)
}
