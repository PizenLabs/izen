package autonomy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/continuation"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/execution/preflight"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/llmstep"
	"github.com/PizenLabs/izen/internal/loop"
	"github.com/PizenLabs/izen/internal/protocol"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// interactionMetadata is retained for package-local compatibility. New
// dispatch paths use selectInteractionContract so explicit bindings and
// classified capabilities cannot be bypassed.
//
//nolint:unused // kept for package-local compatibility with existing callers.
func interactionMetadata(prompt, mode string) (protocol.InteractionContract, *protocol.ContractDescriptor) {
	contract := protocol.SelectInteractionContract(prompt, mode)
	descriptor := protocol.Describe(contract)
	return contract, &descriptor
}

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

	// activeInteraction/activeDescriptor are the semantic contract binding for
	// the current run.  They are kept on the Driver (rather than reconstructed
	// in each dispatcher) so recovery, clarification, and DAG sub-task
	// requests cannot silently drift to a more capable interaction.
	activeInteraction  protocol.InteractionContract
	activeDescriptor   *protocol.ContractDescriptor
	contractExplicit   bool
	contractBindingErr error

	// ── Recovery Contract Mutation ────────────────────────────────────
	mutationStrategy     MutationStrategy
	allowASTBypass       bool
	explicitOutputBudget int
	syntheticSubGoal     string

	// substrate is the execution target — the single authority that executes
	// Proposals. The Driver never mutates the filesystem directly.
	substrate substrate.ProposalExecutor

	// ── Objective Completion Authority (Phase 14) ────────────────────────
	// objectiveAuthority is the ONLY authority permitted to say an objective was
	// PROVEN. The Driver owns the loop, the executor owns execution, and the
	// authority owns the transition into `completed` — the three roles are
	// deliberately distinct so no single component can both do the work and
	// declare it finished.
	objectiveAuthority ObjectiveAuthority
	// preTargets is the durable target state captured BEFORE the lifecycle's
	// first dispatch. It is the only admissible evidence for an idempotent
	// ("already satisfied") claim.
	preTargets map[string]bool
	// derivationNote records what evidence-bound scope derivation concluded for
	// this run — either the evidence that bound a target set, or the reason no
	// target could be bound. It is per-run evidence rather than a log line,
	// because a driver that reports "no target" without ever saying it looked is
	// indistinguishable from one that never looked.
	derivationNote string
	// lastObjective caches the most recent authorization verdict for tests and
	// structured telemetry. It is never consulted to decide anything.
	lastObjective execution.ObjectiveEvaluation
	// lastContract is the canonical TaskContract the last verdict was judged
	// against. Keeping it adjacent to the verdict makes the pairing auditable.
	lastContract execution.TaskContract
	// intents is the lifecycle's ONE canonical intent authority. Preflight, the
	// context compiler and the driver all read it, and only a blocking revision
	// advances it — so no two components can hold conflicting intent states.
	intents *autonomy.IntentAuthority

	// ── Contract Recovery Circuit Breaker (Phase 15) ─────────────────────
	// contractRecoveries counts the strict-contract re-prompts this lifecycle has
	// already spent on a prose-only (zero-artifact) response. It is reset per
	// Run, incremented only when a re-prompt is actually dispatched, and read by
	// authorizeContractRecovery to close the loop. See the constant below for
	// why the bound exists at all.
	contractRecoveries int
	// contractRecoveryExhausted latches the terminal breaker condition so the
	// unsubstantiated verdict is attributable to the contract, not to whatever
	// the observation happened to say last.
	contractRecoveryExhausted bool

	// grants answers whether a full workspace capability grant is in force for
	// this lifecycle (Phase 15). It is the signal that turns a granted workspace
	// capability into a mandatory, synchronous context re-compilation before the
	// preflight barrier is lowered. It is created ONCE with the driver, not per
	// run: a grant issued immediately before the run — which is the common
	// production order, authorize-then-dispatch — would be invisible to an
	// observer that subscribed at run entry.
	grants *grantObserver
	// grantContextSynced records that the grant-gated re-compilation has already
	// run for this lifecycle, so a resumed or re-driven loop does not re-invalidate
	// the context the current attempt is already using.
	grantContextSynced bool

	// behavior is the behavioral execution-and-observation stage. It is nil unless
	// a reasoning backend was wired, which is what keeps every read-only objective
	// and every existing test on exactly the pre-stage path. When present it
	// observes the workspace's real runtime and drives evidence-driven repair
	// through the SAME execution authority — it is a consumer, never a second
	// authority.
	behavior *BehaviorStage
	// lastBehavior is the most recent behavioral stage result, retained so a
	// terminal reason can name the evidence behind it.
	lastBehavior BehaviorResult

	// ── Durable execution ledger ───────────────────────────────────────
	// ledger is the append-only execution record the run is witnessed into
	// (see ledger.go). It is nil unless a store was bound, and every write
	// through it is best-effort with respect to the LOOP: the loop's
	// decisions never depend on whether the journal accepted a line.
	ledger *durable.TaskStore
	// ledgerSession scopes the durable task key so two sessions running the
	// same objective stay two separate records.
	ledgerSession string
	// ledgerTask is the current run's durable task id, empty when no ledger
	// is bound.
	ledgerTask string
	// ledgerLastState is the last loop state written to the journal. Every
	// return path funnels through term(), so this is what keeps a single
	// transition from being appended twice while still recording every
	// genuine state change. The zero value means "nothing written yet" — it
	// is not a valid autonomy.RuntimeState.
	ledgerLastState autonomy.RuntimeState
}

// MaxContractRecoveryAttempts bounds how many times one execution lifecycle
// re-prompts a model that answered a structural artifact contract with prose.
//
// Phase 14 made ErrZeroArtifactsParsed repromptable, which is correct: a single
// prose response is a contract miss, not a task failure. An UNBOUNDED version of
// the same thing is a different bug — a model that has settled on prose keeps
// settling on prose, and each attempt spends real provider billing to confirm
// it. Two is the bound because it separates "the model needed to be told" (one
// re-prompt, after the Phase 15 system-channel contract) from "the model will
// not speak this contract" (a human decision, not a retry).
//
// The bound is deliberately NOT a completion claim and NOT a failure: when it is
// consumed the lifecycle terminates UNSUBSTANTIATED with
// execution.ErrContractRecoveryExhausted, because the honest description of
// that run is "the workspace was not changed and nothing was proven".
const MaxContractRecoveryAttempts = 2

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

// WithGrantLedger binds the authoritative session capability ledger to the
// grant-gated context barrier (Phase 15).
//
// The ledger is consulted on every gate query, not snapshotted, so a grant issued
// at any point in the run — including the common authorize-then-dispatch order,
// where the grant strictly precedes the run — is visible to the gate. Passing nil
// leaves the gate with the event bus alone, which still catches grants issued
// while a run is in flight.
func WithGrantLedger(l *autonomy.GrantLedger) Option {
	return func(d *Driver) {
		if d == nil {
			return
		}
		// The observer already exists and already owns a bus subscription.
		// Rebinding it is deliberate: constructing a replacement would orphan the
		// first subscription, and a subscription owns a dispatch goroutine, so the
		// driver would leak one per option application.
		if d.grants == nil {
			d.grants = newGrantObserver(d.bus, l)
			return
		}
		d.grants.bindLedger(l)
	}
}

// WithSubstrate wires the Substrate execution target. When set, the Driver
// holds the Substrate as its mutation authority; Strategies emit Proposals
// that execute via Substrate, never directly.
func WithSubstrate(s substrate.ProposalExecutor) Option {
	return func(d *Driver) {
		if s != nil {
			d.substrate = s
		}
	}
}

// WithSubcommand sets the policy scope ($prompt / $hot) used to tailor the
// Zero-Token DecisionSurface option set when the preflight hard-gate diverts a
// corrupt-AST / closed-gate target away from DAG decomposition.
func WithSubcommand(s string) Option {
	return func(d *Driver) { d.subcommand = s }
}

// WithInteractionContract binds the semantic interaction contract used by the
// driver and all of its dispatch/recovery requests.  The descriptor is
// optional; when omitted the contract's conservative default is used.  The
// variadic form keeps the option convenient for callers that only need to
// select the contract kind.
func WithInteractionContract(contract protocol.InteractionContract, descriptors ...*protocol.ContractDescriptor) Option {
	return func(d *Driver) {
		if d == nil {
			return
		}
		descriptor := protocol.Describe(contract)
		if len(descriptors) > 0 && descriptors[0] != nil {
			descriptor = descriptors[0].Clone()
		}
		normalized, err := descriptor.Normalize()
		if err != nil {
			d.contractBindingErr = err
			return
		}
		if contract.Valid() && normalized.Contract != contract {
			d.contractBindingErr = fmt.Errorf("autonomy: contract binding mismatch: descriptor=%q requested=%q", normalized.Contract, contract)
			return
		}
		if !contract.Valid() {
			contract = normalized.Contract
		}
		d.activeInteraction = contract
		d.contractExplicit = true
		copy := normalized.Clone()
		d.activeDescriptor = &copy
		d.contractBindingErr = nil
	}
}

// WithContract is a descriptor-first alias for WithInteractionContract.
func WithContract(descriptor protocol.ContractDescriptor) Option {
	return WithInteractionContract(descriptor.Contract, &descriptor)
}

// Substrate returns the execution target.
func (d *Driver) Substrate() substrate.ProposalExecutor {
	if d == nil {
		return nil
	}
	return d.substrate
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
		// The completion authority is domain code with no UI dependency: the
		// driver stays fully executable and testable headless.
		objectiveAuthority: objectiveReducer{},
		grants:             newGrantObserver(bus, nil),
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
	d.lastObjective = execution.ObjectiveEvaluation{}
	d.lastContract = execution.TaskContract{}
	d.preTargets = nil
	d.contractRecoveries = 0
	d.contractRecoveryExhausted = false
	d.derivationNote = ""
	// A fresh run is a fresh LIFECYCLE, and the canonical intent is per
	// lifecycle. Reusing the previous run's authority would let an elevation
	// leak into a read-only objective — the exact split-brain this authority
	// exists to prevent, one run later.
	d.intents = autonomy.NewIntentAuthority()
	// ── PHASE 15: GRANT OBSERVATION ──────────────────────────────────
	// The per-lifecycle observation resets here; the subscription and the ledger
	// binding live as long as the driver does. A fresh lifecycle observes only
	// authorizations issued for it, so a grant issued for one objective cannot
	// gate the context of an entirely different one.
	d.grants.reset()
	d.grantContextSynced = false
	// A fresh run is a fresh behavioral lifecycle. Carrying the previous run's
	// verdict forward would let one run's PROVEN observation stand in for
	// another's, which is the exact split-brain the behavioral gate exists to
	// prevent.
	d.lastBehavior = BehaviorResult{}
	d.runRequestID = fmt.Sprintf("run-%d", d.runID)
	d.loop.Start("user objective: " + objective)
	d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context

	// Target resolution is the gateway's deterministic authority; the loop
	// never guesses a target. A clarification boundary parks BEFORE any
	// execution — no model call, no mutation.
	d.resolved = d.adapter.Resolve(objective)
	// ── EVIDENCE-BOUND SCOPE DERIVATION ─────────────────────────────────
	// A broad objective ("redesign the portfolio page using HTML, CSS and JS")
	// names no file, so the gateway classifies it read-only and the driver would
	// spend a provider call on an empty workspace before noticing there is
	// nothing to read. That is the exact shape of the reported failure:
	//
	//	authorization → context compiled: 0 channels → provider called
	//	→ target=unknown → artifact_missing → objective unproven
	//
	// The fix is not to trust the prompt's word "HTML" as a filename. It is to
	// OBSERVE the workspace and bind the files that satisfy the artifact kinds
	// the objective itself declared. When that yields a target set, the run
	// becomes an evidence-backed mutation and the whole existing machinery —
	// canonical resolution, OCC baseline, MutationSet, approval, verification —
	// applies unchanged. When it yields nothing, the run stays read-only and the
	// emptiness is reported truthfully rather than papered over.
	d.deriveEvidenceScope()
	interaction, interactionDescriptor, contractErr := d.selectInteractionContract(objective)
	if contractErr != nil {
		_, _ = d.loop.Abort("interaction contract binding failed: "+contractErr.Error(), autonomy.FailurePermanent)
		d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context
		d.releaseRunResources()
		return nil, fmt.Errorf("autonomy: interaction contract: %w", contractErr)
	}
	// The strategy gateway is the deterministic authority for whether this
	// run can act.  If it selected a mutation strategy but the text classifier
	// produced a read-only contract, promote only to the bounded AgenticLoop
	// contract; never promote a read-only strategy in the opposite direction.
	if !d.contractExplicit && (d.resolved.Profile.Strategy == strategy.TargetedMutation || d.resolved.Profile.Strategy == strategy.DirectDeterministic) {
		interaction = protocol.AgenticLoop
		descriptor := protocol.Describe(interaction)
		interactionDescriptor = &descriptor
	}
	d.activeInteraction = interaction
	d.activeDescriptor = interactionDescriptor
	d.req = autonomy.LoopRequest{
		RequestID:           d.runRequestID,
		Prompt:              objective,
		Targets:             d.resolved.Targets,
		StreamCallback:      d.streamCb,
		WorkspaceDigest:     d.adapter.WorkspaceVersion(d.resolved.Targets),
		InteractionContract: interaction,
		Contract:            interactionDescriptor,
	}
	// The durable execution record opens HERE: once the objective and its
	// resolved targets are known, and before anything can be dispatched, so
	// an interruption from here on is reconstructable.
	d.ledgerBeginRun(objective)
	// ── PHASE 12: DERIVE THE RUN-LEVEL TOKEN BOUND ──────────────────────
	// The run-level budget is derived from the per-invocation budget this run
	// is actually bound to, so a legitimate multi-invocation task is not
	// truncated by a fixed constant. `WidenBounds` only ever RAISES a floor, so
	// an explicit `WithLoopBounds` from an operator is never reduced. Authority
	// is untouched: the bound terminates a run, it never admits work.
	d.loop.WidenBounds(0, 0,
		autonomy.RunTokenBudget(d.resolved.Profile.MaxOutputTokens,
			d.loop.Bounds().MaxAttempts,
			llmstep.DefaultMaxContinuationSteps), 0)

	// Adaptive heuristic (Task 1): bypass FULL_REWRITE for large targets
	// or small model budgets; force BOUNDED_PATCH as initial strategy.
	if len(d.resolved.Targets) > 0 && d.resolved.Targets[0] != "" {
		targetFile := d.resolved.Targets[0]
		fileSize := autonomy.FileSizeBytes(targetFile)
		if adaptive := autonomy.AdaptiveSelectStrategy(targetFile, fileSize, d.resolved.Profile.MaxOutputTokens); adaptive == autonomy.StrategyBoundedPatch {
			d.mutationStrategy = StrategyBoundedPatch
		}
	}
	// ── PHASE 14: PRE-EXECUTION TARGET PROVENANCE ─────────────────────
	// Capture the durable target state BEFORE any dispatch. This is the only
	// admissible evidence for an idempotent ("already satisfied") claim: a
	// post-hoc reading cannot distinguish "already done" from "done by this
	// run", and a completion authority that trusted one would rubber-stamp
	// execution inertia.
	d.capturePreExecutionTargets()
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
	if err := ValidateObjectiveContract(objective, interaction, interactionDescriptor); err != nil {
		_, _ = d.loop.Abort("interaction contract authority ceiling: "+err.Error(), autonomy.FailurePermanent)
		d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context
		d.releaseRunResources()
		return nil, err
	}
	// ── PHASE 14: CANONICAL INTENT AUTHORITY ───────────────────────────
	// Synchronize the lifecycle's ONE canonical intent before the loop
	// observes anything. When the strategy gateway elevates a read-only
	// classification to a mutation contract, this performs the blocking
	// revision: the previous intent and every artefact derived from it are
	// invalidated, the canonical intent is synchronized, and the workspace
	// context is re-compiled under the modification contract. A re-compilation
	// that cannot satisfy the contract parks the run instead of dispatching a
	// mutation over a read-only context.
	if err := d.syncCanonicalIntent(d.runCtx, objective); err != nil { //nolint:contextcheck // runCtx is the run's own cancellation context
		canonical, _ := d.intentAuthority().Current()
		d.loop.AwaitHuman(autonomy.HumanBoundary{
			Reason: "canonical intent revision incomplete: " + err.Error() +
				fmt.Sprintf(" — the run holds intent %q with no valid workspace context for it; re-scope the objective or grant an explicit mutation contract", canonical),
			Targets: d.objectiveTargets(),
		})
		d.enrichBoundary()
		d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context
		// A parked run terminates nothing, exactly like the preflight-infeasible
		// and NO-OP-escalation paths: the reason travels on the boundary, not
		// as a raw error, so the human gets a decision instead of a stack.
		return d.term(), nil //nolint:nilerr // the incomplete revision is a park, not a run failure
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
	if err := ValidateDispatchContract(autonomy.LoopRequest{
		Prompt:              d.prompt,
		Target:              firstTarget(d.req.Targets),
		Targets:             append([]string(nil), d.req.Targets...),
		Intent:              "modification",
		MutationStrategy:    StrategyFullRewrite.String(),
		InteractionContract: d.req.InteractionContract,
		Contract:            cloneContract(d.req.Contract),
	}); err != nil {
		return d.term(), err
	}
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
			d.releaseRunResources()
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
		d.releaseRunResources()
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
			d.releaseRunResources()
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
	interaction, interactionDescriptor, contractErr := d.currentInteractionMetadata()
	if contractErr != nil {
		return d.term(), fmt.Errorf("autonomy: interaction contract: %w", contractErr)
	}
	// Preserve the active contract and request identity across clarification;
	// only the deterministic target is replaced.
	d.req.Prompt = d.prompt
	d.req.Target = target
	d.req.Targets = []string{target}
	d.req.WorkspaceDigest = d.adapter.WorkspaceVersion([]string{target})
	d.req.InteractionContract = interaction
	d.req.Contract = interactionDescriptor
	d.req.RecoveryAttempt = 0
	d.req.RecoveryStrategy = ""
	d.req.RecoveryReason = ""
	d.req.ParentContractID = ""
	d.req.StagedPlan = nil
	d.req.FocusStartLine, d.req.FocusEndLine = 0, 0
	// A human-specified target replaces the declared set, so the pre-execution
	// provenance is re-captured against the NEW target before dispatch. It is a
	// first observation of a changed scope, not a refresh of an existing claim.
	d.capturePreExecutionTargets()
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
		d.releaseRunResources()
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
		d.releaseRunResources()
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

// ContractRecoveryState reports the circuit breaker's accounting for the current
// lifecycle: how many strict-contract re-prompts have been spent, the bound they
// are spent against, and whether the terminal condition has latched. It is
// observable state for telemetry and tests — it is never consulted to decide
// anything; authorizeContractRecovery reads the counter directly.
func (d *Driver) ContractRecoveryState() (used, limit int, exhausted bool) {
	if d == nil {
		return 0, MaxContractRecoveryAttempts, false
	}
	return d.contractRecoveries, MaxContractRecoveryAttempts, d.contractRecoveryExhausted
}

// authorizeContractRecovery is the CONTRACT RECOVERY CIRCUIT BREAKER. It runs on
// every proposed decision, beside the completion authority, and closes the one
// path Phase 14 deliberately left open: an unbounded re-prompt at a model that
// does not speak the artifact contract.
//
// It is a MUTATION of the decision, not a veto, for the same reason the
// completion authority mutates: the loop history must record the transition that
// actually happened. A prose-only observation normally earns LoopRepair; once
// MaxContractRecoveryAttempts re-prompts have been spent on it, the repair is
// rewritten into LoopUnsubstantiate and the reason names
// execution.ErrContractRecoveryExhausted, so the terminal state is
// UNSUBSTANTIATED — the workspace was not changed, the transport was healthy, and
// nothing was proven. That is neither a success nor a fabricated failure.
func (d *Driver) authorizeContractRecovery(decision *autonomy.LoopDecision) {
	if d == nil || decision == nil {
		return
	}
	if RecoverySubtype(d.obs) != SubtypeZeroArtifacts {
		return
	}
	if d.contractRecoveries < MaxContractRecoveryAttempts {
		return
	}
	d.contractRecoveryExhausted = true
	reason := fmt.Sprintf(
		"objective UNSUBSTANTIATED (%s): %d strict artifact-contract re-prompt(s) were spent on %s and the provider still returned prose instead of a structural artifact — no file was changed",
		execution.ErrContractRecoveryExhausted.Error(), d.contractRecoveries, contractRecoveryTarget(d.obs))
	if d.bus != nil {
		d.bus.Publish(events.NewActivity("[contract] recovery breaker: " + reason))
	}
	decision.Action = autonomy.LoopUnsubstantiate
	decision.Reason = reason
	// A contract the model will not speak is not a decision the human can make
	// from a menu of re-scopes: every option in the proposal vocabulary re-prompts
	// under some contract, and the contract is not the problem. Clearing the patch
	// reference keeps the terminal state free of a held artifact nobody asked for.
	decision.PatchID = ""
}

// contractRecoveryTarget names the target a prose-only response was attributed
// to, for the terminal reason. It never carries response bytes (Recovery
// Isolation): the model is told what it produced was discarded, not what it said.
func contractRecoveryTarget(o autonomy.Observation) string {
	if o.Target != "" {
		return o.Target
	}
	return "the requested artifact"
}

// SetStreamCallback sets a callback for incremental streaming progress during
// the next provider invocation. It is called by the UI before Run.
func (d *Driver) SetStreamCallback(cb execution.StreamCallback) {
	d.streamCb = cb
}

// ActiveContract returns a defensive copy of the descriptor currently bound
// to the driver, or nil when the driver has not selected a contract yet.
func (d *Driver) ActiveContract() *protocol.ContractDescriptor {
	if d == nil || d.activeDescriptor == nil {
		return nil
	}
	copy := d.activeDescriptor.Clone()
	return &copy
}

// InteractionContract returns the semantic contract currently bound to the
// driver.  A zero value means selection is still pending for the next run.
func (d *Driver) InteractionContract() protocol.InteractionContract {
	if d == nil {
		return ""
	}
	return d.activeInteraction
}

// selectInteractionContract derives the contract from the classified intent
// when no explicit binding was supplied.  The mode passed to the pure selector
// is the autonomy dispatch mode, not a provider capability declaration; a
// read-only objective therefore cannot inherit mutation authority merely
// because the adapter is running on the build surface.
func (d *Driver) selectInteractionContract(objective string) (protocol.InteractionContract, *protocol.ContractDescriptor, error) {
	if d != nil && d.contractBindingErr != nil {
		return "", nil, d.contractBindingErr
	}
	if d != nil && d.contractExplicit && d.activeInteraction.Valid() {
		descriptor := protocol.Describe(d.activeInteraction)
		if d.activeDescriptor != nil {
			descriptor = d.activeDescriptor.Clone()
		}
		normalized, err := descriptor.Normalize()
		if err != nil {
			return "", nil, err
		}
		return normalized.Contract, func() *protocol.ContractDescriptor { copy := normalized.Clone(); return &copy }(), nil
	}
	classified := autonomy.Classify(objective, nil)
	caps := make([]string, 0, len(classified.Required))
	for _, capability := range classified.Required {
		caps = append(caps, string(capability))
	}
	contract := protocol.SelectInteractionContract(objective, "autonomy", caps...)
	descriptor := protocol.Describe(contract)
	return contract, &descriptor, nil
}

func (d *Driver) currentInteractionMetadata() (protocol.InteractionContract, *protocol.ContractDescriptor, error) {
	if d != nil && d.req.InteractionContract.Valid() {
		var descriptor *protocol.ContractDescriptor
		if d.req.Contract != nil {
			descriptor = d.req.Contract
		} else {
			value := protocol.Describe(d.req.InteractionContract)
			descriptor = &value
		}
		normalized, err := descriptor.Clone().Normalize()
		if err != nil {
			return "", nil, err
		}
		if d.req.InteractionContract.Valid() && normalized.Contract != d.req.InteractionContract {
			return "", nil, fmt.Errorf("autonomy: %w: active request contract %q does not match descriptor %q", protocol.ErrInvalidContract, d.req.InteractionContract, normalized.Contract)
		}
		copy := normalized.Clone()
		return normalized.Contract, &copy, nil
	}
	return d.selectInteractionContract(d.prompt)
}

// ── drive helpers ───────────────────────────────────────────────────────────

// observeAndRun pushes the current observation through Observe → decide →
// execute until the loop terminates or parks at AwaitingHuman.
// runID is the identity of the run that started this observation loop;
// late results from a different runID are discarded.
//
// deriveEvidenceScope observes the workspace and binds an evidence-backed target
// set for an objective that names no file but DOES declare artifact kinds.
//
// It runs ONCE, before admission, and it is the only place a run acquires a
// target it was not given. Three properties make it safe to run here:
//
//   - It is a no-op when the gateway already resolved a target. A stated or
//     canonical target outranks anything discovery could suggest.
//   - It requires the objective to DECLARE a kind. "Make this better" declares
//     nothing and therefore derives nothing; only "…using HTML, CSS and JS"
//     names file extensions, and only extension-matching observed files qualify.
//   - It mutates nothing and bills nothing. Discovery is a bounded read; the
//     decision is a pure projection of it.
//
// A successful derivation rewrites the run's strategy to the canonical mutation
// contract over the derived targets, because that is now what the run IS: a
// bounded mutation of proven, observed files. Everything downstream — canonical
// re-resolution, OCC baseline, MutationSet, the approval gate, verification —
// then runs on its existing, unchanged path.
//
// A failed derivation is NOT an error and NOT a fabrication. The run keeps its
// read-only classification and the refusal is recorded on the run so a later
// report can say exactly why no target was bound.
func (d *Driver) deriveEvidenceScope() {
	if d.adapter == nil {
		return
	}
	// A resolved target set is already authoritative. Deriving over it would be
	// second-guessing the gateway with a scan.
	if len(d.resolved.Targets) > 0 {
		return
	}
	derivation := d.adapter.DeriveScope(d.prompt, nil)
	if !derivation.Derivable || len(derivation.Targets) == 0 {
		if derivation.Reason != "" {
			d.derivationNote = derivation.Reason
			diagnosticf("[scope] no evidence-bound target derived: %s", derivation.Reason)
			if d.bus != nil {
				d.bus.Publish(events.NewActivity("[scope] " + derivation.Reason))
			}
		}
		return
	}

	// Re-resolve the strategy over the DERIVED objective. This is what keeps
	// strategy selection in its existing authority: the gateway still decides
	// the contract, it simply now sees the scope the objective is actually
	// about instead of an empty one. No second strategy selector is introduced
	// and no strategy is hand-assembled here.
	boundPrompt := d.prompt + " " + joinTargets(derivation.Targets)
	profile := d.adapter.SelectStrategy(boundPrompt)
	if profile.Strategy != strategy.TargetedMutation {
		// The gateway still declines to treat the derived scope as a mutation
		// target. That is a real disagreement between the observed evidence and
		// the classifier, and resolving it by force would be exactly the
		// invention this step exists to avoid. Record it and stay read-only.
		d.derivationNote = "observed files " + strings.Join(derivation.Targets, ",") +
			" satisfy the declared artifact kinds, but the strategy gateway still classifies the objective read-only (" +
			string(profile.Strategy) + "); no mutation was dispatched"
		diagnosticf("[scope] derivation produced %v but gateway selected %s — no dispatch",
			derivation.Targets, profile.Strategy)
		if d.bus != nil {
			d.bus.Publish(events.NewActivity("[scope] " + d.derivationNote))
		}
		return
	}

	var proven []string
	for _, t := range profile.Targets {
		if t.Resolved != "" {
			proven = append(proven, t.Resolved)
		}
	}
	if len(proven) == 0 {
		d.derivationNote = "derived candidates did not resolve to existing workspace files: " + derivation.Reason
		diagnosticf("[scope] %s", d.derivationNote)
		return
	}

	d.resolved.Profile = profile
	d.resolved.Targets = proven
	d.derivationNote = derivation.Reason
	diagnosticf("[scope] evidence-bound derivation: %v (kinds=%v) — %s",
		proven, derivation.Kinds, derivation.Reason)
	if d.bus != nil {
		d.bus.Publish(events.NewActivity(fmt.Sprintf(
			"[scope] observed workspace evidence bound %d target(s) %v — %s",
			len(proven), proven, derivation.Reason)))
	}
}

// joinTargets renders derived targets as @scope tokens so the canonical gateway
// parses them through its EXISTING @scope path — the same path a human-typed
// target takes. Nothing about the gateway's target semantics is bypassed.
func joinTargets(targets []string) string {
	parts := make([]string, 0, len(targets))
	for _, t := range targets {
		parts = append(parts, "@"+t)
	}
	return strings.Join(parts, " ")
}

// Preflight barrier: when wired, the transition from observing to deciding is
// gated by PreflightSyncBarrier (10s timeout → PREFLIGHT_TIMEOUT). This is the
// execution invariant: async discovery never means unverified execution.
// preflightExecutionSpec builds the admission-time view of the run for the
// Phase 16.1 pre-flight gate. Assembling it invokes NO provider and mutates
// nothing: it resolves the target through the executor's resolver (pure
// classification + isolated discovery) and records the boundary the run holds.
//
// ctx is the run's own cancellation context, threaded so resolution is
// interruptible: a cancelled run resolves to an unsubstantiated verdict and
// stops, rather than completing a bounded scan nobody is waiting for.
func (d *Driver) preflightExecutionSpec(ctx context.Context) ExecutionSpec {
	spec := ExecutionSpec{Intent: intentClassForStrategy(d.resolved.Profile.Strategy)}
	if !spec.Intent.IsMutation() {
		return spec
	}
	binding := d.adapter.PreflightTarget(ctx, d.prompt, d.resolved.Targets)
	spec.TargetBinding = &binding
	if binding.Profile != nil {
		spec.WorkspaceEvidence = binding.Profile
	}
	spec.ExplicitTargets = append([]string(nil), d.resolved.Targets...)
	// A proven target binds the boundary directly; an explicitly named creation
	// target binds it by statement. Anything else is UNBOUND.
	explicit := provenExplicit(spec.ExplicitTargets)
	if binding.Dispatchable() || (binding.State == execution.TargetStateNotFound && len(explicit) > 0) {
		spec.MutationBoundary = MutationBoundaryBound
	} else {
		spec.MutationBoundary = MutationBoundaryUnbound
	}
	// A resolved, existing target is an authoritative context channel: it is
	// runtime-observed evidence the prompt can be compiled from. An explicitly
	// named creation target is a channel by statement. A directory or an
	// ambiguous statement is NOT a channel — it is a question. Zero channels on
	// a mutation objective is the empty-context condition I12 forbids.
	channels := provenExplicit(binding.Paths)
	if len(channels) == 0 && binding.State == execution.TargetStateNotFound {
		channels = explicit
	}
	for _, target := range channels {
		spec.ContextChannels = append(spec.ContextChannels, ContextChannel{
			Kind:          "target",
			Source:        target,
			Authoritative: true,
		})
	}
	if len(spec.ContextChannels) > 0 {
		spec.Evidence = EvidenceProduced
	}
	return spec
}

// intentClassForStrategy maps the deterministic execution strategy onto the
// coarse admission intent. It is a pure projection: the strategy is already
// the authority, this only names the mutation/read-only distinction the gate
// reasons over.
func intentClassForStrategy(s strategy.ExecutionStrategy) IntentClass {
	switch s {
	case strategy.TargetedMutation, strategy.DirectDeterministic:
		return IntentMutate
	case strategy.MultiFilePlanning:
		return IntentPlan
	case strategy.RepositoryInvestigation:
		return IntentInvestigate
	default:
		return IntentAsk
	}
}

// observeAndRun runs the bounded observe → decide → execute loop. It is reached
// only after the run's context has been synchronized.
func (d *Driver) observeAndRun(ctx context.Context, runID uint64) (*autonomy.LoopTermination, error) {
	// ── PHASE 15: GRANT-GATED CONTEXT BARRIER ───────────────────────────
	// This is the LAST point before the loop can leave `observing`, and
	// therefore the last point at which a context frozen before capability
	// authorization could still be used. A granted workspace capability is
	// turned into a freshly compiled, verified non-empty target context HERE,
	// synchronously, and a failure parks the run instead of dispatching a model
	// over a workspace it was never shown.
	//
	// It runs before the preflight barrier rather than after it on purpose: the
	// barrier's whole purpose is to guarantee that no execution happens over
	// unverified structure, and "unverified structure" includes an unpopulated
	// prompt.
	if err := d.syncGrantedWorkspaceContext(ctx); err != nil { //nolint:contextcheck // ctx is the run's own cancellation context
		return d.parkOnGrantContextFailure(ctx, err), nil
	}
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

	// ── PHASE 16.1: PRE-FLIGHT ADMISSION GATE ──────────────────────────
	// "No Evidence, No Provider" (I12) is enforced HERE, at the last point
	// before the loop can plan or dispatch a provider. A mutation objective
	// without a proven target, an admissible mutation boundary and
	// authoritative evidence parks at a human boundary or aborts — no provider
	// token is billed. A directory target (I11) can never be promoted to a file
	// on the way through.
	if outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(ctx)); outcome.Blocked() {
		if outcome.Verdict == AdmissionDisambiguate {
			if d.bus != nil {
				d.bus.Publish(events.NewActivity("[preflight] target unresolved — awaiting disambiguation: " + outcome.Reason))
			}
			d.loop.AwaitHuman(autonomy.HumanBoundary{
				Reason:  outcome.Reason,
				Options: outcome.Candidates,
			})
			d.enrichBoundary()
			d.publish(ctx)
			return d.term(), nil
		}
		if d.bus != nil {
			d.bus.Publish(events.NewActivity("[preflight] inadmissible target: " + outcome.Reason))
		}
		return d.terminateAbort(ctx, "preflight inadmissible target: "+outcome.Reason, autonomy.FailurePermanent), nil
	}
	for !d.loop.State().IsTerminal() {
		// Loop boundary: snapshot the durable view so "where was this run
		// when the process stopped" is readable without a full replay.
		d.ledgerCheckpoint(d.loop.State())
		// Late-result guard: if the run was aborted/superseded, exit immediately.
		if d.runID != runID {
			return d.term(), nil
		}
		// ── EXPLICIT CANCELLATION GATE ─────────────────────────────────
		// The loop has no channel receives; this select is the inter-step
		// cancellation boundary. The in-flight executor call below receives
		// ctx and honours it (provider HTTP requests are built with
		// http.NewRequestWithContext), so Esc / Ctrl+C breaks both a blocked
		// provider call and the next inter-step window immediately.
		select {
		case <-ctx.Done():
			// Cancellation is a clean permanent abort, not a propagated error.
			return d.terminateAbort(ctx, "context cancelled", autonomy.FailurePermanent), nil //nolint:nilerr // termination, not a failure
		default:
		}
		switch d.loop.State() {
		case autonomy.RuntimeDeciding, autonomy.RuntimeInterpreting:
			// The loop owns the attempt/cycle counters; the decider sees them
			// through the bounded observation so the recovery matrix can
			// decide "exhausted → ask human" from the authoritative facts.
			d.obs.AttemptNum = d.loop.Attempts()
			d.obs.RecoveryCycle = d.loop.RecoveryCycles()
			decision := d.decide(d.obs, d.loop.Bounds())
			// ── PHASE 14: OBJECTIVE COMPLETION AUTHORITY ────────────────
			// The decision matrix proposes; the authority disposes. A proposed
			// completion is rewritten into the terminal state that matches WHY
			// the evidence failed, so `interpreting -> completed` is reachable
			// only when the Task Contract was actually satisfied. A provider
			// that returned, a step that came back nil and a verifier that
			// merely ran are structurally incapable of producing it.
			d.authorizeObjectiveCompletion(&decision)
			// ── BEHAVIORAL PROOF GATE ───────────────────────────────────
			// An objective that asks for a verifiable RESULT cannot be declared
			// complete on the strength of an applied mutation alone. The workspace
			// must be observed RUNNING and its observable requirements PROVEN.
			//
			// This gate runs AFTER the completion authority so it can only ever
			// REMOVE a completion, never grant one: the existing authority decides
			// whether the mutation contract was satisfied, and this decides whether
			// the objective's behavioural claim is. An objective that does not
			// require behavioural proof (a read, a document write) is untouched,
			// and a run with no behavioral stage wired keeps its exact prior
			// behaviour.
			d.authorizeBehavioralCompletion(ctx, &decision)
			// ── PHASE 15: CONTRACT RECOVERY CIRCUIT BREAKER ───────────────
			// A prose-only response is repromptable, but only a bounded number
			// of times. Once the bound is spent the proposed repair is rewritten
			// into the terminal UNSUBSTANTIATED state, so the loop can neither
			// claim the objective nor burn the remaining budget asking a model
			// that has already declined to speak the contract twice.
			d.authorizeContractRecovery(&decision)
			// ── PHASE 12: CANONICAL CONTINUATION CONSULTATION ────────────
			// An exhausted invocation is an INVOCATION outcome, not a task
			// failure. Before the matrix acts on it, the pure continuation
			// library classifies the durable task state — it already
			// distinguishes "partial but still advanceable" from "nothing
			// evidence-backed remains". A verdict that is NOT "continue"
			// escalates to the human instead of spending another attempt; the
			// library never re-enters execution by itself.
			if decision.Action == autonomy.LoopRepair &&
				RecoverySubtype(d.obs) == SubtypeOutputExhausted {
				if escalated := d.consultContinuationOnExhaustion(); escalated {
					return d.term(), nil
				}
			}
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
			// Contract validation is a dispatcher boundary, not a provider
			// concern.  Reject an out-of-ceiling staged operation before the
			// adapter can create a model request or a held patch.
			if contractErr := ValidateDispatchContract(d.req); contractErr != nil {
				term := d.terminateAbort(ctx, "interaction contract authority ceiling: "+contractErr.Error(), autonomy.FailurePermanent)
				return term, contractErr
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
				// ── M6 PURE CONTINUATION CONFIRMATION (library, not runtime) ──
				// Confirm the halt through the pure transition function: with
				// caller-observed drift it must agree this is STALE. The
				// recovery matrix below remains the decision owner — the abort
				// is taken regardless; a disagreement is a bug signal, logged
				// for forensics, never a second decision path.
				// Terminate as a permanent abort — never verified as execution.
				if cont := DeriveDriverContinuation(DriverContinuationInput{
					Objective:        d.prompt,
					Targets:          append([]string(nil), d.req.Targets...),
					StateFingerprint: d.req.WorkspaceDigest,
					HasStaleState:    true,
					PreviousOutcome:  driverOutcomeToStepOutcome(obs.Outcome),
					AllowedScope:     append([]string(nil), d.req.Targets...),
					ProviderCeiling:  obs.MaxOutputTokens,
				}); cont.Action != continuation.ActionStale {
					diagnosticf("[boundary5] continuation library disagrees on drift: action=%s reason=%s (matrix abort still taken)",
						cont.Action, cont.Reason)
				}
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
			// The step's observation is the only thing that happened to the
			// workspace; it goes into the journal before the loop can decide
			// anything about it.
			d.ledgerObserved(obs)
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
			// Recovery may change artifact strategy and causal lineage, but it
			// must not change the active interaction contract.  Rebind the
			// request to the driver's immutable descriptor even when an injected
			// RepairFunc returns a freshly assembled LoopRequest.
			if d.req.InteractionContract.Valid() {
				activeContract, activeDescriptor, metadataErr := d.currentInteractionMetadata()
				if metadataErr != nil {
					return nil, fmt.Errorf("autonomy: recovery contract: %w", metadataErr)
				}
				if req.InteractionContract != "" && req.InteractionContract != activeContract {
					return nil, fmt.Errorf("autonomy: recovery contract drift: request=%s active=%s", req.InteractionContract, activeContract)
				}
				req.InteractionContract = activeContract
				req.Contract = cloneContract(activeDescriptor)
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
			// ── PHASE 15: SPEND THE CONTRACT RECOVERY BUDGET ───────────────
			// The counter is incremented where the re-prompt is actually
			// dispatched, not where the decision to re-prompt was proposed, so it
			// measures real spend. A recovery that the matrix refused (and that
			// therefore parked the run) never consumed a slot.
			if RecoverySubtype(d.obs) == SubtypeZeroArtifacts {
				d.contractRecoveries++
			}
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

// consultContinuationOnExhaustion asks the pure continuation library to
// classify the durable task state after an exhausted invocation and maps a
// non-CONTINUE verdict onto an explicit human decision.
//
// It returns true when the run was parked (the caller must stop), false when
// the library agrees the task can continue under the matrix's own decision.
//
// The library is a PROPOSAL function: it never schedules, never authorizes and
// never re-enters execution. Only a non-continue verdict is acted on, and it
// is acted on by parking the loop — the most conservative possible outcome.
func (d *Driver) consultContinuationOnExhaustion() bool {
	allowed := append([]string(nil), d.req.Targets...)
	if len(allowed) == 0 {
		allowed = append(allowed, d.resolved.Targets...)
	}
	// Durable, transcript-free task state: the plan's own unit bookkeeping when
	// a decomposition is staged, otherwise the current attempt itself. A
	// monolithic (non-decomposed) attempt is always pending work until the
	// executor reports otherwise.
	var completed, pending []string
	if plan := d.Plan(); plan != nil && len(plan.SubTasks) > 0 {
		for i, st := range plan.SubTasks {
			// Units before the current cursor have been dispatched; the DAG
			// reports how many were satisfied without a mutation.
			if i < plan.NoOpSatisfiedSubTasks {
				completed = append(completed, st.ID)
				continue
			}
			pending = append(pending, st.ID)
		}
	}
	if len(pending) == 0 && d.req.Target != "" {
		pending = append(pending, d.req.Target)
	}
	if len(pending) == 0 && len(allowed) > 0 {
		pending = append(pending, allowed...)
	}
	cont := DeriveDriverContinuation(DriverContinuationInput{
		Objective:        d.prompt,
		Targets:          d.resolved.Targets,
		StateFingerprint: d.req.WorkspaceDigest,
		IsPartialOutput:  true,
		PreviousOutcome:  "partial",
		PreviousReason:   "OUTPUT_CEILING",
		Verified:         d.obs.Verification.Passed,
		AllowedScope:     allowed,
		ProviderCeiling:  d.obs.MaxOutputTokens,
		CompletedSteps:   completed,
		PendingSteps:     pending,
		Observations: []continuation.Observation{{
			Kind:             continuation.KindExecutionResult,
			Subject:          d.obs.Target,
			Detail:           "model invocation exhausted its output ceiling",
			StateFingerprint: d.req.WorkspaceDigest,
			Timestamp:        time.Now(),
		}},
	})
	if cont.Action == continuation.ActionContinue {
		// The pure function agrees the task is still advanceable: the matrix's
		// own decision stands and the run re-enters the SAME executor.
		return false
	}
	// Any other verdict is an explicit human decision, never a silent retry and
	// never a fabricated completion.
	reason := "continuation blocked after output exhaustion: " + cont.Reason
	if cont.Action == continuation.ActionAwaitingApproval {
		reason = "continuation would exceed the authorized scope: " + cont.Reason
	}
	b := &autonomy.HumanBoundary{
		Reason:  reason,
		Targets: append([]string(nil), d.req.Targets...),
	}
	autonomy.DeriveBoundaryAction(b)
	d.loop.AwaitHuman(*b)
	d.enrichBoundary()
	d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context
	return true
}

func (d *Driver) contextObservation() autonomy.Observation {
	maxOut := d.obs.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = d.resolved.Profile.MaxOutputTokens
	}
	interaction := d.req.InteractionContract
	if interaction == "" && d.req.Contract != nil {
		interaction = d.req.Contract.Contract
		if interaction == "" {
			interaction = d.req.Contract.Kind
		}
	}
	return autonomy.Observation{
		Intent:              autonomy.ParseIntent(d.prompt),
		Target:              firstTarget(d.req.Targets),
		Evidence:            d.req.Evidence,
		MaxOutputTokens:     maxOut,
		InteractionContract: interaction,
		Contract:            cloneContract(d.req.Contract),
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

// emitObjectiveUnsubstantiated publishes the objective-authority verdict as
// INFRASTRUCTURE telemetry. It deliberately carries no user-facing evidence, so
// the TUI subscriber boundary routes it to the Trace Overlay (Alt+T) and never
// into the Main Narrative. The runtime source is unchanged; only the projection
// filters.
//
// The line names the run, the contract kind, the contract identity, the target
// and the exact clause that went unmet: a refusal a human cannot reconstruct
// from the trace overlay is a refusal they have to take on faith.
func (d *Driver) emitObjectiveUnsubstantiated(contract execution.TaskContract, evaluation execution.ObjectiveEvaluation) {
	if d == nil || d.bus == nil {
		return
	}
	runID, contractID, target, _ := d.driverFacts()
	d.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[objective] run=%s contract_id=%s kind=%s target=%s outcome=%s clause=%s — %s",
		orUnknownField(runID), orUnknownField(contractID), contract.Kind, orUnknownField(target),
		evaluation.Outcome, orClause(evaluation.Clause), evaluation.Reason)))
}

func orUnknownField(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func orClause(clause string) string {
	if strings.TrimSpace(clause) == "" {
		return "unspecified"
	}
	return clause
}

func (d *Driver) term() *autonomy.LoopTermination {
	if d.loop == nil {
		return nil
	}
	term := d.loop.Termination()
	// Every return path funnels through here, so this is the single point at
	// which "how did the run end" is written to the journal — exactly once.
	d.ledgerTerminal(term)
	return term
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
