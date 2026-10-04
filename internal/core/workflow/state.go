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

	// StateAwaitingAuthorization is the PARKED execution position: an execution
	// run is alive, a mutation candidate is held, and the run is blocked on an
	// explicit human authorization decision.
	//
	// It exists because "idle" cannot mean two opposite things. Before this
	// state, a parked run and a finished run were both reported as idle, so the
	// runtime could neither refuse to finish a run that still owed work nor
	// resume one that had not actually parked. The state is deliberately
	// NON-TERMINAL (IsTerminal reports false): a plan step completing, a
	// provider returning, or a candidate being produced does NOT reach it, and
	// nothing reaches it implicitly.
	//
	// It is appended after StateFailed so no previously persisted ordinal moves.
	StateAwaitingAuthorization
)

// parked is every position in which an execution run is alive but blocked on a
// human decision. It is the set of states that may NOT be reported as terminal
// and that a later authorization resumes rather than replaces.
func (s WorkflowState) parked() bool { return s == StateAwaitingAuthorization }

// Parked reports whether the workflow is blocked on a human authorization
// decision — i.e. a live execution run with a held mutation candidate. It is
// the invariant-1 predicate: a parked run is NEVER idle, completed, or failed.
func (s WorkflowState) Parked() bool { return s.parked() }

// Executable reports whether a mutation may be authorized from this state. It
// is true only for the two positions in which the runtime is actually applying
// or repairing a workspace; a parked run must first be RESUMED into one of them
// (an explicit control-plane transition), never be treated as idle.
func (s WorkflowState) Executable() bool {
	return s == StateBuilding || s == StateRepairing
}

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
	case StateAwaitingAuthorization:
		return "awaiting_authorization"
	default:
		return fmt.Sprintf("WorkflowState(%d)", int(s))
	}
}

func (s WorkflowState) Valid() bool {
	return s >= StateIdle && s <= StateAwaitingAuthorization
}

// IsTerminal reports whether the workflow has reached a terminal outcome.
// StateAwaitingAuthorization is explicitly NOT terminal: the run it belongs to
// still owes a mutation, a verification and an objective verdict.
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
	// EventUserInterrupt is the canonical emergency-interrupt event. It
	// transitions any non-idle workflow state back to StateIdle so the
	// presentation layer can derive StateChat from the state machine
	// instead of forcing it manually (Phase 2 state-drift fix).
	EventUserInterrupt
	// EventAwaitAuthorization parks a live execution run at a human
	// authorization boundary. It is the ONLY way into StateAwaitingAuthorization,
	// so "parked" can never be inferred from a step/provider/candidate completing.
	EventAwaitAuthorization
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
	case EventUserInterrupt:
		return "user-interrupt"
	case EventAwaitAuthorization:
		return "await-authorization"
	default:
		return fmt.Sprintf("WorkflowEvent(%d)", int(e))
	}
}

type TransitionError struct {
	From  WorkflowState
	Event WorkflowEvent
	Msg   string
	Err   error
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("workflow: invalid transition from %s via %s: %s", e.From, e.Event, e.Msg)
}

// Unwrap returns the underlying sentinel. When Err is unset (legacy
// constructions in tests), it defaults to ErrEventNotAllowed so errors.Is
// classification keeps working without string matching.
func (e *TransitionError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Err != nil {
		return e.Err
	}
	return ErrEventNotAllowed
}

type GuardError struct {
	From  WorkflowState
	Event WorkflowEvent
	Msg   string
	Err   error
}

func (e *GuardError) Error() string {
	return fmt.Sprintf("workflow: guard rejected transition from %s via %s: %s", e.From, e.Event, e.Msg)
}

// Unwrap returns the underlying sentinel, defaulting to ErrInvalidTransition.
func (e *GuardError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Err != nil {
		return e.Err
	}
	return ErrInvalidTransition
}
