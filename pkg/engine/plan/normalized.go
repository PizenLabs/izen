package plan

import "github.com/PizenLabs/izen/pkg/engine/strategy"

// NormalizeMetrics describes what the PlanNormalizer changed.
type NormalizeMetrics struct {
	// InputCount is the number of steps fed into the normalizer.
	InputCount int
	// Deduped is the number of duplicate steps collapsed.
	Deduped int
	// Standardized is the number of steps whose action verb was rewritten to
	// a canonical form.
	Standardized int
	// Reordered reports whether step order changed after deduplication.
	Reordered bool
}

// NormalizedPlan is the immutable, deduplicated and standardized form of a
// plan. It is produced by the PlanNormalizer from a LogicalPlan and never
// mutated afterwards.
type NormalizedPlan struct {
	id      string
	goal    strategy.Goal
	steps   []Step
	metrics NormalizeMetrics
}

// NewNormalizedPlan constructs the immutable normalized artifact.
func NewNormalizedPlan(goal strategy.Goal, steps []Step, metrics NormalizeMetrics) *NormalizedPlan {
	return &NormalizedPlan{
		id:      newPlanID("normalized"),
		goal:    goal,
		steps:   immutableSteps(steps),
		metrics: metrics,
	}
}

// ID returns the immutable artifact identifier.
func (p *NormalizedPlan) ID() string { return p.id }

// Goal returns the strategy goal the plan serves.
func (p *NormalizedPlan) Goal() strategy.Goal { return p.goal }

// Steps returns a defensive copy of the normalized steps.
func (p *NormalizedPlan) Steps() []Step { return immutableSteps(p.steps) }

// StepCount returns the number of normalized steps.
func (p *NormalizedPlan) StepCount() int { return len(p.steps) }

// Metrics returns the normalization telemetry.
func (p *NormalizedPlan) Metrics() NormalizeMetrics { return p.metrics }
