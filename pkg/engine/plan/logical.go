package plan

import (
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// LogicalPlan is the immutable abstract plan derived by the Planner from a
// strategy Goal. It describes what should happen in ordered Steps without
// any physical detail: no working directory, no concrete commands.
type LogicalPlan struct {
	id      string
	goal    strategy.Goal
	steps   []Step
	summary string
}

// NewLogicalPlan constructs the immutable logical artifact. The step slice
// is copied in so callers cannot mutate the plan afterwards.
func NewLogicalPlan(goal strategy.Goal, steps []Step, summary string) *LogicalPlan {
	return &LogicalPlan{
		id:      newPlanID("logical"),
		goal:    goal,
		steps:   immutableSteps(steps),
		summary: summary,
	}
}

// ID returns the immutable artifact identifier.
func (p *LogicalPlan) ID() string { return p.id }

// Goal returns the strategy goal the plan was derived from.
func (p *LogicalPlan) Goal() strategy.Goal { return p.goal }

// Steps returns a defensive copy of the plan's steps.
func (p *LogicalPlan) Steps() []Step { return immutableSteps(p.steps) }

// StepCount returns the number of steps.
func (p *LogicalPlan) StepCount() int { return len(p.steps) }

// Summary returns the human-readable plan summary.
func (p *LogicalPlan) Summary() string { return p.summary }
