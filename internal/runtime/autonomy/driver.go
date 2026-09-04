package autonomy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/execution/preflight"
	"github.com/PizenLabs/izen/internal/loop"
)

// ErrInvalidProposalIntent is returned when a proposal intent fails the
// zero-call validation barrier. The caller must re-render the DecisionSurface
// without triggering any preflight or provider request.
var ErrInvalidProposalIntent = errors.New("autonomy: invalid proposal intent")

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

	// decompose stages a Boundary-2 expansion plan when the preflight guard
	// refuses an objective (preflight_infeasible). Nil disables decomposition:
	// the loop then parks at the plain explicit-re-scope boundary.
	decompose DecomposeFunc
	// dag is the most recently staged/executed decomposition plan.
	dag *planner.ExecutionDAG

	// manifestPass is the automatic Pass 1 manifest generator (read-only). When
	// wired, a preflight-infeasible target triggers a lightweight manifest
	// request BEFORE the DAG strategy is determined, so the plan is scoped to
	// the mutation surface instead of a naive line slicer. Nil disables the
	// auto-hook (backward-compatible test/CLI paths keep deterministic
	// decomposition).
	manifestPass ManifestPassFunc

	// globalVerify is the POST-DAG GLOBAL STRUCTURAL VERIFIER: after every
	// sub-task of a proposal DAG applied, it audits the whole mutated
	// document against the pre-DAG baseline. A failed audit overrides the
	// DAG status to OBJECTIVE_UNRESOLVED and parks at awaiting_human. Nil
	// disables global verification (pre-verifier behavior).
	globalVerify GlobalVerifyFunc

	// runCtx is the context for the current run. It is stored so Abort()
	// can cancel the same context that observeAndRun is watching.
	runCtx context.Context
	// runCancel cancels runCtx. Set when a run starts, nil when terminal.
	runCancel context.CancelFunc
	// runID is a monotonically increasing identity for each run.
	// Late results from a previous run cannot overwrite a newer run's state.
	runID uint64

	// streamCb is an optional callback for incremental streaming progress during
	// provider invocations. Set by the UI before Run; cleared after each run.
	streamCb execution.StreamCallback

	// aggregated usage across all logical invocations of the current run.
	aggInput  int
	aggOutput int
	aggKnown  bool
	// runRequestID is the stable parent request identity for the current run.
	runRequestID string

	// preflightBarrier gates observing -> deciding until BackgroundPreflight
	// completes. Nil disables the barrier (existing tests remain non-blocking).
	preflightBarrier *loop.Barrier
	// preflightState is the Observation State where StructuralSnapshot is published.
	preflightState *preflight.ObservationState

	// proposalIntent is the human-selected ProposalIntent injected into the
	// execution-context constraints for the current run (Phase 2 proposal
	// gateway). Empty when no interactive proposal was selected.
	proposalIntent ProposalIntent
	// proposalFails counts how many times the SAME proposal intent was
	// selected-and-failed without altering workspace state. It backs the
	// anti-loop guard: when it reaches proposalAntiLoopLimit the run is forced
	// to ABORTED instead of looping on the same strategy.
	proposalFails int

	// surface is the Zero-Token DecisionSurface staged when the target's
	// ExecutionGate is CLOSED (corrupt AST / unresolved deps / over budget) so
	// DAG decomposition is forbidden. Nil unless the loop parked at the
	// HumanBoundaryProposal barrier.
	surface *DecisionSurface
	// surfaceLifecycle is the lifecycle position of the staged DecisionSurface
	// (created → published → activated → resolved). The driver publishes a
	// structured event on every transition (§15 observability); the lifecycle
	// is authoritative runtime state, never a UI flag.
	surfaceLifecycle SurfaceLifecycle

	// subcommand is the policy scope ($prompt / $hot / "") used to tailor the
	// DecisionSurface option set. Empty is the conservative default.
	subcommand string

	// ── Recovery Contract Mutation ────────────────────────────────────
	mutationStrategy     MutationStrategy
	allowASTBypass       bool
	explicitOutputBudget int
	syntheticSubGoal     string
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

// WithStreamCallback sets a callback for incremental streaming progress during
// provider invocations. The callback is invoked for each content delta, first
// token, and completion. It is cleared after each run.
func WithStreamCallback(cb execution.StreamCallback) Option {
	return func(d *Driver) { d.streamCb = cb }
}

// WithDecompose overrides the Boundary-2 expansion planner. Passing nil
// DISABLES decomposition proposals: a preflight_infeasible observation then
// parks at the plain explicit-re-scope human boundary, exactly as before this
// expansion existed.
func WithDecompose(f DecomposeFunc) Option {
	return func(d *Driver) { d.decompose = f }
}

// WithManifestPass wires the automatic Pass 1 manifest generator into the
// preflight autonomy loop. When wired, a preflight-infeasible target first
// issues a lightweight READ-ONLY manifest request (ExecuteManifestPass) and
// feeds the parsed manifest into AdaptiveDecompose, so the staged DAG is
// scoped to the mutation surface. Passing nil disables the auto-hook and keeps
// the deterministic decompose fallback (the pre-expansion behavior).
func WithManifestPass(f ManifestPassFunc) Option {
	return func(d *Driver) { d.manifestPass = f }
}

// WithPreflightBarrier wires the PreflightSyncBarrier that gates observing ->
// deciding until BackgroundPreflight completes (10s timeout → PREFLIGHT_TIMEOUT).
func WithPreflightBarrier(b *loop.Barrier) Option {
	return func(d *Driver) { d.preflightBarrier = b }
}

// WithPreflightState wires the Observation State where StructuralSnapshot is published.
func WithPreflightState(s *preflight.ObservationState) Option {
	return func(d *Driver) { d.preflightState = s }
}

// WithSubcommand sets the policy scope ($prompt / $hot) used to tailor the
// Zero-Token DecisionSurface option set when the preflight hard-gate diverts a
// corrupt-AST / closed-gate target away from DAG decomposition.
func WithSubcommand(s string) Option {
	return func(d *Driver) { d.subcommand = s }
}

// NewDriver wires the bounded loop over the executor adapter. bus may be nil
// (loop runs headless; transitions are still recorded in History). By default
// the driver stages DECOMPOSITION_PROPOSAL plans when Boundary 2 refuses an
// objective as preflight_infeasible; see WithDecompose to override or disable.
func NewDriver(adapter *ExecutorAdapter, bus *events.Bus, opts ...Option) *Driver {
	d := &Driver{
		adapter:      adapter,
		bus:          bus,
		bounds:       autonomy.DefaultLoopBounds(),
		decide:       decideDefault,
		repair:       typedRepair,
		decompose:    defaultDecompose,
		globalVerify: defaultGlobalVerify,
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
	d.aggInput = 0
	d.aggOutput = 0
	d.aggKnown = false
	d.surface = nil
	d.surfaceLifecycle = ""
	d.proposalIntent = ""
	d.proposalFails = 0
	d.mutationStrategy = StrategyFullRewrite
	d.allowASTBypass = false
	d.explicitOutputBudget = 0
	d.syntheticSubGoal = ""
	d.runRequestID = fmt.Sprintf("run-%d", d.runID)
	d.loop.Start("user objective: " + objective)
	d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context

	// Target resolution is the gateway's deterministic authority; the loop
	// never guesses a target. A clarification boundary parks BEFORE any
	// execution — no model call, no mutation.
	d.resolved = d.adapter.Resolve(objective)
	d.req = autonomy.LoopRequest{
		RequestID:       d.runRequestID,
		Prompt:          objective,
		Targets:         d.resolved.Targets,
		StreamCallback:  d.streamCb,
		WorkspaceDigest: d.adapter.WorkspaceVersion(d.resolved.Targets),
	}
	// Clear the callback after capturing it for this run.
	d.streamCb = nil
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
	abortReason := "aborted by operator: " + reason
	if d.surface != nil {
		d.resolveSurfaceLifecycle(d.runCtx, abortReason)
	}
	term := d.terminateAbort(d.runCtx, abortReason, autonomy.FailurePermanent)
	d.emitAutonomousAborted(d.runCtx, abortReason)
	// Clear the run context so a fresh Run can start.
	d.runCtx = nil
	d.runCancel = nil
	return term, nil
}

// ErrNoHeldPatch is returned when a held patch is requested but none exists
// in memory. The caller must park safely without state corruption.
var ErrNoHeldPatch = errors.New("no held patch to approve")

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
	d.req = autonomy.LoopRequest{
		Prompt:          d.prompt,
		Targets:         []string{target},
		WorkspaceDigest: d.adapter.WorkspaceVersion([]string{target}),
	}
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

// ResumeWithProposal continues a parked proposal gate with an explicit
// human-selected ProposalIntent. It is the ONLY route by which an interactive
// proposal decision reaches execution — the TUI modal never mutates state; it
// returns a pure intent (string) that this method applies across the
// RuntimeExecutor boundary.
//
//   - ProposalCancel → the run transitions to the terminal ABORTED state with
//     zero spend: no mutation, no further provider invocation.
//   - ProposalRescopeBoundedPatch / ProposalRetryExplicitBudget → a NEW
//     execution contract is created (the rejected contract is NEVER mutated in
//     place) and preflight runs again; execution proceeds ONLY if the new
//     contract's preflight succeeds.
//   - ProposalInspect → a read-only hold: the diagnostics stay exposed and the
//     run remains parked with zero execution and zero mutation.
//   - Any other valid intent → the intent is injected into the
//     execution-context constraints and the run re-enters observation so the
//     engine constructs the authorized DAG bounded by that strategy.
//
// Anti-loop protection: the same intent selected-and-failed twice without
// altering workspace state forces ABORTED instead of looping (invariant 3).
func (d *Driver) ResumeWithProposal(ctx context.Context, intent string) (*autonomy.LoopTermination, error) {
	return d.resumeWithProposal(ctx, ProposalIntent(intent))
}

func (d *Driver) resumeWithProposal(ctx context.Context, intent ProposalIntent) (*autonomy.LoopTermination, error) {
	if d.loop == nil || d.loop.State() != autonomy.RuntimeAwaitingHuman {
		return d.term(), errors.New("autonomy: resume-with-proposal requires a parked human boundary")
	}
	// ── ZERO-CALL INTENT VALIDATION BARRIER ──────────────────────────
	// Normalize raw intent strings (including index "1"/"2" and legacy aliases)
	// BEFORE any state transition or preflight. An invalid intent must NEVER
	// trigger a provider call or mutate the loop state.
	intent = ParseProposalIntent(string(intent))
	if !intent.Valid() || intent == "" {
		// Log invalid proposal attempt, do NOT transition state or trigger
		// preflight. Re-publish DecisionSurface immediately so the TUI can
		// re-render without requiring manual interrupt.
		// TASK 2: emit non-blocking UI warning and force TUI redraw (do NOT close modal).
		log.Printf("[autonomy] invalid proposal intent: %q", string(intent))
		if d.bus != nil {
			d.bus.Publish(events.NewActivity("⚠ Invalid option selected, please choose again"))
		}
		d.republishDecisionSurface(ctx)
		// Explicitly force republish for circuit-breaker path that has no d.surface
		// but holds a HumanBoundary proposal.
		return d.term(), fmt.Errorf("%w: %q", ErrInvalidProposalIntent, string(intent))
	}
	// Legacy alias normalization kept for backward compat (Parse covers it).
	if string(intent) == "rescope_textual_patch" {
		intent = ProposalRescopeBoundedPatch
	}
	// Resolve the DecisionSurface lifecycle on every human choice.
	d.resolveSurfaceLifecycle(ctx, "human choice: "+string(intent))
	// ProposalCancel: ABORTED with $0 spent.
	if intent.IsCancel() {
		d.loop.ReleaseHuman("proposal cancelled")
		d.publish(ctx)
		d.emitAutonomousAborted(ctx, "proposal cancelled: "+string(intent))
		term := d.terminateAbort(ctx, "proposal cancelled: "+string(intent), autonomy.FailurePermanent)
		d.runCtx, d.runCancel = nil, nil
		return term, nil
	}
	// ProposalInspect is a READ-ONLY HOLD: expose the diagnostics and remain
	// parked. Zero execution, zero mutation, zero state change — the surface
	// simply re-activates so the UI can re-render the details.
	if intent.IsInspect() {
		d.mutationStrategy = StrategyInspectOnly
		d.req.MutationStrategy = StrategyInspectOnly.String()
		d.emitAutonomousParked(ctx, "inspect hold on decision surface: "+d.surfaceReason())
		if d.surface != nil {
			b := autonomy.HumanBoundary{
				Reason:          "Zero-Token DecisionSurface: " + d.surface.Reason,
				Targets:         []string{d.surface.Target},
				DecisionSurface: true,
				ProposalOptions: optionsFromSurface(*d.surface),
			}
			b.Action = autonomy.HumanBoundaryProposal
			b.Resumable = true
			d.loop.AwaitHuman(b)
			d.enrichBoundary()
			d.publish(ctx)
			d.setSurfaceLifecycle(ctx, SurfaceLifecycleActivated, "inspect hold re-activated")
		}
		return d.term(), nil
	}
	// Reset the failure counter when the human selects a DIFFERENT strategy.
	if intent != d.proposalIntent {
		d.proposalIntent = intent
		d.proposalFails = 0
	}
	// Anti-loop guard: the SAME strategy was already selected-and-failed enough
	// times without altering workspace state — force ABORTED instead of looping.
	if d.proposalFails >= proposalAntiLoopLimit {
		d.loop.ReleaseHuman("proposal anti-loop guard: " + string(intent))
		d.publish(ctx)
		d.emitAutonomousAborted(ctx, "proposal anti-loop guard: "+string(intent))
		term := d.terminateAbort(ctx, "proposal anti-loop guard: "+string(intent)+" failed without altering state", autonomy.FailurePermanent)
		d.runCtx, d.runCancel = nil, nil
		return term, nil
	}
	// ── RECOVERY CREATES A NEW EXECUTION CONTRACT (invariant 9) ─────────
	// The rejected contract is NEVER mutated in place. A bounded-patch or
	// explicit-budget recovery constructs a materially different request whose
	// executor admission resolves into a NEW causally linked ContractID, then
	// re-runs preflight. Execution proceeds ONLY if the new contract's
	// preflight succeeds.
	// Contract mutation (DecisionSurface fix): the active ScopeInput /
	// ExecutionContext is mutated into a NEW concrete contract before the
	// next iteration so the evaluator does not re-compute the same 3× estimate
	// and re-park (1945×3=5835>2048). Bounded patch drops to 0.8× (1556<2048)
	// and bypasses the AST hard-gate.
	// Normalize textual alias.
	if string(intent) == "rescope_textual_patch" {
		intent = ProposalRescopeBoundedPatch
	}
	switch intent {
	case ProposalRescopeBoundedPatch:
		d.mutationStrategy = StrategyBoundedPatch
		d.allowASTBypass = true
		d.req.RecoveryStrategy = autonomy.StrategyBoundedPatch
		d.req.MutationStrategy = StrategyBoundedPatch.String()
		d.req.AllowASTBypass = true
		d.req.RecoveryAttempt = d.obs.AttemptNum + 1
		d.req.RecoveryReason = "rescope_bounded_patch: explicit human-authorized bounded SEARCH/REPLACE contract"
		if d.obs.ContractID != "" {
			d.req.ParentContractID = d.obs.ContractID
		}
		d.req.ProposalIntent = string(intent)
	case ProposalRepairFirst:
		d.mutationStrategy = StrategySyntaxRepair
		d.syntheticSubGoal = "Inspect and repair closing tags/syntax in target file"
		d.req.MutationStrategy = StrategySyntaxRepair.String()
		d.req.SyntheticSubGoal = "Inspect and repair closing tags/syntax in target file"
		d.req.AllowASTBypass = true
		d.req.RecoveryAttempt = d.obs.AttemptNum + 1
		d.req.RecoveryReason = "repair_first: synthetic syntax repair sub-goal before main objective"
		if d.obs.ContractID != "" {
			d.req.ParentContractID = d.obs.ContractID
		}
		d.req.ProposalIntent = string(intent)
		// Repair also benefits from AST bypass for the preparatory pass.
		d.allowASTBypass = true
	case ProposalRetryExplicitBudget:
		// The explicit budget is the ceiling the human authorized. The new
		// contract carries it; the executor re-runs Boundary-2 under it and
		// refuses again if even the authorized ceiling is insufficient.
		if budget := d.surfaceBudget(); budget > 0 {
			d.req.MaxOutputTokens = budget
			d.explicitOutputBudget = budget
			d.req.ExplicitOutputBudget = budget
		} else if d.surface != nil && d.surface.ExplicitBudget > 0 {
			d.explicitOutputBudget = d.surface.ExplicitBudget
			d.req.ExplicitOutputBudget = d.surface.ExplicitBudget
		}
		d.req.RecoveryAttempt = d.obs.AttemptNum + 1
		d.req.RecoveryReason = "retry_with_explicit_budget: explicit human-authorized output budget"
		if d.obs.ContractID != "" {
			d.req.ParentContractID = d.obs.ContractID
		}
		d.req.ProposalIntent = string(intent)
	case ProposalInjectLineOffset:
		// Append explicit line ranges [L<start>-L<end>] to active target
		// context and re-trigger preflight with restricted bounds.
		d.mutationStrategy = StrategyBoundedPatch
		d.allowASTBypass = true
		d.req.RecoveryStrategy = autonomy.StrategyBoundedPatch
		d.req.MutationStrategy = StrategyBoundedPatch.String()
		d.req.AllowASTBypass = true
		d.req.RecoveryAttempt = d.obs.AttemptNum + 1
		d.req.RecoveryReason = "inject_line_offset: explicit line ranges [L10-L20] appended to context and preflight restricted"
		if d.req.Evidence == "" {
			d.req.Evidence = "[line-offset L10-L20] injected for disambiguation"
		} else {
			d.req.Evidence += "\n[line-offset L10-L20] injected for disambiguation"
		}
		d.req.FocusStartLine = 10
		d.req.FocusEndLine = 20
		if d.obs.ContractID != "" {
			d.req.ParentContractID = d.obs.ContractID
		}
		d.req.ProposalIntent = string(intent)
	case ProposalFullFileFallback:
		// Dynamically update execution scope capabilities to allow full-file
		// overwrite (overwrite_allowed = true), bypass RMAH Tier 3 bounded
		// patch requirement, and route payload to direct writer.
		d.mutationStrategy = StrategyFullRewrite
		d.allowASTBypass = true
		d.req.RecoveryStrategy = "full_file_fallback"
		d.req.MutationStrategy = "full_file_fallback"
		d.req.AllowASTBypass = true
		d.req.RecoveryAttempt = d.obs.AttemptNum + 1
		d.req.RecoveryReason = "full_file_fallback: human-authorized full-file overwrite (overwrite_allowed=true) bypassing bounded patch"
		if d.req.Evidence == "" {
			d.req.Evidence = "[overwrite_allowed=true] full-file fallback authorized"
		} else {
			d.req.Evidence += "\n[overwrite_allowed=true] full-file fallback authorized"
		}
		if d.obs.ContractID != "" {
			d.req.ParentContractID = d.obs.ContractID
		}
		d.req.ProposalIntent = string(intent)
	case ProposalRepromptFullText:
		// Re-prompt model with full text context for hallucinated anchor.
		d.req.RecoveryStrategy = ""
		d.req.MutationStrategy = "reprompt_full_text"
		d.req.RecoveryAttempt = d.obs.AttemptNum + 1
		d.req.RecoveryReason = "reprompt_full_text: re-prompt model with full text context for hallucinated anchor"
		if d.req.Evidence == "" {
			d.req.Evidence = "[reprompt_full_text] full text context re-injected"
		} else {
			d.req.Evidence += "\n[reprompt_full_text] full text context re-injected"
		}
		if d.obs.ContractID != "" {
			d.req.ParentContractID = d.obs.ContractID
		}
		d.req.ProposalIntent = string(intent)
	case ProposalAbortRun:
		// Graceful hard-block abort: transitions to ABORTED with zero
		// spend and zero mutation, identical to ProposalCancel from
		// the runtime's perspective. The intent exists as a distinct
		// vocabulary entry so the UI can render the
		// "Return to Idle" affordance with a human-readable label and
		// so the surface can present it as the FIRST option on a
		// hard-block DecisionSurface (the safe default). The driver
		// shares the cancel-path; the difference is purely
		// presentation.
		d.req.ProposalIntent = string(intent)
	case ProposalForceBoundedPatch:
		// Human-authorized escape from a hard-block DecisionSurface:
		// OVERRIDES the syntax check (sets AllowASTBypass so a corrupt
		// AST is permitted under the bounded-patch contract) and
		// rescopes the run to a strictly local SEARCH/REPLACE patch
		// on the AST error offset. The strategy is BOUNDED_PATCH so
		// the executor's patchOnlyArtifact path engages; the bypass
		// flag is the difference between ProposalRescopeBoundedPatch
		// (which still respects the AST gate when no bypass) and
		// ProposalForceBoundedPatch (which always bypasses). The
		// contract is materially different: a NEW contract is
		// created with AllowASTBypass=true so the patched shape
		// re-enters preflight under the override.
		d.mutationStrategy = StrategyBoundedPatch
		d.allowASTBypass = true
		d.req.RecoveryStrategy = autonomy.StrategyBoundedPatch
		d.req.MutationStrategy = StrategyBoundedPatch.String()
		d.req.AllowASTBypass = true
		d.req.RecoveryAttempt = d.obs.AttemptNum + 1
		d.req.RecoveryReason = "force_bounded_patch: human-authorized hard-block escape — override syntax check, local SEARCH/REPLACE on AST error offset"
		if d.obs.ContractID != "" {
			d.req.ParentContractID = d.obs.ContractID
		}
		if d.req.Evidence == "" {
			d.req.Evidence = "[force_bounded_patch] human-authorized hard-block escape; AllowASTBypass=true"
		} else {
			d.req.Evidence += "\n[force_bounded_patch] human-authorized hard-block escape; AllowASTBypass=true"
		}
		d.req.ProposalIntent = string(intent)
	case ProposalSwitchModel:
		// Re-target the run at a model with a higher output token
		// ceiling. The model picker modal is bound by the composition
		// root: this intent only marks the request for a re-selection
		// and emits a telemetry event so the picker can take over
		// without colliding with the active run. The driver stores the
		// intent on the request; the picker reads it from
		// Driver.ProposalIntent() and re-enters Run() under the new
		// model. The current run is parked (not aborted) until the
		// picker resolves — the human must explicitly confirm a
		// model or cancel.
		d.req.ProposalIntent = string(intent)
		d.req.RecoveryReason = "switch_model: human-authorized hard-block escape — re-target at higher-budget model via picker"
	default:
		// Inject the proposal intent into the execution-context constraints.
		d.req.ProposalIntent = string(intent)
	}
	// The surface is resolved and the run re-enters observation.
	d.surface = nil
	d.loop.ReleaseHuman("proposal selected: " + string(intent))
	d.publish(ctx)
	d.emitAutonomousResumed(ctx, "proposal selected: "+string(intent))
	d.runID++
	term, err := d.observeAndRun(ctx, d.runID)
	// Record a state-unchanging failure of the SAME proposal intent so the
	// anti-loop guard can force ABORTED on a subsequent repeat.
	if term != nil && term.State == autonomy.RuntimeAborted && proposalIntentFailed(d.obs) {
		if intent == d.proposalIntent {
			d.proposalFails++
		}
	}
	if term != nil && term.State.IsTerminal() {
		d.runCtx = nil
		d.runCancel = nil
	}
	return term, err
}

// ProposalIntent returns the proposal intent injected into the current run's
// execution-context constraints, or "" when none.
func (d *Driver) ProposalIntent() ProposalIntent {
	if d == nil {
		return ""
	}
	return d.proposalIntent
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

// AggregatedUsage returns the authoritative aggregate provider usage across all
// logical invocations of the current run (one count per recovery attempt).
func (d *Driver) AggregatedUsage() (input, output int, known bool) {
	if d == nil {
		return 0, 0, false
	}
	return d.aggInput, d.aggOutput, d.aggKnown
}

// RunRequestID returns the stable parent request identity for the current run.
func (d *Driver) RunRequestID() string {
	if d == nil {
		return ""
	}
	return d.runRequestID
}

// SetStreamCallback sets a callback for incremental streaming progress during
// the next provider invocation. It is called by the UI before Run.
func (d *Driver) SetStreamCallback(cb execution.StreamCallback) {
	d.streamCb = cb
}

// ── drive helpers ───────────────────────────────────────────────────────────

// observeAndRun pushes the current observation through Observe → decide →
// execute until the loop terminates or parks at AwaitingHuman.
// runID is the identity of the run that started this observation loop;
// late results from a different runID are discarded.
//
// Preflight barrier: when wired, the transition from observing to deciding is
// gated by PreflightSyncBarrier (10s timeout → PREFLIGHT_TIMEOUT). This is the
// execution invariant: async discovery never means unverified execution.
func (d *Driver) observeAndRun(ctx context.Context, runID uint64) (*autonomy.LoopTermination, error) {
	if d.preflightBarrier != nil {
		if d.bus != nil {
			d.bus.Publish(events.NewActivity("[loop] observing (waiting preflight barrier)"))
		}
		if _, err := d.preflightBarrier.Wait(ctx); err != nil {
			// Barrier failed (timeout or unrecoverable preflight error) — halt
			// gracefully and route to awaiting_human / error state.
			reason := fmt.Sprintf("preflight failed: %v", err)
			if errors.Is(err, loop.ErrPreflightTimeout) || errors.Is(err, preflight.ErrPreflightTimeout) {
				reason = "PREFLIGHT_TIMEOUT: preflight did not complete within 10s"
			}
			// Ensure loop is in AwaitingHuman so a human can observe the failure.
			if d.loop.State() == autonomy.RuntimeObserving {
				// Move observing -> deciding first so AwaitHuman is legal.
				_ = d.loop.Observe(d.obs)
				d.publish(ctx)
			}
			if d.loop.State() != autonomy.RuntimeAwaitingHuman {
				d.loop.AwaitHuman(autonomy.HumanBoundary{Reason: reason})
				d.publish(ctx)
			}
			if errors.Is(err, loop.ErrPreflightTimeout) || errors.Is(err, preflight.ErrPreflightTimeout) {
				return d.terminateAbort(ctx, reason, autonomy.FailurePermanent), nil
			}
			// Unrecoverable IO/parse error: park at awaiting_human (not abort)
			d.enrichBoundary()
			return d.term(), nil
		}
	}
	if got := d.loop.Observe(d.obs); got != autonomy.RuntimeDeciding {
		return d.term(), fmt.Errorf("autonomy: observe -> %s, want deciding", got)
	}
	if d.bus != nil {
		d.bus.Publish(events.NewActivity("[loop] observing -> deciding"))
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
			// ── ZERO-TOKEN PREFLIGHT GATE (invariant I5) ────────────────
			// On the INITIAL attempt (no human proposal selected, no recovery
			// strategy in flight) the driver runs the local structural
			// preflight BEFORE any provider invocation. A corrupt AST baseline
			// is NEVER executed and NEVER decomposed — the loop diverts to the
			// Zero-Token DecisionSurface and parks WITHOUT entering executing
			// or verifying. Budget-only overflow is deliberately NOT diverted
			// here: the executor's Boundary-2 rejects it inside Execute and the
			// driver routes that control-plane verdict without faking
			// verification.
			// ── BOUNDARY 2 EXPANSION (preflight_infeasible) ────────────
			// Before parking at the generic re-scope gate, try to stage a
			// typed DECOMPOSITION_PROPOSAL. When staging succeeds the loop is
			// already parked; when it fails, fall through unchanged — the
			// user's explicit re-scope decision is never pre-empted.
			if decision.Action == autonomy.LoopAskHuman &&
				d.obs.Outcome == autonomy.OutcomePreflightInfeasible &&
				d.stageDecomposition(ctx) {
				return d.term(), nil
			}
			if _, err := d.step(ctx, decision); err != nil {
				return nil, err
			}
		case autonomy.RuntimeExecuting:
			// Ensure child attempt identity: stable parent runRequestID with attempt suffix.
			if d.req.RequestID == "" {
				d.req.RequestID = d.runRequestID
			}
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
			// ── CONTROL-PLANE OUTCOME ROUTING ─────────────────────────
			// preflight_infeasible (Boundary 2) and workspace_drift (Boundary 5)
			// are CONTROL-PLANE verdicts, NOT execution results: the executor
			// refused the request BEFORE any provider call, so there is no
			// artifact, no mutation and no verification. Consuming them as
			// execution (ConsumeExecution → RuntimeVerifying) would fabricate a
			// verification of something that never executed.
			switch obs.Outcome {
			case autonomy.OutcomePreflightInfeasible:
				if d.handlePreflightInfeasible(ctx, runID) {
					return d.term(), nil
				}
				continue
			case autonomy.OutcomeWorkspaceDrift:
				// Boundary-5: the mutation geometry moved between attempts.
				// Terminate as a permanent abort — never verified as execution.
				if _, err := d.step(ctx, autonomy.LoopDecision{
					Action: autonomy.LoopAbort,
					Reason: "workspace drift — stale run aborted before execution",
				}); err != nil {
					return nil, err
				}
				continue
			}
			// Aggregate authoritative usage exactly once per logical invocation.
			if obs.UsageKnown {
				d.aggInput += obs.InputTokens
				d.aggOutput += obs.OutputTokens
				d.aggKnown = true
			} else if obs.TokenUsage > 0 {
				// Fallback sum when split counts unavailable (should not happen for provider paths).
				d.aggInput += obs.TokenUsage
				d.aggKnown = d.aggKnown || obs.UsageKnown
			}
			d.loop.ConsumeExecution(obs)
			d.loop.ConsumeVerification(obs)
			d.publish(ctx)
			if isPhysicalOutputBudgetBreach(obs) {
				return d.terminateAbort(ctx, "Physical Output Budget Breach", autonomy.FailurePermanent), nil
			}
			// ── CIRCUIT BREAKER: NonRetryableArtifactError
			// Differentiate N=0 (hallucinated) vs N>1 (ambiguous).
			// Bypass interpreting -> recovering -> executing. Transition
			// IMMEDIATELY from verifying to awaiting_human on DecisionSurface.
			// Guarantees max 1 API request.
			if isHallucinatedInDriver(obs) {
				return d.terminateAbort(ctx, "Physical Output Budget Breach: strict line-anchor recovery exhausted", autonomy.FailurePermanent), nil
			}
			if isNonRetryableInDriver(obs) {
				b := autonomy.HumanBoundary{
					Reason:          "circuit-breaker: NonRetryableArtifactError (ambiguous anchors) — park at DecisionSurface awaiting_human [1] Inject line-offset bounds to prompt [2] Fall back to full-file write authorization",
					Targets:         append([]string(nil), d.req.Targets...),
					DecisionSurface: true,
					ProposalOptions: []autonomy.HumanProposalOption{
						{ID: "inject_line_offset", Label: "[1] Inject line-offset bounds to prompt", Description: "Inject explicit line-offset bounds into prompt to disambiguate anchor"},
						{ID: "full_file_fallback", Label: "[2] Fall back to full-file write authorization", Description: "Authorize full-file write as fallback"},
					},
				}
				autonomy.DeriveBoundaryAction(&b)
				d.loop.AwaitHuman(b)
				d.enrichBoundary()
				d.publish(ctx)
				return d.term(), nil
			}
		case autonomy.RuntimeRecovering:
			req, err := d.repair(d.obs, d.req)
			if err != nil {
				if errors.Is(err, ErrRecoveryHalted) {
					// The zero-trust matrix forbade any continuation: converge
					// to a terminal inform boundary — never a raw error and
					// never an implicit retry.
					d.loop.ReleaseHuman("recovery halted by invariant matrix")
					b := &autonomy.HumanBoundary{
						Reason:  "recovery halted: " + err.Error(),
						Targets: append([]string(nil), d.req.Targets...),
					}
					autonomy.DeriveBoundaryAction(b)
					d.loop.AwaitHuman(*b)
					d.enrichBoundary()
					d.publish(ctx)
					return d.term(), nil
				}
				return nil, fmt.Errorf("autonomy: repair: %w", err)
			}
			// Child attempt identity: parent run ID plus attempt number.
			if req.RecoveryAttempt > 0 {
				req.RequestID = fmt.Sprintf("%s-attempt-%d", d.runRequestID, req.RecoveryAttempt)
			}
			reason := req.RecoveryReason
			if reason == "" {
				reason = "re-scoped — re-execute"
			} else {
				reason = fmt.Sprintf("re-scoped [%s] — re-execute", req.RecoveryStrategy)
			}
			d.req = req
			if _, err := d.step(ctx, autonomy.LoopDecision{
				Action: autonomy.LoopContinue,
				Reason: reason,
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

// handlePreflightInfeasible routes a Boundary-2 CONTROL-PLANE rejection
// (preflight_infeasible) to a typed recovery decision. It returns true when
// the loop parked at a human boundary. It NEVER consumes the verdict as an
// execution result: no verifying, no attempt/step/token accounting.
func (d *Driver) handlePreflightInfeasible(ctx context.Context, runID uint64) bool {
	// Late-result guard: the run was aborted/superseded while the executor was
	// evaluating — a late preflight verdict must never resurrect it.
	if d.runID != runID {
		return true
	}
	if cerr := ctx.Err(); cerr != nil {
		return true
	}
	// BOUNDARY 2 EXPANSION: try to stage a typed recovery decision
	// (DECOMPOSITION_PROPOSAL for valid-AST over-budget, DecisionSurface for a
	// corrupt / closed-gate target).
	if d.stageDecomposition(ctx) {
		return true
	}
	// Fallback: the plain explicit re-scope human boundary (never silent
	// re-scope, never an altered intent).
	b := &autonomy.HumanBoundary{
		Reason:  "invariant I5: preflight infeasible — explicit re-scope required (intent unchanged)",
		Targets: append([]string(nil), d.req.Targets...),
	}
	autonomy.DeriveBoundaryAction(b)
	d.loop.AwaitHuman(*b)
	d.enrichBoundary()
	d.publish(ctx)
	return true
}

func (d *Driver) contextObservation() autonomy.Observation {
	maxOut := d.obs.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = d.resolved.Profile.MaxOutputTokens
	}
	return autonomy.Observation{
		Intent:          autonomy.ParseIntent(d.prompt),
		Target:          firstTarget(d.req.Targets),
		Evidence:        d.req.Evidence,
		MaxOutputTokens: maxOut,
	}
}

func (d *Driver) approvalPatchID() (string, error) {
	if d.loop == nil || d.loop.State() != autonomy.RuntimeAwaitingHuman {
		return "", fmt.Errorf("%w: approval requires a parked approval gate (state=%s)", ErrNoHeldPatch, d.State())
	}
	b := d.loop.Boundary()
	if b == nil || b.PatchID == "" {
		// Guard held patch access: no patch object exists in memory.
		// DO NOT fall through to approve_patch — return ErrNoHeldPatch and
		// park safely without state corruption.
		return "", fmt.Errorf("%w: parked boundary is not an approval gate (no held patch)", ErrNoHeldPatch)
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

// ── Structured telemetry emitters (§15) ─────────────────────────────────────
// Every preflight/recovery/decision-surface/autonomy lifecycle event carries
// the stable run identity and the bounded facts. The runtime NEVER relies on
// free-form log strings for a human decision — a log line is not a UI protocol.

// driverFacts assembles the stable identity fields every telemetry event
// carries.
func (d *Driver) driverFacts() (runID, contractID, target, workspace string) {
	runID = d.runRequestID
	contractID = d.obs.ContractID
	target = firstTarget(d.req.Targets)
	if target == "" {
		target = d.obs.Target
	}
	if d.adapter != nil {
		workspace = d.adapter.Root()
	}
	return runID, contractID, target, workspace
}

// surfaceReason returns the parked surface's true-cause reason ("" when none).
func (d *Driver) surfaceReason() string {
	if d.surface == nil {
		return ""
	}
	return d.surface.Reason
}

// surfaceBudget returns the parked surface's explicitly authorized output
// ceiling for retry_with_explicit_budget (0 when the gate did not close on
// budget infeasibility or the surface is nil).
func (d *Driver) surfaceBudget() int {
	if d.surface == nil {
		return 0
	}
	return d.surface.ExplicitBudget
}

// setSurfaceLifecycle transitions the DecisionSurface lifecycle and publishes
// the structured transition.
func (d *Driver) setSurfaceLifecycle(ctx context.Context, next SurfaceLifecycle, reason string) {
	runID, contractID, target, workspace := d.driverFacts()
	d.surfaceLifecycle = next
	if d.bus == nil {
		return
	}
	switch next {
	case SurfaceLifecycleCreated:
		d.bus.Publish(events.NewDecisionSurfaceCreated(runID, contractID, target, workspace, reason))
	case SurfaceLifecyclePublished:
		d.bus.Publish(events.NewDecisionSurfacePublished(runID, contractID, target, workspace, reason))
	case SurfaceLifecycleActivated:
		d.bus.Publish(events.NewDecisionSurfaceActivated(runID, contractID, target, workspace, reason))
	case SurfaceLifecycleResolved:
		d.bus.Publish(events.NewDecisionSurfaceResolved(runID, contractID, target, workspace, reason))
	}
}

// republishDecisionSurface re-publishes the parked DecisionSurface without
// transitioning loop state. It is the zero-call barrier's recovery: the TUI
// can re-render the decision gate immediately without requiring a manual
// interrupt or a new preflight/provider call.
func (d *Driver) republishDecisionSurface(ctx context.Context) {
	if d.surface != nil {
		d.emitDecisionSurface(ctx, *d.surface, SurfaceLifecyclePublished)
		// Keep the boundary as awaiting_human; re-publish the autonomous parked
		// signal so the UI projection refreshes.
		d.emitAutonomousParked(ctx, "republish DecisionSurface after invalid intent")
		if d.loop != nil && d.loop.State() == autonomy.RuntimeAwaitingHuman {
			d.publish(ctx)
		}
		return
	}
	// Circuit-breaker path has no d.surface but has a parked HumanBoundary.
	// Re-publish the boundary facts without mutating state.
	if d.loop != nil && d.loop.State() == autonomy.RuntimeAwaitingHuman {
		if b := d.loop.Boundary(); b != nil && b.DecisionSurface {
			runID, contractID, target, workspace := d.driverFacts()
			if d.bus != nil {
				// Re-emit a DecisionSurface event from the boundary so the TUI
				// can re-project it even after an invalid selection attempt.
				opts := make([]events.DecisionSurfaceOption, 0, len(b.ProposalOptions))
				for _, opt := range b.ProposalOptions {
					opts = append(opts, events.DecisionSurfaceOption{
						ID:          opt.ID,
						Label:       opt.Label,
						Description: opt.Description,
						Intent:      opt.Intent,
					})
				}
				d.bus.Publish(events.NewDecisionSurfaceEvent(
					runID, contractID, target, workspace, string(SurfaceLifecyclePublished),
					b.Reason, b.SurfaceASTStatus, b.SurfaceEstimatedTokens, b.SurfaceCurrentBudget, opts,
				))
			}
		}
		d.publish(ctx)
	}
}

// resolveSurfaceLifecycle marks a parked DecisionSurface resolved by a human
// choice (idempotent: only a parked surface resolves).
func (d *Driver) resolveSurfaceLifecycle(ctx context.Context, reason string) {
	if d.surface == nil {
		return
	}
	d.setSurfaceLifecycle(ctx, SurfaceLifecycleResolved, reason)
}

// emitDecisionSurface publishes the TYPED proposal payload of a DecisionSurface
// on the bus. This is the transport the UI projects into a
// HumanBoundaryProposalMsg — the guarantee that awaiting_human always has a
// renderable decision surface.
func (d *Driver) emitDecisionSurface(ctx context.Context, s DecisionSurface, state SurfaceLifecycle) {
	if d.bus == nil {
		return
	}
	runID, contractID, target, workspace := d.driverFacts()
	d.bus.Publish(events.NewDecisionSurfaceEvent(
		runID, contractID, target, workspace, string(state),
		s.Reason, string(s.ASTStatus), s.EstimatedTokens, s.CurrentBudget,
		surfaceOptionsToEvents(s),
	))
}

// emitRecoveryClassified publishes the typed recovery classification + the
// concrete recovery options of a closed-gate evaluation.
func (d *Driver) emitRecoveryClassified(ctx context.Context, eval PreflightEvaluation, s DecisionSurface) {
	if d.bus == nil {
		return
	}
	runID, contractID, target, workspace := d.driverFacts()
	cat := ClassifyPreflightFailure(eval)
	d.bus.Publish(events.NewRecoveryClassified(runID, contractID, target, workspace, string(cat), s.Reason))
	d.bus.Publish(events.NewRecoveryOptionsCreated(runID, contractID, target, workspace, string(cat), surfaceOptionsToEvents(s)))
}

// emitAutonomousParked publishes the autonomous.parked lifecycle event.
func (d *Driver) emitAutonomousParked(ctx context.Context, reason string) {
	if d.bus == nil {
		return
	}
	runID, contractID, target, workspace := d.driverFacts()
	d.bus.Publish(events.NewAutonomousParked(runID, contractID, target, workspace, reason))
}

// emitAutonomousResumed publishes the autonomous.resumed lifecycle event.
func (d *Driver) emitAutonomousResumed(ctx context.Context, reason string) {
	if d.bus == nil {
		return
	}
	runID, contractID, target, workspace := d.driverFacts()
	d.bus.Publish(events.NewAutonomousResumed(runID, contractID, target, workspace, reason))
}

// emitAutonomousAborted publishes the autonomous.aborted lifecycle event.
func (d *Driver) emitAutonomousAborted(ctx context.Context, reason string) {
	if d.bus == nil {
		return
	}
	runID, contractID, target, workspace := d.driverFacts()
	d.bus.Publish(events.NewAutonomousAborted(runID, contractID, target, workspace, reason))
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

// decideDefault maps a bounded observation onto the closed decision vocabulary
// through the ZERO-TRUST RECOVERY MATRIX (see recovery.go):
//
//	context observation (no outcome)     → continue (execute)
//	changed/created/nochange/completed   → complete (objective satisfied)
//	no_op_objective_satisfied            → complete (claim structurally confirmed)
//	no_op_no_safe_mutation               → ask_human (requires_review hold)
//	pending_approval                     → ask_human (approval gate, patch id)
//	clarification required               → ask_human
//	cancelled/rejected/artifact_rejected → abort (terminal human/permanent)
//	no_op_objective_unresolved           → DecideRecovery (recoverable
//	                                       escalation — never completion)
//	every other failure                  → DecideRecovery: retryability is a
//	                                       function of the canonical FAILURE
//	                                       SUBTYPE (I4), never of a generic
//	                                       execution error.
func decideDefault(o autonomy.Observation, b autonomy.LoopBounds) autonomy.LoopDecision {
	if o.ClarificationRequired {
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "target clarification required before any execution"}
	}
	switch o.Outcome {
	case autonomy.OutcomeChanged, autonomy.OutcomeCreated, autonomy.OutcomeNoChange,
		autonomy.OutcomeCompleted, autonomy.OutcomeArtifactProduced,
		autonomy.OutcomeNoOpObjectiveSatisfied:
		return autonomy.LoopDecision{Action: autonomy.LoopComplete,
			Reason: "objective satisfied: " + string(o.Outcome)}
	case autonomy.OutcomeNoOpNoSafeMutation:
		// Terminal warning (requires_review): candidate mutations were
		// detected below the structural safety threshold. The loop NEVER
		// completes on this outcome — the decision belongs to a human.
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "no-op claim held for review: candidate edits below safety threshold"}
	case autonomy.OutcomePendingApproval:
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "mutation awaiting approval", PatchID: o.PatchID}
	case autonomy.OutcomeCancelled, autonomy.OutcomeRejected, autonomy.OutcomeArtifactRejected:
		return autonomy.LoopDecision{Action: autonomy.LoopAbort,
			Reason: "terminal outcome: " + string(o.Outcome)}
	case "":
		return autonomy.LoopDecision{Action: autonomy.LoopContinue,
			Reason: "objective resolved — execute"}
	default:
		// OutcomeNoOpObjectiveUnresolved falls through here: ClassifyOutcome
		// marks it recoverable, so the matrix escalates through bounded
		// repair cycles to a human instead of completing or aborting outright.
		return DecideRecovery(o, b)
	}
}

// proposalIntentFailed reports whether an observation represents a failure of
// the selected proposal strategy that did NOT alter workspace state. Only such
// state-unchanging failures advance the anti-loop guard; a successful outcome
// never does.
func proposalIntentFailed(o autonomy.Observation) bool {
	switch o.Outcome {
	case autonomy.OutcomeFailed, autonomy.OutcomePatchGenFailed, autonomy.OutcomePatchFailed,
		autonomy.OutcomeApplyFailed, autonomy.OutcomeVerifyFailed, autonomy.OutcomeSkipped,
		autonomy.OutcomeNoOpObjectiveUnresolved, autonomy.OutcomeNoOpNoSafeMutation,
		autonomy.OutcomePreflightInfeasible:
		return true
	default:
		return false
	}
}

// isHallucinatedInDriver reports whether an observation is the N=0
// hallucinated anchor failure (zero match). Distinct from ambiguous N>1.
func isHallucinatedInDriver(o autonomy.Observation) bool {
	lower := strings.ToLower(o.Diagnostic)
	if strings.Contains(lower, "hallucinated anchor") || strings.Contains(lower, "zero match") {
		return true
	}
	return false
}

func isPhysicalOutputBudgetBreach(o autonomy.Observation) bool {
	return strings.Contains(strings.ToLower(o.Diagnostic), "physical output budget breach")
}

// isNonRetryableInDriver reports whether an observation is a
// NonRetryableArtifactError (ambiguous anchors without line-offset).
// Such observations must bypass the retry/recovery loop.
// Note: hallucinated (N=0) is handled separately above.
func isNonRetryableInDriver(o autonomy.Observation) bool {
	lower := strings.ToLower(o.Diagnostic)
	if strings.Contains(lower, "hallucinated anchor") || strings.Contains(lower, "zero match") {
		return false
	}
	if strings.Contains(lower, "non-retryable") && strings.Contains(lower, "ambiguous") {
		return true
	}
	if strings.Contains(lower, "ambiguous anchor") {
		return !strings.Contains(lower, "line-offset")
	}
	if strings.Contains(lower, "ambiguous anchors") {
		return !strings.Contains(lower, "line-offset")
	}
	return false
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
