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

// RetryPolicy bounds automatic retries with a backoff schedule. It answers two
// questions: is a further retry allowed, and how long to wait before it. The
// retry DECISION (whether the snapshot warrants a retry at all) lives in the
// Decision Engine; this policy only bounds it.
type RetryPolicy struct {
	// MaxAttempts is the total execution budget per node, including the first
	// attempt. A node with MaxAttempts = 1 is never retried.
	MaxAttempts int
	// Backoff computes the delay before a retry attempt.
	Backoff BackoffFunc
}

// DefaultRetryPolicy allows up to DefaultMaxAttempts total attempts per node
// with an exponential backoff capped at 8s.
var DefaultRetryPolicy = RetryPolicy{
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

// Exhausted reports whether a node's execution budget is spent, given the
// number of attempts already made.
func (p RetryPolicy) Exhausted(attempts int) bool {
	if p.MaxAttempts <= 0 {
		return true
	}
	return attempts >= p.MaxAttempts
}

// BackoffFor returns the backoff delay before retry attempt number attempt
// (1-based).
func (p RetryPolicy) BackoffFor(attempt int) time.Duration {
	if p.Backoff == nil {
		return 0
	}
	return p.Backoff(attempt)
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
