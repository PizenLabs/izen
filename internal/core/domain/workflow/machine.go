package workflow

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/checkpoint"
)

// Re-export domain types for workflow domain
type WorkflowState = domain.WorkflowState

// State constants are defined in domain state; import them via alias
// The actual machine delegates to the core/workflow implementation but
// exposes the domain contracts for Phase 2 alignment.
type Machine struct {
	current         domain.WorkflowState
	coordinator     checkpoint.CheckpointCoordinator
	pendingApproval bool
}

func NewMachine() *Machine {
	return &Machine{current: domain.StateIdle}
}

func (m *Machine) WithCoordinator(cc checkpoint.CheckpointCoordinator) *Machine {
	m.coordinator = cc
	return m
}

func (m *Machine) State() domain.WorkflowState { return m.current }

func (m *Machine) PendingApproval() bool {
	if m == nil {
		return false
	}
	return m.pendingApproval
}

func (m *Machine) MarkApprovalPending() {
	if m != nil {
		m.pendingApproval = true
	}
}

func (m *Machine) MarkApprovalResolved() {
	if m != nil {
		m.pendingApproval = false
	}
}

func (m *Machine) SendEvent(ctx context.Context, event domain.WorkflowEvent, tc domain.TransitionContext) error {
	if !m.current.Valid() {
		return &domain.TransitionError{From: m.current, Event: event, Msg: "current state is invalid"}
	}
	next, err := m.lookup(m.current, event, tc)
	if err != nil {
		return err
	}
	// Checkpoint creation on entry to Building/Repairing
	if (next == domain.StateBuilding || next == domain.StateRepairing) && m.current != next {
		if m.coordinator != nil {
			frameID := domain.FrameID(fmt.Sprintf("frame_%s", next.String()))
			if _, err := m.coordinator.CreateBeforeBuild(ctx, frameID); err != nil {
				return fmt.Errorf("workflow: checkpoint creation failed before %s: %w", next, err)
			}
		}
	}
	// Rollback on scope failure
	if event == domain.EventFailureIdentified && tc.FailureClass == domain.FailureScopeClass {
		if m.coordinator != nil && m.coordinator.HasRef() && next != m.current {
			// Use a dummy checkpoint ID for rollback boundary; real ID is managed by coordinator
			_ = m.coordinator.Rollback(ctx, domain.CheckpointID(""), domain.RollbackLocal)
		}
	}
	m.current = next
	return nil
}

func (m *Machine) lookup(from domain.WorkflowState, event domain.WorkflowEvent, ctx domain.TransitionContext) (domain.WorkflowState, error) {
	switch from {
	case domain.StateIdle:
		switch event {
		case domain.EventInvestigate:
			return domain.StateInvestigating, nil
		case domain.EventPlan:
			return domain.StatePlanning, nil
		case domain.EventReset:
			return domain.StateIdle, nil
		}
	case domain.StateInvestigating:
		switch event {
		case domain.EventPlan:
			return domain.StatePlanning, nil
		case domain.EventReset:
			return domain.StateIdle, nil
		}
	case domain.StatePlanning:
		switch event {
		case domain.EventBuild:
			if !ctx.HasPlan {
				return from, &domain.GuardError{From: from, Event: event, Msg: "no authorized plan or micro-plan"}
			}
			if !ctx.HasCapabilities {
				return from, &domain.GuardError{From: from, Event: event, Msg: "no authorized capabilities"}
			}
			return domain.StateBuilding, nil
		case domain.EventReset:
			return domain.StateIdle, nil
		}
	case domain.StateBuilding:
		switch event {
		case domain.EventReview:
			return domain.StateReviewing, nil
		case domain.EventFailureIdentified:
			return m.failureTarget(ctx.FailureClass)
		case domain.EventReset:
			return domain.StateIdle, nil
		}
	case domain.StateReviewing:
		switch event {
		case domain.EventVerificationPassed:
			return domain.StateVerified, nil
		case domain.EventFailureIdentified:
			return m.failureTarget(ctx.FailureClass)
		case domain.EventReset:
			return domain.StateIdle, nil
		}
	case domain.StateRepairing:
		switch event {
		case domain.EventBuild:
			if !ctx.HasCapabilities {
				return from, &domain.GuardError{From: from, Event: event, Msg: "no authorized capabilities"}
			}
			return domain.StateBuilding, nil
		case domain.EventFailureIdentified:
			return m.failureTarget(ctx.FailureClass)
		case domain.EventReset:
			return domain.StateIdle, nil
		}
	case domain.StateVerified:
		if event == domain.EventReset {
			return domain.StateIdle, nil
		}
	case domain.StateFailed:
		if event == domain.EventReset {
			return domain.StateIdle, nil
		}
	}
	return from, &domain.TransitionError{From: from, Event: event, Msg: "event not allowed in current state"}
}

func (m *Machine) failureTarget(class domain.FailureClass) (domain.WorkflowState, error) {
	if m.current == domain.StateRepairing && class == domain.FailureCodeClass {
		return domain.StateRepairing, nil
	}
	switch class {
	case domain.FailureCodeClass:
		return domain.StateRepairing, nil
	case domain.FailureEnvironmentClass:
		return domain.StateInvestigating, nil
	case domain.FailureTestClass:
		return domain.StatePlanning, nil
	case domain.FailureScopeClass:
		return domain.StatePlanning, nil
	case domain.FailureUnknownClass:
		return domain.StateFailed, nil
	}
	return m.current, &domain.TransitionError{From: m.current, Event: domain.EventFailureIdentified, Msg: fmt.Sprintf("unknown failure class %d", int(class))}
}
