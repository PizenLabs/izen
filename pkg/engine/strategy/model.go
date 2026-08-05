package strategy

import (
	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/intent"
)

// GoalGenerator is the optional model-backed seam of a strategy. When a
// strategy is bound to a generator it can delegate goal discovery — outcome
// wording, target selection, greenfield file enumeration — to a language
// model instead of pure string heuristics.
type GoalGenerator interface {
	// ModelName identifies the backing model for telemetry (for example
	// "gemma-4-26b").
	ModelName() string
	// GenerateGoal produces the model's view of the goal for a prompt.
	GenerateGoal(ctx stdctx.Context, req GoalRequest) (GoalResult, error)
}

// GoalRequest is the model-facing input of goal generation.
type GoalRequest struct {
	// Intent is the classified intent for the prompt.
	Intent intent.Intent
	// Prompt is the raw user prompt.
	Prompt string
	// Context is the assembled planning context rendered for the model.
	Context string
}

// GoalResult is the model's goal proposal.
type GoalResult struct {
	// Outcome is the natural-language statement of what success looks like.
	Outcome string
	// Targets are existing files/resources the goal will modify.
	Targets []string
	// NewFiles are files the goal will create (greenfield generation).
	NewFiles []string
	// Criteria are the verifiable success criteria.
	Criteria []string
}

// GoalGeneratorFunc adapts a plain function to GoalGenerator with an
// explicit model name.
type GoalGeneratorFunc struct {
	model string
	fn    func(stdctx.Context, GoalRequest) (GoalResult, error)
}

// NewGoalGeneratorFunc wraps a named model's generate function.
func NewGoalGeneratorFunc(modelName string, fn func(stdctx.Context, GoalRequest) (GoalResult, error)) GoalGeneratorFunc {
	return GoalGeneratorFunc{model: modelName, fn: fn}
}

// ModelName implements GoalGenerator.
func (g GoalGeneratorFunc) ModelName() string { return g.model }

// GenerateGoal implements GoalGenerator.
func (g GoalGeneratorFunc) GenerateGoal(ctx stdctx.Context, req GoalRequest) (GoalResult, error) {
	return g.fn(ctx, req)
}
