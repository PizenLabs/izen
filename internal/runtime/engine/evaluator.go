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
	eval := autonomy.EvaluateScope(autonomy.ScopeInput{
		Target:          ctx.Target,
		Content:         ctx.Content,
		MaxOutputTokens: ctx.MaxOutputTokens,
		Root:            ctx.Root,
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
