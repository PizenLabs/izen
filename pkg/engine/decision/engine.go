package decision

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// Option configures a StandardDecisionEngine.
type Option func(*StandardDecisionEngine)

// WithRetryPolicy installs the retry bounds.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(e *StandardDecisionEngine) { e.retry = p }
}

// WithRePlanTrigger installs the graph-invalidation policy.
func WithRePlanTrigger(t RePlanTrigger) Option {
	return func(e *StandardDecisionEngine) { e.replan = t }
}

// WithHumanInTheLoopTrigger installs the approval gate.
func WithHumanInTheLoopTrigger(t HumanInTheLoopTrigger) Option {
	return func(e *StandardDecisionEngine) { e.human = t }
}

// StandardDecisionEngine is the reference Decision Engine. It is a pure
// function over the Dynamic IR: given an ExecutionSnapshot it produces exactly
// one explicit directive. It never mutates the snapshot and it never executes
// anything — execution is owned by the control loop.
type StandardDecisionEngine struct {
	retry  RetryPolicy
	replan RePlanTrigger
	human  HumanInTheLoopTrigger
}

// NewStandardDecisionEngine returns a Decision Engine with the default retry
// bounds, the default re-plan trigger and the default approval gate.
func NewStandardDecisionEngine(opts ...Option) *StandardDecisionEngine {
	e := &StandardDecisionEngine{
		retry:  DefaultRetryPolicy,
		replan: DefaultRePlanTrigger{},
		human:  DefaultHumanInTheLoopTrigger{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Decide implements DecisionEngine. Precedence:
//
//  1. Human approval gate for the next ready action (safety bounds first).
//  2. Retry a retryable failed node whose budget is not exhausted.
//  3. Skip every non-critical failed node (self-healing without re-planning).
//  4. A critical failed node with exhausted retries: RePlan when the trigger
//     invalidates the graph, otherwise Abort.
//  5. Dispatch the next ready batch.
//  6. Terminal: Continue when complete, Abort when no node is actionable.
func (e *StandardDecisionEngine) Decide(ctx context.Context, snap ir.SnapshotReader) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}

	// 1. Safety bounds first: the next ready action may exceed the approved
	// scope and require a human in the loop.
	if e.human != nil {
		if need, why := e.human.RequiresApproval(snap); need {
			ready := snap.ReadyNodes()
			if len(ready) > 0 {
				return Decision{Directive: DirectiveHumanApproval, NodeID: ready[0], Reason: why}, nil
			}
		}
	}

	// 2. Cheap self-healing: retry a retryable failed node whose budget is not
	// exhausted. Deterministic probes (file checks, env probes, context) are
	// never retried — their observations are stable.
	for _, id := range snap.FailedNodes() {
		node, ok := snap.Node(id)
		if !ok || node == nil {
			continue
		}
		attempts := snap.Attempts(id)
		if node.Kind.Retryable() && !e.retry.Exhausted(attempts) {
			return Decision{
				Directive: DirectiveRetry,
				NodeID:    id,
				Backoff:   e.retry.BackoffFor(attempts + 1),
				Reason:    fmt.Sprintf("node %s failed on attempt %d; retry allowed", id, attempts+1),
			}, nil
		}
	}

	// 3. Non-critical failures are absorbed: the graph proceeds without any
	// LLM re-planning. This is the self-healing path (e.g. a missing optional
	// file).
	var skip []string
	for _, id := range snap.FailedNodes() {
		if node, ok := snap.Node(id); ok && node != nil && !node.Critical {
			skip = append(skip, id)
		}
	}
	if len(skip) > 0 {
		return Decision{
			Directive: DirectiveContinue,
			Skip:      skip,
			Reason:    fmt.Sprintf("skipping %d non-critical failed node(s) without re-planning", len(skip)),
		}, nil
	}

	// 4. Critical failures with exhausted retries invalidate the graph — a
	// fresh plan is required, or the loop aborts.
	for _, id := range snap.FailedNodes() {
		node, _ := snap.Node(id)
		if node == nil || !node.Critical {
			continue
		}
		if e.replan != nil {
			if ok, why := e.replan.ShouldRePlan(snap); ok {
				return Decision{Directive: DirectiveRePlan, NodeID: id, Reason: why}, nil
			}
		}
		return Decision{
			Directive: DirectiveAbort,
			NodeID:    id,
			Reason:    fmt.Sprintf("critical node %s failed and is not recoverable", id),
		}, nil
	}

	// 5. Dispatch the next ready batch.
	ready := snap.ReadyNodes()
	if len(ready) > 0 {
		return Decision{
			Directive: DirectiveContinue,
			Dispatch:  ready,
			Reason:    fmt.Sprintf("dispatching %d ready node(s)", len(ready)),
		}, nil
	}

	// 6. Terminal.
	if snap.IsComplete() {
		return Decision{Directive: DirectiveContinue, Reason: "execution complete"}, nil
	}
	return Decision{Directive: DirectiveAbort, Reason: "no actionable nodes remain"}, nil
}
