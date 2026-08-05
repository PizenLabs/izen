// Package strategy is the microkernel's goal-derivation layer. A strategy
// converts a classified Intent plus an assembled PlanningContext into an
// immutable Goal artifact; the plan pipeline then derives the LogicalPlan
// from that Goal. This separation keeps "what we want" (the Goal) strictly
// apart from "how we get it" (the plan).
package strategy

import (
	"fmt"

	"github.com/PizenLabs/izen/pkg/engine/context"
	"github.com/PizenLabs/izen/pkg/engine/intent"
)

// ConstraintKind enumerates the typed constraints a Goal can carry.
type ConstraintKind string

const (
	// ConstraintPermittedRoot bounds every plan target to a filesystem root.
	ConstraintPermittedRoot ConstraintKind = "permitted_root"
	// ConstraintForbiddenGlob forbids plan targets matching a glob.
	ConstraintForbiddenGlob ConstraintKind = "forbidden_glob"
	// ConstraintMaxSteps caps the number of steps a plan may contain.
	ConstraintMaxSteps ConstraintKind = "max_steps"
	// ConstraintRequireVerify demands a verify step before completion.
	ConstraintRequireVerify ConstraintKind = "require_verify"
	// ConstraintForbidShell forbids shell-executing plan steps.
	ConstraintForbidShell ConstraintKind = "forbid_shell"
)

// Constraint is one typed, immutable requirement attached to a Goal.
type Constraint struct {
	Kind  ConstraintKind
	Value string
}

// Goal is the immutable intent-level target produced by a strategy. It
// describes what success looks like and which resources are in scope; it
// deliberately contains no execution steps — those are derived by the plan
// pipeline.
type Goal struct {
	intent      intent.Intent
	outcome     string
	targets     []string
	newFiles    []string
	constraints []Constraint
	criteria    []string
}

// Intent returns the classified intent the goal serves.
func (g Goal) Intent() intent.Intent { return g.intent }

// Outcome is the natural-language statement of what success looks like.
func (g Goal) Outcome() string { return g.outcome }

// Targets are the existing resources the goal will modify.
func (g Goal) Targets() []string {
	return append([]string(nil), g.targets...)
}

// NewFiles are the files the goal will create from scratch.
func (g Goal) NewFiles() []string {
	return append([]string(nil), g.newFiles...)
}

// Constraints are the typed requirements attached to the goal.
func (g Goal) Constraints() []Constraint {
	return append([]Constraint(nil), g.constraints...)
}

// Criteria are the verifiable success criteria of the goal.
func (g Goal) Criteria() []string {
	return append([]string(nil), g.criteria...)
}

// RequiresVerify reports whether the goal demands a verify step. An explicit
// require_verify constraint wins; otherwise the intent's requires-test facet
// decides.
func (g Goal) RequiresVerify() bool {
	for _, c := range g.constraints {
		if c.Kind == ConstraintRequireVerify {
			return c.Value == "true"
		}
	}
	return g.intent.Has(intent.FacetRequiresTest)
}

// IsZero reports whether the goal is the zero value.
func (g Goal) IsZero() bool { return g.intent.IsZero() && g.outcome == "" }

// Validate reports whether the goal is well-formed.
func (g Goal) Validate() error {
	if g.intent.IsZero() {
		return fmt.Errorf("strategy: goal carries a zero intent")
	}
	if err := g.intent.Validate(); err != nil {
		return fmt.Errorf("strategy: %w", err)
	}
	for _, c := range g.constraints {
		if !c.Kind.valid() {
			return fmt.Errorf("strategy: invalid constraint kind %q", c.Kind)
		}
	}
	return nil
}

func (k ConstraintKind) valid() bool {
	switch k {
	case ConstraintPermittedRoot, ConstraintForbiddenGlob, ConstraintMaxSteps,
		ConstraintRequireVerify, ConstraintForbidShell:
		return true
	default:
		return false
	}
}

// GoalOption configures a Goal during construction.
type GoalOption func(*goalBuilder)

type goalBuilder struct {
	intent      intent.Intent
	outcome     string
	targets     []string
	newFiles    []string
	constraints []Constraint
	criteria    []string
}

// WithOutcome sets the natural-language goal statement.
func WithOutcome(s string) GoalOption {
	return func(b *goalBuilder) { b.outcome = s }
}

// WithTargets adds existing resources the goal will modify.
func WithTargets(targets ...string) GoalOption {
	return func(b *goalBuilder) { b.targets = append(b.targets, targets...) }
}

// WithNewFiles adds files the goal will create from scratch.
func WithNewFiles(files ...string) GoalOption {
	return func(b *goalBuilder) { b.newFiles = append(b.newFiles, files...) }
}

// WithConstraint attaches one or more typed constraints to the goal.
func WithConstraint(c ...Constraint) GoalOption {
	return func(b *goalBuilder) { b.constraints = append(b.constraints, c...) }
}

// WithCriteria appends verifiable success criteria.
func WithCriteria(criteria ...string) GoalOption {
	return func(b *goalBuilder) { b.criteria = append(b.criteria, criteria...) }
}

// NewGoal constructs an immutable Goal for the given intent. The goal is
// always derived from the classified intent, never from a bare prompt.
func NewGoal(in intent.Intent, opts ...GoalOption) (Goal, error) {
	if in.IsZero() {
		return Goal{}, fmt.Errorf("strategy: cannot build a goal from a zero intent")
	}
	if err := in.Validate(); err != nil {
		return Goal{}, fmt.Errorf("strategy: %w", err)
	}
	b := &goalBuilder{intent: in}
	for _, o := range opts {
		o(b)
	}
	g := Goal{
		intent:      b.intent,
		outcome:     b.outcome,
		targets:     append([]string(nil), b.targets...),
		newFiles:    append([]string(nil), b.newFiles...),
		constraints: append([]Constraint(nil), b.constraints...),
		criteria:    append([]string(nil), b.criteria...),
	}
	if err := g.Validate(); err != nil {
		return Goal{}, err
	}
	return g, nil
}

// PlanningStrategy is the strategy interface of the microkernel. It
// determines the Goal from a classified Intent and an assembled
// PlanningContext. Implementations must be pure with respect to their inputs
// and return a fresh Goal; they must never mutate the context.
type PlanningStrategy interface {
	// Name returns the stable strategy identifier.
	Name() string
	// DetermineGoal derives the immutable Goal for the intent and context.
	DetermineGoal(in intent.Intent, pc context.PlanningContext) (Goal, error)
}

// PlanningStrategyFunc adapts a plain function to PlanningStrategy.
type PlanningStrategyFunc struct {
	name string
	fn   func(intent.Intent, context.PlanningContext) (Goal, error)
}

// NewPlanningStrategyFunc wraps a named strategy function.
func NewPlanningStrategyFunc(name string, fn func(intent.Intent, context.PlanningContext) (Goal, error)) PlanningStrategyFunc {
	return PlanningStrategyFunc{name: name, fn: fn}
}

// Name implements PlanningStrategy.
func (s PlanningStrategyFunc) Name() string { return s.name }

// DetermineGoal implements PlanningStrategy.
func (s PlanningStrategyFunc) DetermineGoal(in intent.Intent, pc context.PlanningContext) (Goal, error) {
	if s.fn == nil {
		return Goal{}, fmt.Errorf("strategy %q: nil function", s.name)
	}
	return s.fn(in, pc)
}
