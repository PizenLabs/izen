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
				return from, &GuardError{From: from, Event: event, Msg: "no authorized plan or micro-plan"}
			}
			if !ctx.HasCapabilities {
				return from, &GuardError{From: from, Event: event, Msg: "no authorized capabilities"}
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
				return from, &GuardError{From: from, Event: event, Msg: "no authorized capabilities"}
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
	}
	return from, &TransitionError{From: from, Event: event, Msg: "event not allowed in current state"}
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
	return m.current, &TransitionError{From: m.current, Event: EventFailureIdentified, Msg: fmt.Sprintf("unknown failure class %d", int(class))}
}
