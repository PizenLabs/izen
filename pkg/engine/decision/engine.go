package decision

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// Option configures a StandardDecisionEngine.
type Option func(*StandardDecisionEngine)

// WithRetryPolicy installs the retry bounds. When nil, retries are disabled.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(e *StandardDecisionEngine) { e.retry = p }
}

// WithBudgetPolicy installs the execution budget bounds. When nil, the budget
// is unlimited.
func WithBudgetPolicy(b BudgetPolicy) Option {
	return func(e *StandardDecisionEngine) { e.budget = b }
}

// WithRePlanTrigger installs the graph-invalidation policy.
func WithRePlanTrigger(t RePlanTrigger) Option {
	return func(e *StandardDecisionEngine) { e.replan = t }
}

// WithHumanInTheLoopTrigger installs the approval gate.
func WithHumanInTheLoopTrigger(t HumanInTheLoopTrigger) Option {
	return func(e *StandardDecisionEngine) { e.human = t }
}

// StandardDecisionEngine is the reference Decision Engine. It is a thin
// orchestration controller over the Dynamic IR: given an ExecutionSnapshot it
// produces exactly one explicit directive. It never mutates the snapshot and
// it never executes anything — execution is owned by the control loop.
//
// Every policy decision is delegated to an injected strategy object:
//
//   - RetryPolicy answers "may this failed node be retried?"
//   - BudgetPolicy answers "is there remaining execution budget?"
//   - RePlanTrigger answers "is the static graph invalidated?"
//   - HumanInTheLoopTrigger answers "does the next action need a human?"
//
// The engine contains zero hardcoded retry, recovery or budget arithmetic; it
// only sequences the questions in precedence order.
type StandardDecisionEngine struct {
	retry  RetryPolicy
	budget BudgetPolicy
	replan RePlanTrigger
	human  HumanInTheLoopTrigger
}

// NewStandardDecisionEngine returns a Decision Engine with the default retry
// budget, the unlimited default budget, the default re-plan trigger and the
// default approval gate.
func NewStandardDecisionEngine(opts ...Option) *StandardDecisionEngine {
	e := &StandardDecisionEngine{
		retry:  DefaultRetryBudget,
		budget: DefaultBudgetPolicy{},
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
//  2. Retry a retryable failed node when the injected RetryPolicy and the
//     injected BudgetPolicy both permit.
//  3. Skip every non-critical failed node (self-healing without re-planning).
//  4. A critical failed node with exhausted retries: RePlan when the trigger
//     invalidates the graph, otherwise Abort.
//  5. Dispatch the next ready batch, or Abort when the budget is exhausted.
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

	// 2. Cheap self-healing: retry a retryable failed node when the injected
	// policies permit. Deterministic probes (file checks, env probes, context)
	// are never retried — their observations are stable.
	for _, id := range snap.FailedNodes() {
		node, ok := snap.Node(id)
		if !ok || node == nil || !node.Kind.Retryable() {
			continue
		}
		attempts := snap.Attempts(id)
		if !e.retryPolicyAllows(attempts, id, snap) {
			continue
		}
		if !e.budgetAllows(snap) {
			break
		}
		return Decision{
			Directive: DirectiveRetry,
			NodeID:    id,
			Backoff:   e.backoffFor(id, attempts+1, snap),
			Reason:    fmt.Sprintf("node %s failed on attempt %d; retry allowed", id, attempts+1),
		}, nil
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
		if e.budgetAllows(snap) {
			return Decision{
				Directive: DirectiveContinue,
				Dispatch:  ready,
				Reason:    fmt.Sprintf("dispatching %d ready node(s)", len(ready)),
			}, nil
		}
		return Decision{
			Directive: DirectiveAbort,
			Reason:    "execution budget exhausted before the next dispatch",
		}, nil
	}

	// 6. Terminal.
	if snap.IsComplete() {
		return Decision{Directive: DirectiveContinue, Reason: "execution complete"}, nil
	}
	return Decision{Directive: DirectiveAbort, Reason: "no actionable nodes remain"}, nil
}

// retryPolicyAllows asks the injected RetryPolicy whether node id — after
// `attempts` executions — may be retried. The node's last observation is
// projected onto the policy's error parameter; observations with no
// machine-readable error yield a nil error.
func (e *StandardDecisionEngine) retryPolicyAllows(attempts int, id string, snap ir.SnapshotReader) bool {
	if e.retry == nil {
		return false
	}
	var retryErr error
	if obs, ok := snap.Observation(id); ok && obs.Err != "" {
		retryErr = errors.New(obs.Err)
	}
	return e.retry.ShouldRetry(attempts, retryErr)
}

// backoffFor asks the injected RetryPolicy (when it also implements
// BackoffProvider) for the delay before the given 1-based attempt. A policy
// that only implements RetryPolicy yields a zero backoff.
func (e *StandardDecisionEngine) backoffFor(_ string, nextAttempt int, _ ir.SnapshotReader) time.Duration {
	if bp, ok := e.retry.(BackoffProvider); ok {
		return bp.BackoffFor(nextAttempt)
	}
	return 0
}

// budgetAllows asks the injected BudgetPolicy whether the execution may keep
// going, feeding it the deterministic nominal token consumption already
// recorded by the Dynamic IR. A nil budget policy is treated as unlimited.
func (e *StandardDecisionEngine) budgetAllows(snap ir.SnapshotReader) bool {
	if e.budget == nil {
		return true
	}
	return e.budget.HasRemainingBudget(snapshotConsumption(snap))
}

// snapshotConsumption is a deterministic measurement of the work already
// consumed by the execution, expressed as a nominal token figure. It derives
// the figure from the Dynamic IR alone (observation output + failure text,
// ~4 chars per token) so the injected BudgetPolicy receives a stable,
// reproducible input. This is measurement, not policy — the BudgetPolicy owns
// the meaning of the number.
func snapshotConsumption(snap ir.SnapshotReader) int {
	if snap == nil || snap.StaticPlan() == nil || snap.StaticPlan().Graph == nil {
		return 0
	}
	var tokens int
	for _, id := range snap.StaticPlan().Graph.IDs() {
		if obs, ok := snap.Observation(id); ok {
			tokens += (len(obs.Output) + len(obs.Err)) / 4
		}
	}
	return tokens
}
