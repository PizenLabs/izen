package decision

import (
	"fmt"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// BackoffFunc computes the backoff delay for a 1-based retry attempt.
type BackoffFunc func(attempt int) time.Duration

// DefaultMaxAttempts bounds automatic retries when no policy is configured.
const DefaultMaxAttempts = 3

// RetryPolicy decides whether a failed node should be retried. It is a pure
// function over the attempt history and the failure that produced it; the
// Decision Engine delegates every retry bound to an injected RetryPolicy and
// never performs retry arithmetic itself. The retry DECISION (whether the
// snapshot warrants a retry at all) lives in the Decision Engine; this policy
// only bounds it.
type RetryPolicy interface {
	// ShouldRetry reports whether a further retry is allowed after the given
	// number of attempts have already been made. attempt is the count of
	// executions so far; err is the failure that triggered the retry check
	// (it may be nil when the observation carried no machine-readable error).
	ShouldRetry(attempt int, err error) bool
}

// BackoffProvider is implemented by RetryPolicy strategies that also compute a
// backoff schedule. The Decision Engine consults it (via a type assertion)
// when assembling a Retry directive; a policy that does not implement it
// yields a zero backoff.
type BackoffProvider interface {
	// BackoffFor returns the backoff delay before retry attempt number attempt
	// (1-based).
	BackoffFor(attempt int) time.Duration
}

// RetryBudget is the reference RetryPolicy strategy: it bounds automatic
// retries by a maximum attempt count with an optional backoff schedule. It is
// a pure data strategy — it holds no state and never touches the snapshot.
type RetryBudget struct {
	// MaxAttempts is the total execution budget per node, including the first
	// attempt. A node with MaxAttempts = 1 is never retried.
	MaxAttempts int
	// Backoff computes the delay before a retry attempt.
	Backoff BackoffFunc
}

// DefaultRetryBudget allows up to DefaultMaxAttempts total attempts per node
// with an exponential backoff capped at 8s.
var DefaultRetryBudget = RetryBudget{
	MaxAttempts: DefaultMaxAttempts,
	Backoff: func(attempt int) time.Duration {
		d := 100 * time.Millisecond
		for i := 1; i < attempt; i++ {
			d *= 2
			if d > 8*time.Second {
				d = 8 * time.Second
				break
			}
		}
		return d
	},
}

// DefaultRetryPolicy is retained for backward compatibility. It is the same
// strategy as DefaultRetryBudget.
var DefaultRetryPolicy = DefaultRetryBudget

// ShouldRetry implements RetryPolicy.
func (p RetryBudget) ShouldRetry(attempt int, _ error) bool {
	if p.MaxAttempts <= 0 {
		return false
	}
	return attempt < p.MaxAttempts
}

// Exhausted reports whether a node's execution budget is spent, given the
// number of attempts already made.
func (p RetryBudget) Exhausted(attempts int) bool {
	if p.MaxAttempts <= 0 {
		return true
	}
	return attempts >= p.MaxAttempts
}

// BackoffFor implements BackoffProvider.
func (p RetryBudget) BackoffFor(attempt int) time.Duration {
	if p.Backoff == nil {
		return 0
	}
	return p.Backoff(attempt)
}

// BudgetPolicy decides whether the execution still has remaining budget to
// keep going. The Decision Engine delegates every budget bound to an injected
// BudgetPolicy; it never performs budget arithmetic itself.
type BudgetPolicy interface {
	// HasRemainingBudget reports whether the execution may continue given the
	// nominal token consumption already recorded by the Dynamic IR.
	HasRemainingBudget(tokens int) bool
}

// DefaultBudgetPolicy is the reference BudgetPolicy strategy. A zero MaxTokens
// means the budget is unlimited — every request passes.
type DefaultBudgetPolicy struct {
	// MaxTokens is the ceiling on the nominal token consumption recorded by
	// the snapshot. Zero means unlimited.
	MaxTokens int
}

// HasRemainingBudget implements BudgetPolicy.
func (p DefaultBudgetPolicy) HasRemainingBudget(tokens int) bool {
	if p.MaxTokens <= 0 {
		return true
	}
	return tokens < p.MaxTokens
}

// RePlanTrigger decides when live tool output fundamentally invalidates the
// static graph. When it fires, the Decision Engine emits DirectiveRePlan and
// the orchestrator halts execution so a fresh plan can be produced.
type RePlanTrigger interface {
	// ShouldRePlan inspects the snapshot and reports whether the graph is
	// invalidated, along with the rationale.
	ShouldRePlan(snap ir.SnapshotReader) (bool, string)
}

// DefaultRePlanTrigger re-plans when any failed observation carries a graph
// invalidation environment signal. A plain critical failure without an
// invalidation signal is NOT a re-plan — it is an Abort (or a Retry when the
// budget allows).
type DefaultRePlanTrigger struct{}

// ShouldRePlan implements RePlanTrigger.
func (DefaultRePlanTrigger) ShouldRePlan(snap ir.SnapshotReader) (bool, string) {
	for _, id := range snap.FailedNodes() {
		obs, ok := snap.Observation(id)
		if !ok {
			continue
		}
		for _, sig := range obs.EnvSignals {
			if sig.Kind == ir.SignalGraphInvalidation {
				return true, fmt.Sprintf("node %s emitted graph invalidation signal %q", id, sig.Name)
			}
		}
	}
	return false, ""
}

// Compile-time assertions that the reference strategies satisfy the injected
// policy contracts.
var (
	_ RetryPolicy           = RetryBudget{}
	_ BackoffProvider       = RetryBudget{}
	_ BudgetPolicy          = DefaultBudgetPolicy{}
	_ RePlanTrigger         = DefaultRePlanTrigger{}
	_ HumanInTheLoopTrigger = DefaultHumanInTheLoopTrigger{}
)

// HumanInTheLoopTrigger decides when the next action exceeds the safety bounds
// (a destructive request, a mutation beyond the pre-declared scope) and must
// be authorized by a human before execution proceeds.
type HumanInTheLoopTrigger interface {
	// RequiresApproval inspects the snapshot and reports whether the next
	// ready action requires human authorization.
	RequiresApproval(snap ir.SnapshotReader) (bool, string)
}

// DefaultHumanInTheLoopTrigger requires approval for any ready node explicitly
// marked RequiresApproval on the static graph.
type DefaultHumanInTheLoopTrigger struct{}

// RequiresApproval implements HumanInTheLoopTrigger.
func (DefaultHumanInTheLoopTrigger) RequiresApproval(snap ir.SnapshotReader) (bool, string) {
	for _, id := range snap.ReadyNodes() {
		node, ok := snap.Node(id)
		if !ok || node == nil {
			continue
		}
		if node.RequiresApproval {
			return true, fmt.Sprintf("node %s (%s) requests a destructive action", id, node.Kind)
		}
	}
	return false, ""
}
