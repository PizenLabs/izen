package control

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/internal/runtime/substrate"
	"github.com/PizenLabs/izen/pkg/engine/decision"
	"github.com/PizenLabs/izen/pkg/engine/ir"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// Sentinel errors returned by the control loop.
var (
	// ErrNothingActionable is returned when the Decision Engine produced
	// Continue but no work could be dispatched and the graph is not complete —
	// a guard against a livelock.
	ErrNothingActionable = errors.New("control: no actionable nodes remain but execution is incomplete")
	// ErrDenied is returned when a human declined an approval request.
	ErrDenied = errors.New("control: human approval denied")
	// ErrAbortedByDecision is returned when the Decision Engine emitted Abort.
	ErrAbortedByDecision = errors.New("control: aborted by decision engine")
)

// ApprovalRequester asks the human operator to authorize a pending action.
type ApprovalRequester interface {
	// Require blocks until the operator authorizes or declines the action the
	// decision targets. It returns true when approved.
	Require(ctx context.Context, d decision.Decision) (bool, error)
}

// ApprovalFunc adapts a plain function to the ApprovalRequester interface.
type ApprovalFunc func(ctx context.Context, d decision.Decision) (bool, error)

// Require implements ApprovalRequester. A nil function declines every request.
func (f ApprovalFunc) Require(ctx context.Context, d decision.Decision) (bool, error) {
	if f == nil {
		return false, ErrDenied
	}
	return f(ctx, d)
}

// Result is the immutable outcome of a control loop run.
type Result struct {
	// RunID identifies the run.
	RunID string
	// Directive is the terminal directive (Continue for success; RePlan,
	// HumanApproval or Abort otherwise).
	Directive decision.Directive
	// Reason is the terminal rationale.
	Reason string
	// NodeStates is the terminal per-node state map.
	NodeStates map[string]ir.NodeState
	// Attempts is the total number of node executions across the run.
	Attempts int
	// StartedAt / EndedAt bound the run.
	StartedAt time.Time
	EndedAt   time.Time
	// Err is set when the loop aborted on an error.
	Err error
}

// Option configures a ControlLoopOrchestrator.
type Option func(*ControlLoopOrchestrator)

// WithApprovalRequester installs the human-in-the-loop gate. Nil (default)
// declines every request.
func WithApprovalRequester(a ApprovalRequester) Option {
	return func(o *ControlLoopOrchestrator) {
		if a != nil {
			o.approvals = a
		}
	}
}

// WithEventBus wires the fact-only telemetry bus. Nil (default) disables
// emission.
func WithEventBus(bus *telemetry.EventBus) Option {
	return func(o *ControlLoopOrchestrator) { o.bus = bus }
}

// WithClock overrides the loop clock (test seam).
func WithClock(now func() time.Time) Option {
	return func(o *ControlLoopOrchestrator) {
		if now != nil {
			o.now = now
		}
	}
}

// WithSubstrate wires the Substrate authority. When set, external patch
// generation is wrapped into a Proposal and passed to Substrate; session.Apply
// remains restricted to internal state/variable updates.
func WithSubstrate(s substrate.Substrate) Option {
	return func(o *ControlLoopOrchestrator) {
		if s != nil {
			o.substrate = s
		}
	}
}

// WithMaxIterations bounds the reconciliation iterations (livelock guard).
// Values <= 0 disable the bound.
func WithMaxIterations(n int) Option {
	return func(o *ControlLoopOrchestrator) {
		if n > 0 {
			o.maxIterations = n
		}
	}
}

// ControlLoopOrchestrator is the MINIMAL reconcile control loop:
//
//	loop {
//		snap := session.Observe()             // 1. Observe — read Dynamic IR
//		d := decisions.Decide(ctx, snap)      // 2. Decide — explicit directive
//		execute(d)                            // 3. Execute — through the pool
//	}
//
// It contains zero retry / re-plan / skip / approval policy; it only executes
// the directives of the Decision Engine and applies mechanical state
// transitions on the session. All work is dispatched strictly through the
// WorkerPool.
type ControlLoopOrchestrator struct {
	session       Session
	decisions     decision.DecisionEngine
	pool          *WorkerPool
	approvals     ApprovalRequester
	bus           *telemetry.EventBus
	substrate     substrate.Substrate
	now           func() time.Time
	maxIterations int
}

// NewControlLoopOrchestrator returns a control loop over the given session,
// decision engine and worker pool.
func NewControlLoopOrchestrator(session Session, decisions decision.DecisionEngine, pool *WorkerPool, opts ...Option) *ControlLoopOrchestrator {
	o := &ControlLoopOrchestrator{
		session:   session,
		decisions: decisions,
		pool:      pool,
		approvals: ApprovalFunc(nil),
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

var runIDCounter atomic.Uint64

func newRunID() string {
	return fmt.Sprintf("run-%d", runIDCounter.Add(1))
}

// Run drives the closed loop to a terminal directive. It returns the Result;
// a non-nil error accompanies an Abort that ended on an error.
func (o *ControlLoopOrchestrator) Run(ctx context.Context) (*Result, error) {
	started := o.now()
	runID := newRunID()

	for iteration := 0; ; iteration++ {
		if err := ctx.Err(); err != nil {
			return o.terminate(runID, started, decision.DirectiveAbort, "context cancelled", err)
		}
		if o.maxIterations > 0 && iteration >= o.maxIterations {
			return o.terminate(runID, started, decision.DirectiveAbort, "iteration budget exhausted", ErrNothingActionable)
		}

		// 1. Observe — the only truth the loop reads between iterations.
		snap := o.session.Observe()
		o.emitIteration(runID, snap)
		if snap.IsComplete() {
			return o.terminate(runID, started, decision.DirectiveContinue, "execution complete", nil)
		}

		// 2. Decide — the isolated Decision Engine reads the snapshot and
		// returns an explicit directive.
		d, err := o.decisions.Decide(ctx, snap)
		if err != nil {
			return o.terminate(runID, started, decision.DirectiveAbort, fmt.Sprintf("decision error: %v", err), err)
		}

		// 3. Execute — act on the directive through the worker pool.
		switch d.Directive {
		case decision.DirectiveContinue:
			// A skip directive absorbs non-critical failures and may unblock
			// dependents; apply it and reconcile again.
			if len(d.Skip) > 0 {
				for _, id := range d.Skip {
					if err := o.session.Skip(id, "non-critical failure absorbed by decision engine"); err != nil {
						return o.terminate(runID, started, decision.DirectiveAbort, err.Error(), err)
					}
				}
				continue
			}
			if len(d.Dispatch) == 0 {
				if o.session.Observe().IsComplete() {
					return o.terminate(runID, started, decision.DirectiveContinue, "execution complete", nil)
				}
				return o.terminate(runID, started, decision.DirectiveAbort, "no actionable nodes remain but execution is incomplete", ErrNothingActionable)
			}
			if err := o.dispatch(ctx, runID, d.Dispatch); err != nil {
				return o.terminate(runID, started, decision.DirectiveAbort, err.Error(), err)
			}
		case decision.DirectiveRetry:
			if err := o.session.ResetForRetry(d.NodeID); err != nil {
				return o.terminate(runID, started, decision.DirectiveAbort, err.Error(), err)
			}
			if d.Backoff > 0 {
				waitFor(ctx, d.Backoff)
			}
			if err := o.dispatch(ctx, runID, []string{d.NodeID}); err != nil {
				return o.terminate(runID, started, decision.DirectiveAbort, err.Error(), err)
			}
		case decision.DirectiveHumanApproval:
			approved, err := o.approvals.Require(ctx, d)
			if err != nil {
				return o.terminate(runID, started, decision.DirectiveAbort, fmt.Sprintf("approval error: %v", err), err)
			}
			if !approved {
				return o.terminate(runID, started, decision.DirectiveAbort, "human approval denied", ErrDenied)
			}
			if err := o.dispatch(ctx, runID, []string{d.NodeID}); err != nil {
				return o.terminate(runID, started, decision.DirectiveAbort, err.Error(), err)
			}
		case decision.DirectiveRePlan:
			return o.terminate(runID, started, decision.DirectiveRePlan, d.Reason, nil)
		case decision.DirectiveAbort:
			return o.terminate(runID, started, decision.DirectiveAbort, d.Reason, ErrAbortedByDecision)
		}
	}
}

// dispatch executes the given nodes strictly through the worker pool and folds
// the observations back into the session. session.Apply is restricted to
// internal state/variable updates; any external patch generation is wrapped
// into a Proposal and passed to Substrate.
func (o *ControlLoopOrchestrator) dispatch(ctx context.Context, runID string, nodeIDs []string) error {
	snap := o.session.Observe()
	vars := cloneVars(snap.Variables)
	if err := o.session.MarkRunning(nodeIDs); err != nil {
		return err
	}
	nodes, err := o.session.Plan().Graph.Select(nodeIDs)
	if err != nil {
		return err
	}
	items := make([]WorkItem, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, WorkItem{Node: n, Vars: vars})
	}
	for obs := range o.pool.Submit(ctx, items) {
		o.emitNodeObserved(runID, obs)
		// External patch generation must be wrapped into a Proposal when
		// Substrate is wired; the execution is via Substrate only.
		if o.substrate != nil && len(obs.FileMutations) > 0 {
			prop := proposalForMutations(obs)
			if _, err := o.substrate.Execute(ctx, prop); err != nil {
				return err
			}
			// After substrate commit, still fold the observation for state
			// tracking; Apply remains restricted to internal state.
		}
		if err := o.session.Apply(obs); err != nil {
			return err
		}
	}
	return nil
}

func proposalForMutations(obs ir.ObservationPayload) substrate.Proposal {
	ops := make([]substrate.Operation, 0, len(obs.FileMutations))
	for _, m := range obs.FileMutations {
		switch m.Action {
		case ir.ActionCreated, ir.ActionModified:
			ops = append(ops, substrate.Operation{Type: substrate.OpFileWrite, Target: m.Path, Content: []byte(m.Content)})
		case ir.ActionDeleted:
			ops = append(ops, substrate.Operation{Type: substrate.OpFileDelete, Target: m.Path})
		default:
			ops = append(ops, substrate.Operation{Type: substrate.OpFileWrite, Target: m.Path, Content: []byte(m.Content)})
		}
	}
	return substrate.Proposal{ID: fmt.Sprintf("control-%s", obs.NodeID), Intent: obs.NodeID, Operations: ops}
}

// terminate records the final facts, publishes the termination event and
// returns the immutable Result.
func (o *ControlLoopOrchestrator) terminate(runID string, started time.Time, d decision.Directive, reason string, err error) (*Result, error) {
	ended := o.now()
	snap := o.session.Observe()
	res := &Result{
		RunID:      runID,
		Directive:  d,
		Reason:     reason,
		NodeStates: snap.StateMap(),
		Attempts:   totalAttempts(snap.AttemptCounts),
		StartedAt:  started,
		EndedAt:    ended,
		Err:        err,
	}
	if o.bus != nil {
		o.bus.Publish(telemetry.NewControlTerminated(runID, d.String(), snap.StateMap(), res.Attempts, ended.Sub(started)))
	}
	return res, err
}

// emitIteration publishes the raw Dynamic IR facts of one loop iteration.
func (o *ControlLoopOrchestrator) emitIteration(runID string, snap *ir.ExecutionSnapshot) {
	if o.bus == nil {
		return
	}
	attempts := make(map[string]int, len(snap.AttemptCounts))
	for k, v := range snap.AttemptCounts {
		attempts[k] = v
	}
	o.bus.Publish(telemetry.NewControlIteration(runID, snap.StateMap(), attempts))
}

// emitNodeObserved publishes a single node observation fact.
func (o *ControlLoopOrchestrator) emitNodeObserved(runID string, obs ir.ObservationPayload) {
	if o.bus == nil {
		return
	}
	o.bus.Publish(telemetry.NewControlNodeObserved(runID, obs))
}

// waitFor waits for the backoff window or context cancellation.
func waitFor(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// totalAttempts sums the per-node attempt counts.
func totalAttempts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// cloneVars returns a defensive copy of a variable surface.
func cloneVars(v ir.Variables) ir.Variables {
	out := make(ir.Variables, len(v))
	for k, val := range v {
		out[k] = val
	}
	return out
}
