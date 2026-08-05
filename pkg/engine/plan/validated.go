package plan

import "github.com/PizenLabs/izen/pkg/engine/strategy"

// ValidationResult is one schema or internal-logic check verdict.
type ValidationResult struct {
	// Subject is the step id the check applied to, or "plan" for plan-wide
	// checks.
	Subject string
	// Rule is the stable rule identifier, e.g. "schema:target" or
	// "logic:acyclic".
	Rule string
	// OK reports whether the check passed.
	OK bool
	// Detail is a human-readable explanation of the verdict.
	Detail string
}

// ValidatedPlan is the immutable, internally-consistent plan produced by the
// PlanValidator. Valid() reports whether every schema and logic check
// passed; the PolicyEngine refuses to evaluate an invalid plan.
type ValidatedPlan struct {
	id      string
	goal    strategy.Goal
	steps   []Step
	results []ValidationResult
	valid   bool
}

// NewValidatedPlan constructs the immutable validated artifact.
func NewValidatedPlan(goal strategy.Goal, steps []Step, results []ValidationResult, valid bool) *ValidatedPlan {
	return &ValidatedPlan{
		id:      newPlanID("validated"),
		goal:    goal,
		steps:   immutableSteps(steps),
		results: append([]ValidationResult(nil), results...),
		valid:   valid,
	}
}

// ID returns the immutable artifact identifier.
func (p *ValidatedPlan) ID() string { return p.id }

// Goal returns the strategy goal the plan serves.
func (p *ValidatedPlan) Goal() strategy.Goal { return p.goal }

// Steps returns a defensive copy of the validated steps.
func (p *ValidatedPlan) Steps() []Step { return immutableSteps(p.steps) }

// StepCount returns the number of validated steps.
func (p *ValidatedPlan) StepCount() int { return len(p.steps) }

// Valid reports whether every validation check passed.
func (p *ValidatedPlan) Valid() bool { return p.valid }

// Results returns a defensive copy of the validation verdicts.
func (p *ValidatedPlan) Results() []ValidationResult {
	return append([]ValidationResult(nil), p.results...)
}

// FailedResults returns the verdicts that did not pass.
func (p *ValidatedPlan) FailedResults() []ValidationResult {
	var out []ValidationResult
	for _, r := range p.results {
		if !r.OK {
			out = append(out, r)
		}
	}
	return out
}
