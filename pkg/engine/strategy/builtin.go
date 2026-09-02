package strategy

import (
	"fmt"
	"strings"

	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/context"
	"github.com/PizenLabs/izen/pkg/engine/intent"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// extractTargets pulls explicit @file references from a prompt. It delegates to
// the canonical target extraction lexer (target.ExtractReferences) so quoted
// and standard @path references behave identically across admission and
// strategy phases. It makes no assumptions about the files existing —
// existence checks belong to the execution preconditions stage.
func extractTargets(prompt string) []string {
	return target.ExtractReferencePaths(prompt)
}

// baseGoal builds the shared skeleton of a goal from the classified intent:
// facet-derived constraints and default criteria. The family determines the
// mutating/read-only posture.
func baseGoal(in intent.Intent) (*goalBuilder, error) {
	b := &goalBuilder{intent: in}
	switch {
	case in.Has(intent.FacetRequiresTest):
		b.constraints = append(b.constraints, Constraint{
			Kind: ConstraintRequireVerify, Value: "true",
		})
	case in.Has(intent.FacetReadOnly):
		b.constraints = append(b.constraints, Constraint{
			Kind: ConstraintForbidShell, Value: "true",
		})
	}
	if in.Has(intent.FacetHighRisk) {
		b.constraints = append(b.constraints, Constraint{
			Kind: ConstraintMaxSteps, Value: "16",
		})
	}
	return b, nil
}

// ─── PromptStrategy ──────────────────────────────────────────────────────────

// PromptStrategy is the default strategy: it derives the Goal from the
// prompt provider's chunk and the classified intent. Without a bound model
// generator it is fully deterministic — the outcome is the raw prompt and
// targets are the explicit @path references. With a generator bound (for
// example a Gemma model), it delegates outcome wording and greenfield file
// enumeration to the model.
type PromptStrategy struct {
	model GoalGenerator
}

// NewPromptStrategy returns a deterministic prompt-driven strategy.
func NewPromptStrategy() *PromptStrategy { return &PromptStrategy{} }

// WithModel binds a goal generator to the strategy.
func (s *PromptStrategy) WithModel(g GoalGenerator) *PromptStrategy {
	s.model = g
	return s
}

// Name implements PlanningStrategy.
func (s *PromptStrategy) Name() string { return "prompt" }

// DetermineGoal implements PlanningStrategy. It never mutates the context
// and returns a fresh Goal.
func (s *PromptStrategy) DetermineGoal(in intent.Intent, pc context.PlanningContext) (Goal, error) {
	if in.IsZero() {
		return Goal{}, fmt.Errorf("strategy prompt: zero intent")
	}
	prompt := pc.Prompt()

	b, err := baseGoal(in)
	if err != nil {
		return Goal{}, err
	}

	if s.model != nil {
		res, err := s.model.GenerateGoal(stdctx.Background(), GoalRequest{
			Intent:  in,
			Prompt:  prompt,
			Context: pc.Render(),
		})
		if err != nil {
			return Goal{}, fmt.Errorf("strategy prompt: model %s: %w", s.model.ModelName(), err)
		}
		b.outcome = res.Outcome
		b.targets = append(b.targets, res.Targets...)
		b.newFiles = append(b.newFiles, res.NewFiles...)
		b.criteria = append(b.criteria, res.Criteria...)
	} else {
		b.outcome = strings.TrimSpace(prompt)
		b.targets = append(b.targets, extractTargets(prompt)...)
	}

	if b.outcome == "" {
		b.outcome = "Complete the request described in the prompt."
	}
	if len(b.criteria) == 0 {
		b.criteria = append(b.criteria, "Outcome matches the request", "No scope creep beyond the request")
	}

	return NewGoal(in,
		WithOutcome(b.outcome),
		WithTargets(b.targets...),
		WithNewFiles(b.newFiles...),
		WithConstraint(b.constraints[0:]...),
		WithCriteria(b.criteria...),
	)
}

// ─── GuidedStrategy ──────────────────────────────────────────────────────────

// GuidedStrategy builds on a prompt-driven base and additionally constrains
// the goal from the assembled context: the workspace root becomes a
// permitted-root constraint and a read-only intent forbids shell steps. It
// demonstrates context-aware goal derivation without touching the context.
type GuidedStrategy struct {
	base     PlanningStrategy
	root     string
	maxFiles int
}

// NewGuidedStrategy returns a context-guided strategy rooted at workspace.
func NewGuidedStrategy(workspace string, maxFiles int) *GuidedStrategy {
	return &GuidedStrategy{
		base:     NewPromptStrategy(),
		root:     workspace,
		maxFiles: maxFiles,
	}
}

// WithModel binds a goal generator to the underlying prompt strategy.
func (s *GuidedStrategy) WithModel(g GoalGenerator) *GuidedStrategy {
	if p, ok := s.base.(*PromptStrategy); ok {
		p.WithModel(g)
	}
	return s
}

// Name implements PlanningStrategy.
func (s *GuidedStrategy) Name() string { return "guided" }

// DetermineGoal implements PlanningStrategy.
func (s *GuidedStrategy) DetermineGoal(in intent.Intent, pc context.PlanningContext) (Goal, error) {
	base, err := s.base.DetermineGoal(in, pc)
	if err != nil {
		return Goal{}, err
	}

	var extra []Constraint
	if s.root != "" {
		extra = append(extra, Constraint{Kind: ConstraintPermittedRoot, Value: s.root})
	}
	if s.maxFiles > 0 {
		extra = append(extra, Constraint{Kind: ConstraintMaxSteps, Value: fmt.Sprintf("%d", s.maxFiles*4)})
	}
	if in.Has(intent.FacetReadOnly) {
		extra = append(extra, Constraint{Kind: ConstraintForbidShell, Value: "true"})
	}
	// Fold any permitted_root from the base so duplicates are avoided.
	merged := append([]Constraint(nil), base.Constraints()...)
	for _, c := range extra {
		duplicate := false
		for _, existing := range merged {
			if existing.Kind == c.Kind && existing.Value == c.Value {
				duplicate = true
				break
			}
		}
		if !duplicate {
			merged = append(merged, c)
		}
	}

	return NewGoal(base.Intent(),
		WithOutcome(base.Outcome()),
		WithTargets(base.Targets()...),
		WithNewFiles(base.NewFiles()...),
		WithConstraint(merged...),
		WithCriteria(base.Criteria()...),
	)
}
