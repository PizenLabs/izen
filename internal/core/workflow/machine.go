package workflow

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/core/classifier"
)

type TransitionContext struct {
	FailureClass    classifier.FailureClass
	HasPlan         bool
	HasCapabilities bool
	HasArtifact     bool
}

type WorkflowStateMachine struct {
	current     WorkflowState
	coordinator *CheckpointCoordinator

	// pendingApproval is the single source of truth for whether the workflow
	// is blocked on an explicit human approval gate (a queued build/hotfix
	// proposal or an intent-disambiguation prompt). The presentation layer
	// derives its AwaitingApproval UI state from this signal rather than
	// tracking approvals independently.
	pendingApproval bool
}

func NewWorkflowStateMachine() *WorkflowStateMachine {
	return &WorkflowStateMachine{
		current: StateIdle,
	}
}

func (m *WorkflowStateMachine) WithCheckpointCoordinator(cc *CheckpointCoordinator) *WorkflowStateMachine {
	m.coordinator = cc
	return m
}

func (m *WorkflowStateMachine) State() WorkflowState {
	return m.current
}

// MarkApprovalPending records that the workflow is blocked on an explicit
// human approval gate. It is the canonical signal the presentation layer
// projects onto its AwaitingApproval UI state.
//
// The boolean flag is the gate signal; the STATE it also drives is the
// lifecycle truth. Both move together, because a parked run that is only
// remembered by a boolean is observably indistinguishable from an idle one —
// which is exactly how a plan step completing could masquerade as an execution
// reaching its end.
//
// ParkFromState drives the state transition without touching the flag. It
// exists so the lifecycle owner can park a run that never went through the
// legacy proposal gate, and vice versa.
func (m *WorkflowStateMachine) MarkApprovalPending() {
	if m == nil {
		return
	}
	m.pendingApproval = true
	m.ParkFromState()
}

// ParkFromState moves a live workflow into the non-terminal parked position.
// It is idempotent and fail-open: if a transition is refused the pending-approval
// flag remains the authoritative gate signal, because refusing a park must never
// make an unauthenticated mutation look authorized — nor strand a parked run in
// a position the resume edge cannot reach.
func (m *WorkflowStateMachine) ParkFromState() {
	if m == nil || m.current == StateAwaitingAuthorization {
		return
	}
	_ = m.SendEvent(EventAwaitAuthorization, TransitionContext{})
}

// Parked reports whether the workflow holds a live execution run blocked on a
// human authorization decision. A parked run is never idle and never terminal.
func (m *WorkflowStateMachine) Parked() bool {
	if m == nil {
		return false
	}
	return m.current == StateAwaitingAuthorization
}

// MarkApprovalResolved clears the pending-approval gate.
func (m *WorkflowStateMachine) MarkApprovalResolved() {
	if m != nil {
		m.pendingApproval = false
	}
}

// PendingApproval reports whether the workflow is awaiting human approval.
func (m *WorkflowStateMachine) PendingApproval() bool {
	if m == nil {
		return false
	}
	return m.pendingApproval
}

func (m *WorkflowStateMachine) MustTransition(event WorkflowEvent, ctx TransitionContext) {
	if err := m.SendEvent(event, ctx); err != nil {
		panic(err)
	}
}

func (m *WorkflowStateMachine) SendEvent(event WorkflowEvent, ctx TransitionContext) error {
	if !m.current.Valid() {
		return &TransitionError{
			From:  m.current,
			Event: event,
			Msg:   "current state is invalid",
			Err:   ErrEventNotAllowed,
		}
	}
	next, err := m.lookup(m.current, event, ctx)
	if err != nil {
		return err
	}
	if (next == StateBuilding || next == StateRepairing) && m.current != next {
		if m.coordinator != nil {
			if err := m.coordinator.CreateBeforeBuild(); err != nil {
				return fmt.Errorf("workflow: checkpoint creation failed before %s: %w", next, err)
			}
		}
	}
	if event == EventFailureIdentified && ctx.FailureClass == classifier.FailureScopeClass {
		if m.coordinator != nil && m.coordinator.HasRef() && next != m.current {
			_ = m.coordinator.Rollback()
		}
	}
	m.current = next
	return nil
}

func (m *WorkflowStateMachine) lookup(from WorkflowState, event WorkflowEvent, ctx TransitionContext) (WorkflowState, error) {
	// Canonical emergency interrupt: any state returns to Idle. This is
	// the single sanctioned path for Ctrl+C / Esc force-cancel so the UI
	// never drifts by hand-setting presentation state while the machine
	// remains in Building/Planning.
	if event == EventUserInterrupt {
		return StateIdle, nil
	}
	// ── THE PARK IS AN EXPLICIT EVENT, NEVER A SIDE EFFECT ─────────────
	// Every live position may park at a human authorization boundary (an
	// execution run holding a mutation candidate), and re-parking is
	// idempotent. Crucially this is the ONLY producer of
	// StateAwaitingAuthorization, so "the workflow went idle" and "the run
	// parked awaiting a human" can never be the same observable fact.
	if event == EventAwaitAuthorization {
		return StateAwaitingAuthorization, nil
	}
	switch from {
	case StateIdle:
		switch event {
		case EventInvestigate:
			return StateInvestigating, nil
		case EventPlan:
			return StatePlanning, nil
		case EventReset:
			return StateIdle, nil
		}
	case StateInvestigating:
		switch event {
		case EventPlan:
			return StatePlanning, nil
		case EventReset:
			return StateIdle, nil
		}
	case StatePlanning:
		switch event {
		case EventBuild:
			if !ctx.HasPlan {
				return from, &GuardError{From: from, Event: event, Msg: "no authorized plan or micro-plan", Err: ErrInvalidTransition}
			}
			if !ctx.HasCapabilities {
				return from, &GuardError{From: from, Event: event, Msg: "no authorized capabilities", Err: ErrInvalidTransition}
			}
			return StateBuilding, nil
		case EventReset:
			return StateIdle, nil
		}
	case StateBuilding:
		switch event {
		case EventReview:
			return StateReviewing, nil
		case EventFailureIdentified:
			return m.failureTarget(ctx.FailureClass)
		case EventReset:
			return StateIdle, nil
		}
	case StateReviewing:
		switch event {
		case EventVerificationPassed:
			return StateVerified, nil
		case EventFailureIdentified:
			return m.failureTarget(ctx.FailureClass)
		case EventReset:
			return StateIdle, nil
		}
	case StateRepairing:
		switch event {
		case EventBuild:
			if !ctx.HasCapabilities {
				return from, &GuardError{From: from, Event: event, Msg: "no authorized capabilities", Err: ErrInvalidTransition}
			}
			return StateBuilding, nil
		case EventFailureIdentified:
			return m.failureTarget(ctx.FailureClass)
		case EventReset:
			return StateIdle, nil
		}
	case StateVerified:
		if event == EventReset {
			return StateIdle, nil
		}
	case StateFailed:
		if event == EventReset {
			return StateIdle, nil
		}
	case StateAwaitingAuthorization:
		// ── THE RESUME EDGE ───────────────────────────────────────────
		// A human authorization is a control-plane EVENT that resumes the
		// SAME parked run into the executable position. It is not a reset,
		// not a re-plan and not a new run: the candidate, the targets, the
		// run identity and the objective all survive untouched.
		switch event {
		case EventBuild:
			if !ctx.HasCapabilities {
				return from, &GuardError{From: from, Event: event, Msg: "no authorized capabilities", Err: ErrInvalidTransition}
			}
			return StateBuilding, nil
		case EventFailureIdentified:
			return m.failureTarget(ctx.FailureClass)
		case EventReset:
			return StateIdle, nil
		}
	}
	return from, &TransitionError{From: from, Event: event, Msg: "event not allowed in current state", Err: ErrEventNotAllowed}
}

func (m *WorkflowStateMachine) failureTarget(class classifier.FailureClass) (WorkflowState, error) {
	if m.current == StateRepairing && class == classifier.FailureCodeClass {
		return StateRepairing, nil
	}
	switch class {
	case classifier.FailureCodeClass:
		return StateRepairing, nil
	case classifier.FailureEnvironmentClass:
		return StateInvestigating, nil
	case classifier.FailureTestClass:
		return StatePlanning, nil
	case classifier.FailureScopeClass:
		return StatePlanning, nil
	case classifier.FailureUnknownClass:
		return StateFailed, nil
	}
	return m.current, &TransitionError{From: m.current, Event: EventFailureIdentified, Msg: fmt.Sprintf("unknown failure class %d", int(class)), Err: ErrInvalidTransition}
}
