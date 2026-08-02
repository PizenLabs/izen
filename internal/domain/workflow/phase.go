// Package workflow defines the pure lifecycle model of an izen run: the
// ordered execution phases and the runtime that enforces the phase transition
// rules. It holds no references to the runtime, infrastructure, or
// presentation layers.
package workflow

import "fmt"

// Phase describes the current stage of the workflow lifecycle. Phases are
// ordered: Ask < Investigate < Plan < Build < Review.
type Phase int

const (
	// PhaseAsk is the entry phase: intent parsing and read-only inspection.
	PhaseAsk Phase = iota
	// PhaseInvestigate performs root-cause analysis of failures.
	PhaseInvestigate
	// PhasePlan produces the task graph before any mutation.
	PhasePlan
	// PhaseBuild executes mutations against the workspace.
	PhaseBuild
	// PhaseReview audits the changes produced by the build phase.
	PhaseReview
)

// String returns the canonical phase name.
func (p Phase) String() string {
	switch p {
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

// Valid reports whether p is one of the declared phases.
func (p Phase) Valid() bool {
	return p >= PhaseAsk && p <= PhaseReview
}

// IsTerminal reports whether the workflow may conclude in this phase.
func (p Phase) IsTerminal() bool {
	return p == PhaseReview
}

// Precedes reports whether p comes strictly before other in the lifecycle.
func (p Phase) Precedes(other Phase) bool {
	return p.Valid() && other.Valid() && p < other
}

// Follows reports whether p comes strictly after other in the lifecycle.
func (p Phase) Follows(other Phase) bool {
	return p.Valid() && other.Valid() && p > other
}
