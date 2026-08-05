// Package decision implements the isolated Decision Engine of the adaptive
// control system. The Decision Engine is a THIN ORCHESTRATION CONTROLLER: a
// pure function over the Dynamic IR —
//
//	DecisionEngine.Decide(ExecutionSnapshot) → DecisionDirective
//
// It answers exactly one question — "what directive should be dispatched
// next?" — by sequencing the injected policy strategies in precedence order.
// Retry, budget and human-approval bounds live in injected strategy objects
// (RetryPolicy, BudgetPolicy, RePlanTrigger, HumanInTheLoopTrigger); the
// engine contains zero hardcoded retry, recovery or budget arithmetic. The
// state machine and the orchestrator contain no decision logic either; they
// only execute the directives the Decision Engine produces. Because the
// Decision Engine consumes the read-only SnapshotReader projection, it
// verifiably cannot mutate the Dynamic IR.
package decision

import (
	"context"
	"errors"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// Sentinel errors returned by decision policies.
var (
	// ErrContextCancelled is returned by Decide when the surrounding context
	// is cancelled.
	ErrContextCancelled = errors.New("decision: context cancelled")
)

// Directive is the explicit control command the Decision Engine produces. The
// orchestrator executes directives verbatim; it never interprets them.
type Directive string

const (
	// DirectiveContinue proceeds with the next dispatch batch and applies any
	// skip set produced for self-healed non-critical failures.
	DirectiveContinue Directive = "continue"
	// DirectiveRetry re-executes a failed node after the computed backoff.
	DirectiveRetry Directive = "retry"
	// DirectiveRePlan invalidates the static graph: live tool output proved the
	// plan can no longer be satisfied and a fresh plan is required.
	DirectiveRePlan Directive = "replan"
	// DirectiveHumanApproval pauses execution: the next action exceeds the
	// safety bounds and requires human authorization.
	DirectiveHumanApproval Directive = "human_approval"
	// DirectiveAbort terminates the control loop.
	DirectiveAbort Directive = "abort"
)

// String returns the machine-readable directive label.
func (d Directive) String() string { return string(d) }

// Decision is the pure output of the Decision Engine. It couples the Directive
// with the concrete execution data the orchestrator needs to act. A Decision
// never carries state — it is a command, and all state stays in the snapshot.
type Decision struct {
	// Directive is the control command to execute.
	Directive Directive
	// NodeID is the node the directive targets (Retry, RePlan,
	// HumanApproval). Empty for Continue and Abort.
	NodeID string
	// Dispatch lists the node ids to execute next (Continue).
	Dispatch []string
	// Skip lists failed non-critical node ids to absorb without re-planning
	// (Continue). The orchestrator transitions them to SUCCESS mechanically.
	Skip []string
	// Backoff is the delay to apply before a Retry dispatch.
	Backoff time.Duration
	// Reason is the human-readable rationale for the directive.
	Reason string
}

// DecisionEngine is the isolated control intelligence. It accepts the
// read-only Dynamic IR (the ExecutionSnapshot projection) and returns the
// explicit directive the orchestrator must execute. Implementations MUST NOT
// mutate the snapshot.
type DecisionEngine interface {
	// Decide inspects an ExecutionSnapshot and returns the DecisionDirective
	// the orchestrator must execute.
	Decide(ctx context.Context, snap ir.SnapshotReader) (Decision, error)
}
