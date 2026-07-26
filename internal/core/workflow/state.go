package workflow

import "fmt"

type WorkflowState int

const (
	StateIdle WorkflowState = iota
	StateInvestigating
	StatePlanning
	StateBuilding
	StateReviewing
	StateRepairing
	StateVerified
	StateFailed
)

func (s WorkflowState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateInvestigating:
		return "investigating"
	case StatePlanning:
		return "planning"
	case StateBuilding:
		return "building"
	case StateReviewing:
		return "reviewing"
	case StateRepairing:
		return "repairing"
	case StateVerified:
		return "verified"
	case StateFailed:
		return "failed"
	default:
		return fmt.Sprintf("WorkflowState(%d)", int(s))
	}
}

func (s WorkflowState) Valid() bool {
	return s >= StateIdle && s <= StateFailed
}

func (s WorkflowState) IsTerminal() bool {
	return s == StateVerified || s == StateFailed
}

type WorkflowEvent int

const (
	EventInvestigate WorkflowEvent = iota
	EventPlan
	EventBuild
	EventReview
	EventFailureIdentified
	EventVerificationPassed
	EventReset
)

func (e WorkflowEvent) String() string {
	switch e {
	case EventInvestigate:
		return "investigate"
	case EventPlan:
		return "plan"
	case EventBuild:
		return "build"
	case EventReview:
		return "review"
	case EventFailureIdentified:
		return "failure-identified"
	case EventVerificationPassed:
		return "verification-passed"
	case EventReset:
		return "reset"
	default:
		return fmt.Sprintf("WorkflowEvent(%d)", int(e))
	}
}

type TransitionError struct {
	From  WorkflowState
	Event WorkflowEvent
	Msg   string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("workflow: invalid transition from %s via %s: %s", e.From, e.Event, e.Msg)
}

type GuardError struct {
	From  WorkflowState
	Event WorkflowEvent
	Msg   string
}

func (e *GuardError) Error() string {
	return fmt.Sprintf("workflow: guard rejected transition from %s via %s: %s", e.From, e.Event, e.Msg)
}
