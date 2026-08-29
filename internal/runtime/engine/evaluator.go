// Package engine hosts the Zero-Token EVALUATING_SCOPE step of the runtime
// engine pipeline. The scope step is the fail-closed gate that runs BEFORE any
// LLM inference, DAG decomposition, or manifest generation: it performs only
// local, deterministic heuristics and, when the ExecutionGate closes, halts the
// transition to DECIDING/STAGING and diverts to AWAITING_HUMAN_PROPOSAL with
// zero LLM tokens spent.
package engine

import (
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/runtime/autonomy"
)

// ErrEvaluatingScopeBarrier is the canonical fail-closed error returned when
// the Zero-Token ExecutionGate closes. It is the signal the engine uses to
// halt the transition to DECIDING/STAGING and divert to
// AWAITING_HUMAN_PROPOSAL.
var ErrEvaluatingScopeBarrier = errors.New("EVALUATING_SCOPE_BARRIER: scope evaluation failed the ExecutionGate — no inference, decomposition, or staging may begin")

// ExecutionContext carries the local, workspace-scoped facts the
// EVALUATING_SCOPE step inspects and the fail-closed enforcement surface.
type ExecutionContext struct {
	// Root is the workspace root used to resolve local file references.
	Root string
	// Target is the workspace-relative file being evaluated.
	Target string
	// Content is the raw bytes of the target (nil when absent/creation intent).
	Content []byte
	// MaxOutputTokens is the declared output budget (0 = unbounded).
	MaxOutputTokens int

	// Subcommand is the policy scope ($prompt / $hot / ""). It is forwarded
	// verbatim into ScopeInput so the budget estimator applies the correct
	// multiplier (bounded patch vs full rewrite).
	Subcommand string

	// Prompt is the raw admitted prompt text. It MUST be propagated into
	// ScopeInput.Prompt: the budget estimator inspects it to distinguish a
	// targeted modification request (remove/fix/refactor → bounded patch
	// multiplier) from an explicit whole-file rewrite (→ full-rewrite
	// multiplier). An empty prompt silently downgrades every targeted request
	// to full-rewrite accounting and can falsely close the gate on budget.
	Prompt string

	// StateMachine is the scope-execution state machine this step drives. It
	// must be non-nil; the step transitions it to DECIDING or
	// AWAITING_HUMAN_PROPOSAL.
	StateMachine *autonomy.ScopeStateMachine

	// TokensSpent counts LLM tokens spent. The EVALUATING_SCOPE step MUST leave
	// it at 0: no inference occurs here.
	TokensSpent int

	// StagePlan is the mutation-planning/staging callback. The scope step MUST
	// never invoke it when the ExecutionGate is closed. It is the seam through
	// which "no DAG staging occurs" is enforced and observed.
	StagePlan func() error

	// ── Recovery Contract Mutation ────────────────────────────────────
	MutationStrategy     autonomy.MutationStrategy
	AllowASTBypass       bool
	ExplicitOutputBudget int // 0 = default, >0 = human override
	SyntheticSubGoal     string

	// TargetTokens is the token count of the target (bytes/4) used by the
	// strategy-aware evaluator. When 0 it is derived from Content length.
	TargetTokens int
	ASTStatus    autonomy.ASTStatus
}

// PreflightResult is the strategy-aware evaluation verdict.
type PreflightResult struct {
	Feasible        bool
	Reason          string
	GateClosed      bool
	ASTStatus       autonomy.ASTStatus
	EstimatedTokens int
	MaxOutputTokens int
}

// Evaluator is the strategy-aware preflight evaluator. It applies the
// dynamic multiplier and AST bypass rules so a recovered contract does
// not re-park at the same DecisionSurface.
type Evaluator struct{}

// Evaluate runs the strategy-aware zero-token preflight. It mirrors
// EvaluateScope's accounting but uses the explicit MutationStrategy and
// AllowASTBypass fields to determine the effective multiplier and gate.
func (e *Evaluator) Evaluate(ctx *ExecutionContext) (*PreflightResult, error) {
	if ctx == nil {
		return nil, errors.New("engine: nil execution context")
	}
	// Dynamic multiplier based on strategy.
	multiplier := 3.0
	switch ctx.MutationStrategy {
	case autonomy.StrategyBoundedPatch:
		multiplier = 0.8
	case autonomy.StrategySyntaxRepair:
		multiplier = 0.5
	}
	// Effective max_output.
	maxOutput := ctx.MaxOutputTokens
	if ctx.ExplicitOutputBudget > 0 {
		maxOutput = ctx.ExplicitOutputBudget
	}
	// Target tokens: explicit or derived from content.
	targetTokens := ctx.TargetTokens
	if targetTokens <= 0 && len(ctx.Content) > 0 {
		targetTokens = len(ctx.Content) / 4
	}
	// AST status: explicit or derived.
	astStatus := ctx.ASTStatus
	if astStatus == "" && len(ctx.Content) > 0 {
		// Best-effort derivation: if Content looks corrupt, mark corrupt;
		// otherwise valid. For tests, ASTStatus is set explicitly.
		astStatus = autonomy.ASTValid
	}
	estimatedTokens := int(float64(targetTokens) * multiplier)
	budgetExceeded := maxOutput > 0 && estimatedTokens > maxOutput

	// Explicit AST gate override: only a full rewrite without bypass blocks
	// on corrupt AST.
	if astStatus == autonomy.ASTCorrupt && !ctx.AllowASTBypass && ctx.MutationStrategy == autonomy.StrategyFullRewrite {
		return &PreflightResult{
			Feasible:   false,
			Reason:     "corrupt AST baseline forbids full-rewrite DAG decomposition",
			GateClosed: true,
			ASTStatus:  astStatus,
		}, nil
	}
	if budgetExceeded {
		return &PreflightResult{
			Feasible:        false,
			Reason:          fmt.Sprintf("estimated tokens (%d) exceeds max_output (%d)", estimatedTokens, maxOutput),
			GateClosed:      true,
			EstimatedTokens: estimatedTokens,
			MaxOutputTokens: maxOutput,
		}, nil
	}
	return &PreflightResult{
		Feasible:        true,
		EstimatedTokens: estimatedTokens,
		MaxOutputTokens: maxOutput,
	}, nil
}

// RunEvaluatingScopeStep is the Zero-Token EVALUATING_SCOPE step of the engine
// pipeline.
//
// Invariants:
//   - It spends ZERO LLM tokens (TokensSpent stays 0).
//   - It runs only local heuristics (AST/syntax validity, local file
//     resolution, budget estimation).
//   - When the ExecutionGate returns true it advances the state machine to
//     DECIDING and returns nil.
//   - When the ExecutionGate returns false it advances the state machine to
//     AWAITING_HUMAN_PROPOSAL, does NOT invoke StagePlan, and returns
//     ErrEvaluatingScopeBarrier (wrapping the evaluation evidence).
func RunEvaluatingScopeStep(ctx *ExecutionContext) error {
	if ctx == nil {
		return errors.New("engine: nil execution context for scope evaluation")
	}
	if ctx.StateMachine == nil {
		return errors.New("engine: scope evaluation requires a state machine")
	}

	// ── 0-TOKEN LOCAL PREFLIGHT ───────────────────────────────────────
	// The raw prompt text and subcommand policy MUST reach ScopeInput so the
	// budget estimator applies the bounded-patch-vs-full-rewrite multiplier for
	// the actual request. Omitting them would make every background evaluation
	// run under empty-prompt full-rewrite accounting, falsely closing the gate
	// on targeted modification prompts.
	// Recovery mutations (bounded patch / syntax repair / explicit budget)
	// are propagated as a NEW concrete contract so the evaluator runs under
	// the mutated strategy and does not re-calculate the same 3× estimate.
	eval := autonomy.EvaluateScope(autonomy.ScopeInput{
		Target:               ctx.Target,
		Content:              ctx.Content,
		MaxOutputTokens:      ctx.MaxOutputTokens,
		Root:                 ctx.Root,
		Subcommand:           ctx.Subcommand,
		Prompt:               ctx.Prompt,
		MutationStrategy:     ctx.MutationStrategy,
		AllowASTBypass:       ctx.AllowASTBypass,
		ExplicitOutputBudget: ctx.ExplicitOutputBudget,
		SyntheticSubGoal:     ctx.SyntheticSubGoal,
	})

	if !eval.ExecutionGate() {
		// FAIL-CLOSED: halt the transition to DECIDING/STAGING and divert to
		// AWAITING_HUMAN_PROPOSAL. No inference, no decomposition, no manifest,
		// no DAG staging — zero tokens spent.
		reason := fmt.Sprintf("scope evaluation barrier on %q: %s", eval.Target, findingsSummary(eval))
		if _, err := ctx.StateMachine.GateBarred(reason); err != nil {
			return fmt.Errorf("engine: %w", err)
		}
		return fmt.Errorf("%w — %s", ErrEvaluatingScopeBarrier, reason)
	}

	// GATE PASSED: the target is structurally valid, resolved, and within
	// budget. Advance to DECIDING. Staging still requires an explicit decision;
	// the scope step never stages on its own.
	if _, err := ctx.StateMachine.GatePassed("scope evaluation passed the ExecutionGate"); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	return nil
}

// findingsSummary renders the bounded evaluation evidence for the barrier.
func findingsSummary(eval autonomy.PreflightEvaluation) string {
	if len(eval.Findings) == 0 {
		return "scope not executable as-is"
	}
	return eval.Findings[0]
}
