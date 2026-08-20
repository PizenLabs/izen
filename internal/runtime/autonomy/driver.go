package autonomy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
)

// Decider maps a bounded observation to the next loop decision. It is
// injectable for policy tests; the default uses the canonical recovery matrix.
type Decider func(o autonomy.Observation, b autonomy.LoopBounds) autonomy.LoopDecision

// RepairFunc re-scopes a failed request before a bounded re-execution. It is
// injectable; the default only appends the failed outcome as evidence.
type RepairFunc func(o autonomy.Observation, req autonomy.LoopRequest) (autonomy.LoopRequest, error)

// Driver is the real autonomous loop: it owns the bounded control flow over
// the RuntimeExecutor (through the ExecutorAdapter) and publishes every
// transition as a canonical loop.transition event on the shared bus. It is a
// single-lane control flow: exactly one loop per Driver, not safe for
// concurrent use.
//
// Flow: resolve target (gateway) → observe → decide → execute (adapter) →
// verify → interpret → complete/recover/abort/park. The loop only drives; the
// executor executes; the human approves/clarifies; the loop NEVER mutates the
// filesystem or invokes a provider itself.
type Driver struct {
	adapter   *ExecutorAdapter
	bus       *events.Bus
	bounds    autonomy.LoopBounds
	decide    Decider
	repair    RepairFunc
	loop      *autonomy.RuntimeLoop
	prompt    string
	resolved  Resolved
	req       autonomy.LoopRequest
	obs       autonomy.Observation
	published int

	// runCtx is the context for the current run. It is stored so Abort()
	// can cancel the same context that observeAndRun is watching.
	runCtx context.Context
	// runCancel cancels runCtx. Set when a run starts, nil when terminal.
	runCancel context.CancelFunc
	// runID is a monotonically increasing identity for each run.
	// Late results from a previous run cannot overwrite a newer run's state.
	runID uint64
}

// Option configures the Driver during construction.
type Option func(*Driver)

// WithLoopBounds overrides the runtime-owned termination bounds.
func WithLoopBounds(b autonomy.LoopBounds) Option {
	return func(d *Driver) { d.bounds = b }
}

// WithDecider overrides the observation → decision policy.
func WithDecider(dec Decider) Option {
	return func(d *Driver) {
		if dec != nil {
			d.decide = dec
		}
	}
}

// WithRepair overrides the bounded recovery re-scope.
func WithRepair(f RepairFunc) Option {
	return func(d *Driver) {
		if f != nil {
			d.repair = f
		}
	}
}

// NewDriver wires the bounded loop over the executor adapter. bus may be nil
// (loop runs headless; transitions are still recorded in History).
func NewDriver(adapter *ExecutorAdapter, bus *events.Bus, opts ...Option) *Driver {
	d := &Driver{
		adapter: adapter,
		bus:     bus,
		bounds:  autonomy.DefaultLoopBounds(),
		decide:  decideDefault,
		repair:  defaultRepair,
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Run starts a fresh bounded run for the objective and drives it until the loop
// terminates or parks at a human boundary. A parked loop returns a nil
// termination with a non-nil Boundary(); resume via ResumeApprove/Reject/
// Clarify. A completed or aborted run returns its terminal outcome.
func (d *Driver) Run(ctx context.Context, objective string) (*autonomy.LoopTermination, error) {
	if d.adapter == nil {
		return nil, errors.New("autonomy: driver requires an executor adapter")
	}
	// ── DUPLICATE-START PROTECTION (§18 / §21-I) ──────────────────────
	// Exactly one run at a time: a second Run while the current loop is still
	// active (executing) OR parked (awaiting human) must never silently
	// clobber the parked approval/decision state. A fresh Run is legal only
	// after the previous run reached a terminal state.
	if d.loop != nil && !d.loop.State().IsTerminal() {
		return d.term(), errors.New("autonomy: a run is already active or parked at a human boundary — resume or abort it first")
	}

	// Create a new run context with cancellation for this run.
	// This context is the single cancellation authority for the run.
	d.runCtx, d.runCancel = context.WithCancel(ctx)
	d.runID++
	runID := d.runID

	d.loop = autonomy.NewRuntimeLoop(d.bounds)
	d.prompt = objective
	d.obs = autonomy.Observation{}
	d.published = 0
	d.loop.Start("user objective: " + objective)
	d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context

	// Target resolution is the gateway's deterministic authority; the loop
	// never guesses a target. A clarification boundary parks BEFORE any
	// execution — no model call, no mutation.
	d.resolved = d.adapter.Resolve(objective)
	d.req = autonomy.LoopRequest{Prompt: objective, Targets: d.resolved.Targets}
	if d.resolved.Ambiguous {
		d.loop.AwaitHuman(autonomy.HumanBoundary{
			Reason:  "target clarification required before any execution",
			Options: d.resolved.Options,
		})
		d.enrichBoundary()
		d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context
		return d.term(), nil
	}
	d.obs = d.contextObservation()
	term, err := d.observeAndRun(d.runCtx, runID) //nolint:contextcheck // runCtx is the run's own cancellation context
	// Clear run context on terminal completion; preserve when parked.
	if term != nil && term.State.IsTerminal() {
		d.runCtx = nil
		d.runCancel = nil
	}
	return term, err
}

// Abort terminates the current parked or active run as a permanent human
// cancellation. It is the ONLY way to cancel a run that is already parked at
// a human boundary (an in-flight run is cancelled via its context). After
// Abort the loop is terminal and a fresh Run may start. Aborting a loop that
// has not started or is already terminal is a no-op.
func (d *Driver) Abort(reason string) (*autonomy.LoopTermination, error) {
	if d.loop == nil {
		return nil, errors.New("autonomy: abort requires a started run")
	}
	if d.loop.State().IsTerminal() {
		return d.term(), nil
	}
	// Cancel the run context so the in-flight observeAndRun/execute sees it.
	if d.runCancel != nil {
		d.runCancel()
	}
	// Use the run context (now cancelled) for termination so the termination
	// event carries the correct cancellation context.
	term := d.terminateAbort(d.runCtx, "aborted by operator: "+reason, autonomy.FailurePermanent)
	// Clear the run context so a fresh Run can start.
	d.runCtx = nil
	d.runCancel = nil
	return term, nil
}

// ResumeApprove resolves a parked approval gate: it approves the held patch
// through the executor and INTERPRETS the terminal result of the SAME
// execution. It never re-executes the mutation (idempotency).
//
// Convergence: the approval decision converges to exactly ONE terminal
// outcome. A successful apply completes the loop; a failed apply/verify
// aborts it permanently — the loop NEVER auto-repairs an approved proposal
// into a second provider invocation (the human approved THIS patch, not a
// regeneration). A hard approve error (no result, e.g. double-approve)
// aborts the run so it can never park at a stale awaiting_human.
func (d *Driver) ResumeApprove(ctx context.Context) (*autonomy.LoopTermination, error) {
	pid, err := d.approvalPatchID()
	if err != nil {
		return d.term(), err
	}
	obs, err := d.adapter.Approve(ctx, pid)
	if obs.RequestID == "" {
		// Hard approve error: no terminal result exists (the held patch is
		// gone). Release the human and converge to a permanent abort so the
		// run never sits at a stale awaiting_human.
		reason := "approval failed: patch no longer held by the executor"
		if err != nil {
			reason = "approval failed: " + err.Error()
		}
		if d.loop != nil && !d.loop.State().IsTerminal() {
			d.loop.ReleaseHuman(reason)
			d.publish(ctx)
			term := d.terminateAbort(ctx, reason, autonomy.FailurePermanent)
			d.runCtx, d.runCancel = nil, nil
			return term, nil
		}
		return d.term(), nil
	}
	d.obs = obs
	d.loop.ReleaseHuman("patch approved")
	d.publish(ctx)
	if approvalFailureOutcome(obs) {
		// The approved proposal failed to apply/verify. This is a terminal
		// human-decision outcome: converge to ONE aborted terminal state and
		// NEVER re-execute (a repair would regenerate a patch the human did
		// not see). err, when set, is surfaced via the termination reason.
		reason := "approved mutation failed: " + string(obs.Outcome)
		if err != nil {
			reason += ": " + err.Error()
		}
		term := d.terminateAbort(ctx, reason, autonomy.FailurePermanent)
		d.runCtx, d.runCancel = nil, nil
		return term, nil
	}
	d.runID++
	term, err := d.observeAndRun(ctx, d.runID)
	if term != nil && term.State.IsTerminal() {
		d.runCtx = nil
		d.runCancel = nil
	}
	return term, err
}

// ResumeReject resolves a parked approval gate by rejecting the held patch
// through the executor; the rejection is a terminal human decision, never a
// re-execution.
func (d *Driver) ResumeReject(ctx context.Context, reason string) (*autonomy.LoopTermination, error) {
	pid, err := d.approvalPatchID()
	if err != nil {
		return d.term(), err
	}
	obs, err := d.adapter.Reject(ctx, pid, reason)
	if obs.RequestID == "" {
		// Hard reject error: no terminal result exists. Release the human and
		// converge to a permanent abort so the run never parks at a stale
		// awaiting_human.
		r := "rejection failed: patch no longer held by the executor"
		if err != nil {
			r = "rejection failed: " + err.Error()
		}
		if d.loop != nil && !d.loop.State().IsTerminal() {
			d.loop.ReleaseHuman(r)
			d.publish(ctx)
			term := d.terminateAbort(ctx, r, autonomy.FailurePermanent)
			d.runCtx, d.runCancel = nil, nil
			return term, nil
		}
		return d.term(), nil
	}
	d.obs = obs
	d.loop.ReleaseHuman("patch rejected")
	d.publish(ctx)
	d.runID++
	term, err := d.observeAndRun(ctx, d.runID)
	if term != nil && term.State.IsTerminal() {
		d.runCtx = nil
		d.runCancel = nil
	}
	return term, err
}

// ResumeClarify continues a parked clarification with an explicit human-chosen
// target. No mutation ever happened before the boundary, so a bounded
// re-resolution and re-execution is safe.
func (d *Driver) ResumeClarify(ctx context.Context, target string) (*autonomy.LoopTermination, error) {
	if d.loop == nil || d.loop.State() != autonomy.RuntimeAwaitingHuman {
		return d.term(), errors.New("autonomy: clarify requires a parked clarification boundary")
	}
	d.req = autonomy.LoopRequest{Prompt: d.prompt, Targets: []string{target}}
	d.obs = d.contextObservation()
	d.loop.ReleaseHuman("target specified: " + target)
	d.publish(ctx)
	d.runID++
	term, err := d.observeAndRun(ctx, d.runID)
	if term != nil && term.State.IsTerminal() {
		d.runCtx = nil
		d.runCancel = nil
	}
	return term, err
}

// State returns the current loop position.
func (d *Driver) State() autonomy.RuntimeState {
	if d.loop == nil {
		return autonomy.RuntimeIdle
	}
	return d.loop.State()
}

// Boundary returns the active human boundary, or nil while not parked.
func (d *Driver) Boundary() *autonomy.HumanBoundary {
	if d.loop == nil {
		return nil
	}
	return d.loop.Boundary()
}

// Termination returns the terminal outcome, or nil while running/parked.
func (d *Driver) Termination() *autonomy.LoopTermination { return d.term() }

// History returns the observed transitions, oldest first.
func (d *Driver) History() []autonomy.RuntimeTransition {
	if d.loop == nil {
		return nil
	}
	return d.loop.History()
}

// LastObservation returns the most recent observation the driver consumed.
func (d *Driver) LastObservation() autonomy.Observation { return d.obs }

// ── drive helpers ───────────────────────────────────────────────────────────

// observeAndRun pushes the current observation through Observe → decide →
// execute until the loop terminates or parks at AwaitingHuman.
// runID is the identity of the run that started this observation loop;
// late results from a different runID are discarded.
func (d *Driver) observeAndRun(ctx context.Context, runID uint64) (*autonomy.LoopTermination, error) {
	if got := d.loop.Observe(d.obs); got != autonomy.RuntimeDeciding {
		return d.term(), fmt.Errorf("autonomy: observe -> %s, want deciding", got)
	}
	d.publish(ctx)
	for !d.loop.State().IsTerminal() {
		// Late-result guard: if the run was aborted/superseded, exit immediately.
		if d.runID != runID {
			return d.term(), nil
		}
		if cerr := ctx.Err(); cerr != nil {
			// Cancellation is a clean permanent abort, not a propagated error.
			return d.terminateAbort(ctx, "context cancelled", autonomy.FailurePermanent), nil //nolint:nilerr // termination, not a failure
		}
		switch d.loop.State() {
		case autonomy.RuntimeDeciding, autonomy.RuntimeInterpreting:
			// The loop owns the attempt/cycle counters; the decider sees them
			// through the bounded observation so the recovery matrix can
			// decide "exhausted → ask human" from the authoritative facts.
			d.obs.AttemptNum = d.loop.Attempts()
			d.obs.RecoveryCycle = d.loop.RecoveryCycles()
			decision := d.decide(d.obs, d.loop.Bounds())
			if _, err := d.step(ctx, decision); err != nil {
				return nil, err
			}
		case autonomy.RuntimeExecuting:
			obs, err := d.adapter.Execute(ctx, d.req)
			// Late-result guard: if the run was aborted/superseded while we were
			// executing, discard the result and return the terminal state.
			if d.runID != runID {
				return d.term(), nil
			}
			if err != nil {
				return nil, fmt.Errorf("autonomy: execute: %w", err)
			}
			d.obs = obs
			d.loop.ConsumeExecution(obs)
			d.loop.ConsumeVerification(obs)
			d.publish(ctx)
		case autonomy.RuntimeRecovering:
			req, err := d.repair(d.obs, d.req)
			if err != nil {
				return nil, fmt.Errorf("autonomy: repair: %w", err)
			}
			d.req = req
			if _, err := d.step(ctx, autonomy.LoopDecision{
				Action: autonomy.LoopContinue,
				Reason: "re-scoped — re-execute",
			}); err != nil {
				return nil, err
			}
		case autonomy.RuntimeAwaitingHuman:
			d.enrichBoundary()
			return d.term(), nil
		default:
			return d.term(), nil
		}
	}
	return d.term(), nil
}

func (d *Driver) step(ctx context.Context, decision autonomy.LoopDecision) (autonomy.RuntimeState, error) {
	state, err := d.loop.Step(ctx, decision)
	if err != nil {
		return state, err
	}
	d.publish(ctx)
	return state, nil
}

func (d *Driver) contextObservation() autonomy.Observation {
	return autonomy.Observation{
		Intent:   autonomy.ParseIntent(d.prompt),
		Target:   firstTarget(d.req.Targets),
		Evidence: d.req.Evidence,
	}
}

func (d *Driver) approvalPatchID() (string, error) {
	if d.loop == nil || d.loop.State() != autonomy.RuntimeAwaitingHuman {
		return "", errors.New("autonomy: approval requires a parked approval gate")
	}
	b := d.loop.Boundary()
	if b == nil || b.PatchID == "" {
		return "", errors.New("autonomy: parked boundary is not an approval gate")
	}
	return b.PatchID, nil
}

// enrichBoundary completes a parked boundary's presentation facts: the loop
// derives Action/Resumable at park time; the driver supplies the authoritative
// target set the parked execution holds (approval) or would hold (clarify).
// The UI's executor authorization on approve covers exactly these targets.
func (d *Driver) enrichBoundary() {
	b := d.loop.Boundary()
	if b == nil {
		return
	}
	if b.Action == "" {
		switch {
		case b.PatchID != "":
			b.Action = autonomy.HumanBoundaryApproval
			b.Resumable = true
		case len(b.Options) > 0:
			b.Action = autonomy.HumanBoundaryClarify
			b.Resumable = true
		default:
			b.Action = autonomy.HumanBoundaryInform
			b.Resumable = false
		}
	}
	if len(b.Targets) == 0 {
		b.Targets = append([]string(nil), d.req.Targets...)
	}
}

// publish emits every not-yet-published loop transition as a canonical
// loop.transition event. The driver is the single owner of these events;
// consumers (UI projection, tests) only observe.
func (d *Driver) publish(_ context.Context) {
	if d.bus == nil || d.loop == nil {
		return
	}
	history := d.loop.History()
	for i := d.published; i < len(history); i++ {
		t := history[i]
		d.bus.Publish(events.NewLoopTransition(t.From.String(), t.To.String(), string(t.Action), t.Reason))
	}
	d.published = len(history)
}

func (d *Driver) term() *autonomy.LoopTermination {
	if d.loop == nil {
		return nil
	}
	return d.loop.Termination()
}

func (d *Driver) terminateAbort(ctx context.Context, reason string, class autonomy.FailureClass) *autonomy.LoopTermination {
	_, term := d.loop.Abort(reason, class)
	d.publish(ctx)
	return term
}

// ── default policies ────────────────────────────────────────────────────────

// decideDefault maps a bounded observation onto the closed decision vocabulary:
//
//	context observation (no outcome)     → continue (execute)
//	changed/created/nochange/completed   → complete (objective satisfied)
//	pending_approval                     → ask_human (approval gate, patch id)
//	clarification required               → ask_human
//	cancelled/rejected/artifact_rejected → abort (permanent, never auto-retry)
//	no_artifact/failed/patch/apply/verify → canonical recovery matrix (bounded)
func decideDefault(o autonomy.Observation, b autonomy.LoopBounds) autonomy.LoopDecision {
	if o.ClarificationRequired {
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "target clarification required before any execution"}
	}
	switch o.Outcome {
	case autonomy.OutcomeChanged, autonomy.OutcomeCreated, autonomy.OutcomeNoChange,
		autonomy.OutcomeCompleted, autonomy.OutcomeArtifactProduced:
		return autonomy.LoopDecision{Action: autonomy.LoopComplete,
			Reason: "objective satisfied: " + string(o.Outcome)}
	case autonomy.OutcomePendingApproval:
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "mutation awaiting approval", PatchID: o.PatchID}
	case autonomy.OutcomeCancelled, autonomy.OutcomeRejected, autonomy.OutcomeArtifactRejected:
		return autonomy.LoopDecision{Action: autonomy.LoopAbort,
			Reason: "terminal outcome: " + string(o.Outcome)}
	case autonomy.OutcomeNoArtifact, autonomy.OutcomeFailed, autonomy.OutcomePatchGenFailed,
		autonomy.OutcomePatchFailed, autonomy.OutcomeApplyFailed, autonomy.OutcomeVerifyFailed,
		autonomy.OutcomeSkipped:
		return autonomy.RecoverFailure(o, autonomy.ClassifyOutcome(o.Outcome), b)
	case "":
		return autonomy.LoopDecision{Action: autonomy.LoopContinue,
			Reason: "objective resolved — execute"}
	default:
		return autonomy.LoopDecision{Action: autonomy.LoopAbort,
			Reason: "unrecognized outcome: " + string(o.Outcome)}
	}
}

// approvalFailureOutcome reports whether an observation produced by an approval
// apply is a failure. A success (changed/created/nochange) completes the loop; a
// failure must converge to a terminal aborted outcome — the loop never
// auto-repairs an approved proposal into a second provider invocation, because
// the human approved THIS patch, not a regeneration of it.
func approvalFailureOutcome(o autonomy.Observation) bool {
	switch o.Outcome {
	case autonomy.OutcomeFailed, autonomy.OutcomePatchGenFailed, autonomy.OutcomePatchFailed,
		autonomy.OutcomeApplyFailed, autonomy.OutcomeVerifyFailed, autonomy.OutcomeSkipped:
		return true
	default:
		return false
	}
}

// defaultRepair re-scopes a failed request for a bounded re-execution by
// appending the failed outcome as evidence. Real re-planning policies inject a
// RepairFunc; this default only preserves determinism.
func defaultRepair(o autonomy.Observation, req autonomy.LoopRequest) (autonomy.LoopRequest, error) {
	req.Evidence = strings.TrimSpace(req.Evidence + "\n" +
		"recovery of failed execution (outcome " + string(o.Outcome) + ")")
	return req, nil
}
