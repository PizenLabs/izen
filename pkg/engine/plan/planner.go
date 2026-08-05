package plan

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/intent"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// Planner derives an immutable LogicalPlan from a strategy Goal. It is the
// goal→plan seam of the microkernel: the strategy decides what success looks
// like, and the planner decides the abstract ordered steps that achieve it.
type Planner struct{}

// NewPlanner returns a planner.
func NewPlanner() *Planner { return &Planner{} }

// Derive converts a Goal into a LogicalPlan. The derivation is a pure
// function of the goal: read-only goals produce read steps only, mutating
// goals produce create/modify steps (and a verify step when the goal
// requires one). Steps are chained in declaration order via dependencies.
func (p *Planner) Derive(g strategy.Goal) (*LogicalPlan, error) {
	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	in := g.Intent()

	readOnly := in.Has(intent.FacetReadOnly) || !in.Family().Mutating()
	if readOnly {
		return p.deriveReadOnly(g)
	}
	return p.deriveMutating(g)
}

// deriveReadOnly maps every goal target to a read step.
func (p *Planner) deriveReadOnly(g strategy.Goal) (*LogicalPlan, error) {
	targets := g.Targets()
	steps := make([]Step, 0, len(targets))
	for i, target := range targets {
		steps = append(steps, NewStep(
			StepRead, target,
			WithID(fmt.Sprintf("logical-%d", i)),
			WithReason("read-only goal"),
		))
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: read-only goal has no targets", ErrEmptyPlan)
	}
	summary := fmt.Sprintf("read-only: %d inspect step(s)", len(steps))
	return NewLogicalPlan(g, steps, summary), nil
}

// deriveMutating maps goal new files to create steps, existing targets to
// modify steps, and appends a verify step when the goal requires one.
func (p *Planner) deriveMutating(g strategy.Goal) (*LogicalPlan, error) {
	newFiles := g.NewFiles()
	targets := g.Targets()
	wantVerify := g.RequiresVerify()

	total := len(newFiles) + len(targets)
	if total == 0 {
		return nil, fmt.Errorf("%w: mutating goal has no files or targets", ErrEmptyPlan)
	}
	if wantVerify {
		total++
	}

	steps := make([]Step, 0, total)
	var prevID string
	i := 0
	link := func(s Step) {
		if prevID != "" {
			s = s.WithDependenciesDerived(append(s.DependsOn(), prevID))
		}
		prevID = s.ID()
		steps = append(steps, s)
	}

	for _, f := range newFiles {
		link(NewStep(StepCreate, f,
			WithID(fmt.Sprintf("logical-%d", i)),
			WithReason("greenfield: "+g.Outcome()),
		))
		i++
	}
	for _, t := range targets {
		link(NewStep(StepModify, t,
			WithID(fmt.Sprintf("logical-%d", i)),
			WithReason("mutating goal"),
		))
		i++
	}
	if wantVerify {
		link(NewStep(StepVerify, "verify",
			WithID(fmt.Sprintf("logical-%d", i)),
			WithReason("goal requires verification"),
		))
	}

	if err := p.enforceMaxSteps(g, len(steps)); err != nil {
		return nil, err
	}

	summary := fmt.Sprintf("mutating: %d step(s) [%s]", len(steps), describeSteps(steps))
	return NewLogicalPlan(g, steps, summary), nil
}

// enforceMaxSteps rejects plans that exceed a goal's max_steps constraint.
func (p *Planner) enforceMaxSteps(g strategy.Goal, count int) error {
	for _, c := range g.Constraints() {
		if c.Kind != strategy.ConstraintMaxSteps {
			continue
		}
		max, err := strconv.Atoi(c.Value)
		if err != nil {
			return fmt.Errorf("plan: goal max_steps constraint %q is not an integer", c.Value)
		}
		if max > 0 && count > max {
			return fmt.Errorf("plan: derived %d steps exceeds goal max_steps of %d", count, max)
		}
	}
	return nil
}

// describeSteps renders a compact summary of the step kinds.
func describeSteps(steps []Step) string {
	counts := map[StepKind]int{}
	for _, s := range steps {
		counts[s.Kind()]++
	}
	var parts []string
	for _, k := range []StepKind{StepCreate, StepModify, StepDelete, StepRead, StepRun, StepVerify} {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	return strings.Join(parts, ", ")
}
