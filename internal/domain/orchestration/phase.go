// Package orchestration holds the pure orchestration state types of the Izen
// Agent Runtime: logical execution phases and transition errors.
//
// The package carries no execution logic, no state-machine driving, no event
// bus emission, and no filesystem access. It depends only on the standard
// library and has zero imports of internal/ui, internal/runtime,
// internal/kernel, or internal/orchestrator. The legacy
// internal/orchestrator package retains the execution bridge (Orchestrator
// struct, SM driving, bus wiring) and re-exports these canonical types via
// aliases during migration.
package orchestration

import (
	"fmt"

	domainworkflow "github.com/PizenLabs/izen/internal/domain/workflow"
)

// Phase is a logical execution phase within the workflow.
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseAsk
	PhaseInvestigate
	PhasePlan
	PhaseBuild
	PhaseReview
)

// String returns the machine-readable phase label.
func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseAsk:
		return "ask"
	case PhaseInvestigate:
		return "investigate"
	case PhasePlan:
		return "plan"
	case PhaseBuild:
		return "build"
	case PhaseReview:
		return "review"
	default:
		return fmt.Sprintf("Phase(%d)", int(p))
	}
}

// Valid reports whether the phase is a known logical execution phase.
func (p Phase) Valid() bool { return p >= PhaseIdle && p <= PhaseReview }

// ValidTransition reports whether a logical transition from `from` to `to`
// is permitted by the orchestrator's phase table. A self-transition is a
// no-op and always valid. This is the pure, side-effect-free form of the
// legacy orchestrator.validEdge check.
func ValidTransition(from, to Phase) bool {
	if to == from {
		return true
	}
	switch from {
	case PhaseIdle:
		return to == PhaseAsk || to == PhaseInvestigate || to == PhasePlan
	case PhaseAsk:
		return to == PhaseInvestigate || to == PhasePlan
	case PhaseInvestigate:
		return to == PhasePlan || to == PhaseAsk
	case PhasePlan:
		return to == PhaseBuild || to == PhaseAsk || to == PhaseInvestigate
	case PhaseBuild:
		return to == PhaseReview || to == PhaseAsk
	case PhaseReview:
		return to == PhaseBuild || to == PhaseAsk
	default:
		return false
	}
}

// Transition is a pure data-transfer record of a logical phase hop. It
// carries no execution behaviour; the legacy Orchestrator.Transition method
// remains the execution bridge that validates and applies such hops.
type Transition struct {
	From Phase
	To   Phase
}

// Valid reports whether the transition is permitted by the phase table.
func (t Transition) Valid() bool {
	if !t.From.Valid() || !t.To.Valid() {
		return false
	}
	return ValidTransition(t.From, t.To)
}

// TransitionError reports an invalid logical phase transition.
type TransitionError struct {
	From Phase
	To   Phase
	Msg  string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("orchestrator: invalid transition %s -> %s: %s", e.From, e.To, e.Msg)
}

// Unwrap exposes the canonical invalid-transition sentinel so callers can
// classify rejections with errors.Is instead of string matching.
func (e *TransitionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return domainworkflow.ErrInvalidTransition
}

// PhaseStateMachine is the pure phase-tracking value: current phase plus
// ordered history. It performs no SM driving and emits no events; it exists
// so callers that only need phase state (UI models, planners) depend on the
// domain instead of the legacy execution bridge.
type PhaseStateMachine struct {
	current Phase
	history []Phase
}

// NewPhaseStateMachine returns a machine parked at PhaseIdle.
func NewPhaseStateMachine() PhaseStateMachine {
	return PhaseStateMachine{current: PhaseIdle, history: []Phase{PhaseIdle}}
}

// Current returns the current phase.
func (m PhaseStateMachine) Current() Phase { return m.current }

// History returns a defensive copy of the transition history, oldest first.
func (m PhaseStateMachine) History() []Phase {
	out := make([]Phase, len(m.history))
	copy(out, m.history)
	return out
}

// Apply validates and records a transition, returning a TransitionError when
// the edge is forbidden. It never touches a workflow SM or event bus.
func (m *PhaseStateMachine) Apply(next Phase) error {
	if !next.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", next)
	}
	if next == m.current {
		return nil
	}
	if !ValidTransition(m.current, next) {
		return &TransitionError{From: m.current, To: next, Msg: "no valid transition"}
	}
	m.current = next
	m.history = append(m.history, next)
	return nil
}
