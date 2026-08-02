package workflow

import (
	"errors"
	"fmt"
	"sync"
)

// Sentinel errors returned by WorkflowRuntime. Use errors.Is to distinguish
// transition failures from invalid target phases.
var (
	// ErrInvalidPhase is returned when a transition targets an undeclared phase.
	ErrInvalidPhase = errors.New("workflow: invalid phase")
	// ErrInvalidTransition is returned when a transition violates the rules.
	ErrInvalidTransition = errors.New("workflow: invalid phase transition")
)

// TransitionError describes a rejected phase transition with full context.
type TransitionError struct {
	From   Phase
	To     Phase
	Reason string
	Err    error
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("workflow: transition from %s to %s: %s", e.From, e.To, e.Reason)
}

// Unwrap returns the underlying sentinel error.
func (e *TransitionError) Unwrap() error { return e.Err }

// TransitionRule decides whether a move from one phase to another is allowed.
// It returns nil when the move is permitted, or a *TransitionError otherwise.
type TransitionRule func(from, to Phase) error

// DefaultTransitionRule encodes the system phase transition rules:
//   - The target phase must be a declared phase.
//   - Staying in the current phase is a no-op and always allowed.
//   - Moving forward in the lifecycle is always allowed (jumps permitted).
//   - Moving backward is only allowed as a re-plan from Build or Review back
//     to Plan, where new investigation outcomes may invalidate the plan.
//   - Any other backward move is rejected; use WorkflowRuntime.Reset to
//     restart the lifecycle from Ask.
func DefaultTransitionRule(from, to Phase) error {
	if !to.Valid() {
		return &TransitionError{
			From:   from,
			To:     to,
			Reason: "target phase is not a declared phase",
			Err:    ErrInvalidPhase,
		}
	}
	if from == to {
		return nil
	}
	if to.Precedes(from) {
		if to == PhasePlan && (from == PhaseBuild || from == PhaseReview) {
			return nil
		}
		return &TransitionError{
			From:   from,
			To:     to,
			Reason: "moving to a previous phase is not permitted",
			Err:    ErrInvalidTransition,
		}
	}
	return nil
}

// WorkflowRuntime manages the phase state of a workflow run. Implementations
// must be safe for concurrent use.
type WorkflowRuntime interface {
	// Phase returns the current phase.
	Phase() Phase
	// Transition moves the runtime to the target phase according to the
	// configured rule. A no-op transition to the current phase succeeds.
	Transition(next Phase) error
	// CanTransition reports whether Transition(next) would succeed.
	CanTransition(next Phase) bool
	// Reset returns the runtime to the entry phase.
	Reset()
	// IsTerminal reports whether the runtime is in a terminal phase.
	IsTerminal() bool
}

// RuntimeOption configures a WorkflowRuntime at construction.
type RuntimeOption func(*runtime)

// WithTransitionRule overrides the default phase transition rules.
func WithTransitionRule(rule TransitionRule) RuntimeOption {
	return func(r *runtime) {
		if rule != nil {
			r.rule = rule
		}
	}
}

type runtime struct {
	mu    sync.RWMutex
	phase Phase
	rule  TransitionRule
}

// NewWorkflowRuntime builds a runtime starting in PhaseAsk with the default
// transition rules.
func NewWorkflowRuntime(opts ...RuntimeOption) WorkflowRuntime {
	r := &runtime{
		phase: PhaseAsk,
		rule:  DefaultTransitionRule,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *runtime) Phase() Phase {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.phase
}

func (r *runtime) Transition(next Phase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.rule(r.phase, next); err != nil {
		return err
	}
	r.phase = next
	return nil
}

func (r *runtime) CanTransition(next Phase) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rule(r.phase, next) == nil
}

func (r *runtime) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = PhaseAsk
}

func (r *runtime) IsTerminal() bool {
	return r.Phase().IsTerminal()
}
