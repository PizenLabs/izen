// Package orchestrator owns the execution lifecycle: it maps the logical
// execution phases (Ask, Investigate, Plan, Build, Review) onto the single
// Workflow State Machine and shares one persistent RuntimeContext across all
// phase transitions.
//
// The RuntimeContext is created once at bootstrap and never replaced: state
// transitions are logical workflow changes, NOT agent re-initializations or
// process restarts. Accumulated workspace context, retrieved artifacts, and
// execution history survive every phase hop. Consumers observe phase changes
// asynchronously via PhaseChanged events on the event bus; they never drive
// the state machine directly.
package orchestrator

import (
	"context"
	"fmt"
	"sync"

	"github.com/PizenLabs/izen/internal/core/classifier"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	domainorch "github.com/PizenLabs/izen/internal/domain/orchestration"
	"github.com/PizenLabs/izen/internal/engine/pipeline"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ── STEP 1 transitional bridge ────────────────────────────────────────────
// Canonical phase state types live in internal/domain/orchestration. These
// aliases keep the legacy execution bridge (Orchestrator struct, SM driving,
// bus wiring) compiling while high-level callers migrate to the domain.
type Phase = domainorch.Phase
type Transition = domainorch.Transition
type TransitionError = domainorch.TransitionError

const (
	PhaseIdle        = domainorch.PhaseIdle
	PhaseAsk         = domainorch.PhaseAsk
	PhaseInvestigate = domainorch.PhaseInvestigate
	PhasePlan        = domainorch.PhasePlan
	PhaseBuild       = domainorch.PhaseBuild
	PhaseReview      = domainorch.PhaseReview
)

// validEdge is the execution-bridge wrapper over the canonical pure table in
// the domain (domainorch.ValidTransition).
func validEdge(from, to Phase) bool { return domainorch.ValidTransition(from, to) }

// workflowStateFor maps a logical phase onto its matching workflow SM state.
// It is the execution-bridge form of the former (p Phase).workflowState
// method, kept as a free function because methods cannot be defined on an
// aliased (domain-owned) type.
func workflowStateFor(p Phase) workflow.WorkflowState {
	switch p {
	case PhaseAsk, PhaseIdle:
		return workflow.StateIdle
	case PhaseInvestigate:
		return workflow.StateInvestigating
	case PhasePlan:
		return workflow.StatePlanning
	case PhaseBuild:
		return workflow.StateBuilding
	case PhaseReview:
		return workflow.StateReviewing
	default:
		return workflow.StateIdle
	}
}

// phaseForWorkflowState is the INVERSE of workflowStateFor. It lets the
// orchestrator RE-ANCHOR its own projection when the shared SM has moved —
// which is the repair for a split-brain where `current` and `sm.State()`
// disagreed and every subsequent Transition became a silent no-op.
func phaseForWorkflowState(s workflow.WorkflowState) (Phase, bool) {
	switch s {
	case workflow.StateIdle:
		return PhaseAsk, true
	case workflow.StateInvestigating:
		return PhaseInvestigate, true
	case workflow.StatePlanning:
		return PhasePlan, true
	case workflow.StateBuilding:
		return PhaseBuild, true
	case workflow.StateReviewing:
		return PhaseReview, true
	case workflow.StateRepairing, workflow.StateVerified, workflow.StateFailed:
		// Repairing is a failure-routing sub-position of Build; the two terminal
		// positions have no phase of their own and project onto Build, from
		// which the canonical exits remain reachable.
		return PhaseBuild, true
	default:
		return PhaseIdle, false
	}
}

// Orchestrator drives the workflow state machine across execution phases while
// keeping a single persistent RuntimeContext. It is safe for concurrent use.
//
// STEP 2 — thin pass-through adapter: when a durable TaskStore is wired (via
// WithTaskStore, or via WithRuntimeEngine which borrows the RuntimeEngine's
// bound store), every Transition/Force hop is delegated to
// internal/runtime/orchestrator.RecordPhaseTransition(_Forced) and persisted
// as PHASE_TRANSITION (+ WORKSPACE_SWITCHED) ledger lineage BEFORE the
// in-memory projection mutates. The ledger is the single source of truth;
// current/history are a read mirror of persisted hops, not independent
// state. Persistence failures are fail-closed: the hop is rejected and the
// projection is left untouched. With no store wired the legacy in-memory
// lifecycle runs unchanged.
type PhaseManager struct {
	mu       sync.RWMutex
	sm       *workflow.WorkflowStateMachine
	rt       *runtime.RuntimeContext
	current  Phase
	bus      *events.Bus
	history  []Phase
	pipeline *pipeline.Engine

	// ledger is the durable phase-transition sink borrowed from the
	// RuntimeEngine. Nil disables ledger persistence (legacy mode).
	ledger PhaseLedger
	// ledgerTaskID anchors phase lineage to one durable task. Empty with a
	// wired ledger disables persistence (fail-closed needs identity).
	ledgerTaskID string

	// runtimeLoop is the injected pkg/runtime/orchestrator.Loop closed
	// execution controller. It owns the Observe → RMAH → Gate → Commit path
	// with a single Observation-phase snapshot []byte that is passed to
	// verification without repeating os.ReadFile.
	runtimeLoop *Loop

	// planAuthorization carries the explicitly authorized execution plan the
	// workflow guard consults (see plan_authorization.go): a human-approved
	// DECOMPOSITION_PROPOSAL micro-plan, a fast-path ephemeral plan, or a
	// synthetic direct-mutation micro-plan ($hot / single-file rewrite).
	planAuthorized bool
	microPlan      *MicroPlan
	ephemeral      *EphemeralPlan
	synthetic      *SyntheticMicroPlan
}

// New creates an Orchestrator bound to the shared WorkflowStateMachine and
// RuntimeContext. The RuntimeContext is persistent for the lifetime of the
// orchestrator; every phase transition shares the same instance.
func New(sm *workflow.WorkflowStateMachine, rt *runtime.RuntimeContext) *PhaseManager {
	return &PhaseManager{
		sm:      sm,
		rt:      rt,
		current: PhaseIdle,
		history: []Phase{PhaseIdle},
	}
}

// WithEventBus wires the event bus so PhaseChanged transitions are published.
// Nil disables emission. Returns the orchestrator for chaining.
func (o *PhaseManager) WithEventBus(bus *events.Bus) *PhaseManager {
	if o != nil {
		o.bus = bus
	}
	return o
}

// RuntimeProvider is satisfied by *runtime.RuntimeEngine (via its Store
// method) without importing the master runtime package from the legacy
// bridge, keeping the dependency direction toward the leaf durable store.
type RuntimeProvider interface {
	Store() *durable.TaskStore
}

// WithTaskStore wires the durable phase-transition sink borrowed from the
// RuntimeEngine, anchored to taskID. The store stays owned by the
// RuntimeEngine; the adapter never replicates its state. An empty taskID
// disables persistence (lineage needs identity — fail-closed). Nil store
// detaches persistence. Returns the orchestrator for chaining.
func (o *PhaseManager) WithTaskStore(store *durable.TaskStore, taskID string) *PhaseManager {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if store == nil || taskID == "" {
		o.ledger = nil
		o.ledgerTaskID = ""
		return o
	}
	o.ledger = store
	o.ledgerTaskID = taskID
	return o
}

// WithRuntimeEngine wires the RuntimeEngine as the phase authority by
// borrowing its bound durable store, anchored to taskID. See WithTaskStore
// for ownership and fail-closed semantics. Returns the orchestrator for
// chaining.
func (o *PhaseManager) WithRuntimeEngine(rt RuntimeProvider, taskID string) *PhaseManager {
	if o == nil {
		return nil
	}
	if rt == nil {
		return o.WithTaskStore(nil, "")
	}
	return o.WithTaskStore(rt.Store(), taskID)
}

// WithPipeline wires the layered Pipeline Engine (Layers 0-5) onto the
// orchestrator. The pipeline supplies knowledge resolution, capability
// detection, governed context, intent-based model routing and validation for
// the execution phases. Nil detaches the pipeline. Returns the orchestrator
// for chaining.
func (o *PhaseManager) WithPipeline(pe *pipeline.Engine) *PhaseManager {
	if o != nil {
		o.pipeline = pe
	}
	return o
}

// WithRuntimeLoop injects the pkg/runtime/orchestrator.Loop closed execution
// controller into the primary orchestrator. The loop's Observe phase is the
// single disk-read authority; its snapshot []byte is passed to verification
// without repeating os.ReadFile.
func (o *PhaseManager) WithRuntimeLoop(loop *Loop) *PhaseManager {
	if o != nil {
		o.mu.Lock()
		o.runtimeLoop = loop
		o.mu.Unlock()
	}
	return o
}

// RuntimeLoop returns the injected runtime loop, if any.
func (o *PhaseManager) RuntimeLoop() *Loop {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.runtimeLoop
}

// Observe captures the target file's bytes into the runtime loop's memory
// snapshot (Observation phase). It is the ONLY disk read of the cycle — the
// returned snapshot []byte is the single source for RMAH extraction and gate
// verification.
func (o *PhaseManager) Observe(ctx context.Context, path string) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	o.mu.RLock()
	loop := o.runtimeLoop
	o.mu.RUnlock()
	if loop == nil {
		return fmt.Errorf("orchestrator: no runtime loop wired — call WithRuntimeLoop first")
	}
	return loop.Observe(ctx, path)
}

// Snapshot returns the current memory snapshot from the runtime loop, or nil
// before Observe. The snapshot.Content []byte is consumed by verification
// without repeating os.ReadFile.
func (o *PhaseManager) Snapshot() *MemorySnapshot {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	loop := o.runtimeLoop
	o.mu.RUnlock()
	if loop == nil {
		return nil
	}
	return loop.Snapshot()
}

// SnapshotContent returns the snapshot's raw bytes for verification. It is
// the state-machine's consumption point: verification receives this []byte
// directly, never re-reading the file from disk.
func (o *PhaseManager) SnapshotContent() []byte {
	snap := o.Snapshot()
	if snap == nil {
		return nil
	}
	return snap.Content
}

// ExecuteCycle runs one model-output cycle over the Observation-phase snapshot
// via the runtime loop. The snapshot []byte from Observe is passed through
// unchanged — no os.ReadFile occurs during extraction or verification.
func (o *PhaseManager) ExecuteCycle(ctx context.Context, rawModelOutput []byte) (*CycleOutcome, error) {
	if o == nil {
		return nil, fmt.Errorf("orchestrator: nil receiver")
	}
	o.mu.RLock()
	loop := o.runtimeLoop
	o.mu.RUnlock()
	if loop == nil {
		return nil, fmt.Errorf("orchestrator: no runtime loop wired")
	}
	return loop.ExecuteCycle(ctx, rawModelOutput)
}

// Pipeline returns the wired layered Pipeline Engine, if any.
func (o *PhaseManager) Pipeline() *pipeline.Engine {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.pipeline
}

// RuntimeContext returns the shared, persistent runtime context. The returned
// pointer is stable across all phase transitions.
func (o *PhaseManager) RuntimeContext() *runtime.RuntimeContext {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.rt
}

// Current returns the current execution phase.
func (o *PhaseManager) Current() Phase {
	if o == nil {
		return PhaseIdle
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.current
}

// History returns the ordered phase-transition history, oldest first.
func (o *PhaseManager) History() []Phase {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]Phase, len(o.history))
	copy(out, o.history)
	return out
}

// CurrentWorkflowState returns the underlying workflow state machine state.
// It is read-only instrumentation for the UI's lifecycle badge.
func (o *PhaseManager) CurrentWorkflowState() workflow.WorkflowState {
	if o == nil || o.sm == nil {
		return workflow.StateIdle
	}
	return o.sm.State()
}

// ParkedAtAuthorization reports whether a live execution run is blocked on a
// human authorization decision. The shared SM is the authority: the logical
// phase can still read Build while the run is parked inside it, and only the SM
// distinguishes "building" from "building, waiting for a human".
func (o *PhaseManager) ParkedAtAuthorization() bool {
	if o == nil || o.sm == nil {
		return false
	}
	return o.sm.Parked()
}

// ParkAtAuthorization parks a live execution run at its human authorization
// boundary. It is the orchestrator's OWN spelling of the transition: the shared
// SM moves to the non-terminal parked position and the logical phase is
// re-anchored so `current` can never claim Build while the SM says idle.
//
// A run that parks is NOT finished. Nothing here may be read as completion.
func (o *PhaseManager) ParkAtAuthorization(tctx workflow.TransitionContext) error {
	if o == nil || o.sm == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.sm.SendEvent(workflow.EventAwaitAuthorization, tctx); err != nil {
		return err
	}
	if !o.sm.Parked() {
		return fmt.Errorf("orchestrator: %s refused to park the run", o.sm.State())
	}
	o.reanchorLocked()
	return nil
}

// ResumeParkedRun resumes a run parked at a human authorization boundary into
// the executable position.
//
// This is the HUMAN-AUTHORIZATION EVENT of the execution lifecycle, and it is
// deliberately distinct from Transition/Force:
//
//   - it takes the lifecycle's own resume edge (EventBuild out of
//     awaiting_authorization), which a human decision is entitled to;
//   - it preserves the run, its candidate, its targets and its objective
//     verbatim — a resume is never a reset, a re-plan or a new run;
//   - it refuses anything but PhaseBuild, because the only question an
//     authorization answers is "may this mutation be applied?".
//
// Without it, resuming a parked run meant forcing a phase hop, which either
// refused with an illegal-edge error or — once the machine had been reset behind
// the projection's back — silently left the machine idle while the caller
// believed the resume had succeeded.
func (o *PhaseManager) ResumeParkedRun(tctx workflow.TransitionContext) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sm == nil {
		return fmt.Errorf("orchestrator: no workflow state machine bound")
	}
	if !o.sm.Parked() {
		return fmt.Errorf("orchestrator: no run is parked awaiting human authorization (state=%s)", o.sm.State())
	}
	tctx = o.authorizeContextLocked(tctx)
	if err := o.sm.SendEvent(workflow.EventBuild, tctx); err != nil {
		return fmt.Errorf("orchestrator: resume from the parked position refused: %w", err)
	}
	if !o.sm.State().Executable() {
		return fmt.Errorf("orchestrator: resume did not reach an executable position (now %s)", o.sm.State())
	}
	o.reanchorLocked()
	return nil
}

// ResetToAsk is the ONE sanctioned unwind of a live execution phase back to the
// interactive resting position.
//
// It replaces the direct `sm.SendEvent(EventReset)` calls that used to reset the
// shared state machine from behind the orchestrator's back. Those calls left
// `current` stale, so a later Transition(PhaseBuild) was a no-op while the SM
// sat at idle — and every mutation authorization issued afterwards was refused
// with "expected Building or Repairing ... got idle".
//
// The reset is durable lineage like any other hop: fail-closed, never
// half-applied.
func (o *PhaseManager) ResetToAsk(tctx workflow.TransitionContext) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sm != nil && o.sm.State() != workflow.StateIdle {
		if err := o.sm.SendEvent(workflow.EventReset, tctx); err != nil {
			return err
		}
	}
	from := o.current
	if from == PhaseAsk {
		return nil
	}
	if err := RecordPhaseTransitionForced(o.ledger, o.ledgerTaskID, from, PhaseAsk); err != nil {
		return err
	}
	o.current = PhaseAsk
	o.history = append(o.history, PhaseAsk)
	if o.bus != nil {
		o.bus.Publish(events.NewPhaseChanged(from.String(), PhaseAsk.String()))
	}
	return nil
}

// reanchorLocked recomputes the logical phase from the shared state machine so
// the projection can never drift away from the authority the rest of the runtime
// reads. It is deliberately unexported and lock-held: every caller is already
// inside a transition that just moved the SM.
func (o *PhaseManager) reanchorLocked() {
	if o.sm == nil {
		return
	}
	state := o.sm.State()
	if state == workflow.StateAwaitingAuthorization {
		// A parked run still belongs to the phase it parked inside; there is no
		// distinct phase for it and inventing one would fork the phase graph.
		return
	}
	phase, ok := phaseForWorkflowState(state)
	if !ok || phase == o.current {
		return
	}
	from := o.current
	o.current = phase
	o.history = append(o.history, phase)
	if o.bus != nil {
		o.bus.Publish(events.NewPhaseChanged(from.String(), phase.String()))
	}
}

// Transition advances the workflow to the given logical phase. It preserves
// the shared RuntimeContext and emits a PhaseChanged event when the phase
// actually changes. A transition to the current phase is a no-op.
//
// The underlying workflow SM is driven to the matching state. When the target
// state is not reachable in one event from the SM's current state (e.g.
// Review -> Build), the SM is reset to idle first. Guards enforced by the SM
// (EventBuild requires HasPlan and HasCapabilities) are evaluated through the
// provided TransitionContext; guard violations surface as errors.
func (o *PhaseManager) Transition(next Phase, tctx workflow.TransitionContext) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	if !next.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", next)
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	// ── DRIFT REPAIR BEFORE EDGE VALIDATION ─────────────────────────────
	// The shared state machine is the authority the AuthorizationEngine reads;
	// `current` is a projection of it. When the two disagree the projection is
	// stale, and validating an edge against a stale projection produces a refusal
	// that names a phase the runtime never held ("ask -> build: no valid
	// transition" while the machine actually sat at planning).
	//
	// Reconciling FIRST means the edge check judges the real position, and an
	// equal-phase request still repairs the machine instead of silently
	// succeeding on a lie — which is the exact shape of the reported defect:
	// "expected Building or Repairing ... got idle" immediately after a transition
	// that had reported success.
	if err := o.reconcileLocked(next, tctx); err != nil {
		return err
	}
	if next == o.current {
		return nil
	}
	if !validEdge(o.current, next) {
		return &TransitionError{From: o.current, To: next, Msg: "no valid transition"}
	}

	// The workflow guard evaluates HasPlan through the bound plan
	// authorization: an approved micro-plan or injected ephemeral plan
	// satisfies it even when the caller supplies no session-task evidence.
	tctx = o.authorizeContextLocked(tctx)

	// Atomicity scope: the machine may be left advanced only while the hop is still
	// being committed (durable lineage). A persistence failure rolls it back so
	// the machine and the projection never disagree; a SUCCESSFUL hop keeps the
	// machine where it was driven.
	restore, err := o.driveAtomicLocked(next, tctx)
	if err != nil {
		return err
	}

	// Adapter delegation: persist the authorized hop as durable lineage
	// BEFORE the in-memory projection mutates. Fail-closed: a persistence
	// failure rejects the hop and leaves current/history untouched.
	from := o.current
	if err := RecordPhaseTransition(o.ledger, o.ledgerTaskID, from, next); err != nil {
		if restore != nil {
			restore()
		}
		return err
	}
	o.current = next
	o.history = append(o.history, next)

	if o.bus != nil {
		o.bus.Publish(events.NewPhaseChanged(from.String(), next.String()))
	}
	return nil
}

// reconcileLocked repairs drift between the logical phase and the shared state
// machine. It is a no-op when they agree.
//
// It runs BEFORE edge validation, so the projection is re-anchored from the
// authority first: an edge check against a stale projection rejects a legal hop
// (or blesses an illegal one), and both outcomes are lies about where the runtime
// actually is.
//
// A PARKED run is never path-found here. Parking is a control-plane decision, not
// a routing hop: the machine is left parked and the projection re-anchored, and
// only the caller's explicit resume (driveAtomicLocked's parked branch) may leave
// it. That keeps "a human authorized this" from ever being inferred from a
// phase request.
func (o *PhaseManager) reconcileLocked(next Phase, tctx workflow.TransitionContext) error {
	if o.sm == nil {
		return nil
	}
	want := workflowStateFor(next)
	if o.sm.State() == want {
		return nil
	}
	tctx = o.authorizeContextLocked(tctx)
	// A parked run resumes through its own edge (driveAtomicLocked owns that
	// case). Here we only need to re-anchor a projection that drifted behind a
	// machine already sitting at the target.
	if o.sm.Parked() {
		o.reanchorLocked()
		return nil
	}
	if err := driveSM(o.sm, next, tctx); err != nil {
		return err
	}
	if o.sm.State() != want {
		return fmt.Errorf("orchestrator: phase %q could not be reconciled onto the workflow state machine (now %s)", next, o.sm.State())
	}
	o.reanchorLocked()
	return nil
}

// driveAtomicLocked drives the shared SM onto the phase's workflow state and
// returns a restore function that puts it back exactly as it was when the
// transition cannot be completed.
//
// Every SM mutation is therefore all-or-nothing. Driving the machine forward and
// returning on the first error — what this file used to do — left the SM in a
// half-advanced position (e.g. `planning` after a refused build guard) while
// `current` still read the OLD phase: the same split brain, reached from the
// other direction.
func (o *PhaseManager) driveAtomicLocked(next Phase, tctx workflow.TransitionContext) (restore func(), err error) {
	if o.sm == nil {
		return nil, nil
	}
	// From a parked position the only forward move is the resume edge, not a
	// driveSM path search: EventBuild out of awaiting_authorization is the
	// sanctioned resume and preserves the run's identity.
	if o.sm.Parked() {
		if next != PhaseBuild {
			return nil, fmt.Errorf("orchestrator: run is parked awaiting human authorization; %q requires the run to be resumed or aborted first", next)
		}
		tctx = o.authorizeContextLocked(tctx)
		if err := o.sm.SendEvent(workflow.EventBuild, tctx); err != nil {
			return nil, err
		}
		if o.sm.State() != workflowStateFor(next) {
			_ = o.sm.SendEvent(workflow.EventAwaitAuthorization, tctx)
			return nil, fmt.Errorf("orchestrator: resume from the parked position did not reach building (now %s)", o.sm.State())
		}
		o.reanchorLocked()
		return nil, nil
	}
	before := o.sm.State()
	if before == workflowStateFor(next) {
		return nil, nil
	}
	if err := driveSM(o.sm, next, tctx); err != nil {
		// Undo the partial advance so the SM and the projection stay in step.
		o.rewindLocked(before, tctx)
		return nil, err
	}
	if o.sm.State() != workflowStateFor(next) {
		o.rewindLocked(before, tctx)
		return nil, fmt.Errorf("orchestrator: phase %q did not reach the workflow state machine (now %s)", next, o.sm.State())
	}
	return func() { o.rewindLocked(before, tctx) }, nil
}

// rewindLocked puts the shared SM back on a previously observed state using the
// canonical reset-and-drive path, and re-anchors the projection from it.
func (o *PhaseManager) rewindLocked(want workflow.WorkflowState, tctx workflow.TransitionContext) {
	if o.sm == nil || o.sm.State() == want {
		o.reanchorLocked()
		return
	}
	if err := o.sm.SendEvent(workflow.EventReset, tctx); err != nil {
		return
	}
	phase, ok := phaseForWorkflowState(want)
	if !ok || phase == PhaseIdle {
		// A non-phase position (the parked state, or a terminal one) is reached
		// directly; there is no logical phase to drive through.
		o.reanchorLocked()
		return
	}
	_ = driveSM(o.sm, phase, o.authorizeContextLocked(tctx))
	o.reanchorLocked()
}

// Force advances the workflow to the given logical phase even when the phase
// graph forbids a direct logical hop (e.g. Review -> Plan). The underlying SM
// is reset to idle and driven forward along the canonical path. It preserves
// the shared RuntimeContext and emits a PhaseChanged event. It is the UI's
// explicit user-mode-switch entry: user intent always wins over the phase
// graph, unlike Transition which enforces validEdge.
func (o *PhaseManager) Force(next Phase, tctx workflow.TransitionContext) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	if !next.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", next)
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if next == o.current {
		// Same reconciliation contract as Transition: the shared state machine
		// is the authority, so an equal phase is a request to make it agree,
		// not a licence to skip the drive.
		return o.reconcileLocked(next, tctx)
	}

	// The SM is reset to idle first so any phase hop becomes reachable, then
	// driven forward along the canonical path. Guards are still evaluated
	// through the provided context (e.g. EventBuild requires HasPlan) — but
	// atomically: a refusal restores the machine instead of stranding it
	// half-advanced.
	//
	// A PARKED run is never reset here. The reset exists to make a phase hop
	// reachable; a parked run's only legitimate exit is the human's decision
	// (resume via the resume edge, abort via reset). Force is user-mode-switch
	// intent, and a mode switch must not silently discard a run that is waiting
	// on a human — the caller must resolve that boundary explicitly.
	if o.sm != nil && !o.sm.Parked() {
		if err := o.sm.SendEvent(workflow.EventReset, tctx); err != nil {
			return err
		}
	}
	tctx = o.authorizeContextLocked(tctx)
	if o.sm != nil {
		// Atomicity scope: the machine may be left advanced ONLY while the hop is
		// still being committed (durable lineage). A persistence failure must roll
		// it back so the machine and the projection never disagree — but a
		// SUCCESSFUL hop must keep the machine where it was driven.
		restore, err := o.driveAtomicLocked(next, tctx)
		if err != nil {
			return err
		}
		from := o.current
		// Adapter delegation: persist the forced hop as durable lineage BEFORE
		// the in-memory projection mutates (forced skips edge validation but
		// never skips lineage). Fail-closed on persistence failure.
		if err := RecordPhaseTransitionForced(o.ledger, o.ledgerTaskID, from, next); err != nil {
			if restore != nil {
				restore()
			}
			return err
		}
		o.current = next
		o.history = append(o.history, next)
		if o.bus != nil {
			o.bus.Publish(events.NewPhaseChanged(from.String(), next.String()))
		}
		return nil
	}

	// No shared machine: the projection is the whole truth.
	from := o.current
	if err := RecordPhaseTransitionForced(o.ledger, o.ledgerTaskID, from, next); err != nil {
		return err
	}
	o.current = next
	o.history = append(o.history, next)

	if o.bus != nil {
		o.bus.Publish(events.NewPhaseChanged(from.String(), next.String()))
	}
	return nil
}

// Fail reports a workflow failure through the shared state machine. Unlike
// Transition, it does not change the logical phase: it drives the SM to the
// failure-relevant sub-state (Failed / Repairing / Investigating / Planning)
// selected by the failure class. This keeps the UI decoupled from the raw SM.
func (o *PhaseManager) Fail(class classifier.FailureClass) error {
	if o == nil || o.sm == nil {
		return fmt.Errorf("orchestrator: nil receiver or state machine")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sm.SendEvent(workflow.EventFailureIdentified, workflow.TransitionContext{
		FailureClass: class,
	})
}

// driveSM moves the workflow state machine onto the state matching `target`.
// If the target is not reachable directly from the current state, the machine
// is reset to idle first, then driven forward along the canonical path.
func driveSM(sm *workflow.WorkflowStateMachine, target Phase, tctx workflow.TransitionContext) error {
	want := workflowStateFor(target)

	// Already there.
	if sm.State() == want {
		return nil
	}

	// Determine a minimal path to the target state from the current state.
	path, err := smPath(sm.State(), want)
	if err != nil {
		// Unreachable directly: reset to idle and retry from scratch.
		if err2 := sm.SendEvent(workflow.EventReset, tctx); err2 != nil {
			return fmt.Errorf("orchestrator: %w", err2)
		}
		path, err = smPath(sm.State(), want)
		if err != nil {
			return fmt.Errorf("orchestrator: cannot reach %s: %w", target, err)
		}
	}

	for _, ev := range path {
		if err := sm.SendEvent(ev, tctx); err != nil {
			return fmt.Errorf("orchestrator: %w", err)
		}
	}
	return nil
}

// smPath returns the shortest sequence of workflow events from `from` to
// `want`, or an error when no such path exists.
func smPath(from, want workflow.WorkflowState) ([]workflow.WorkflowEvent, error) {
	type step struct {
		state workflow.WorkflowState
		path  []workflow.WorkflowEvent
	}
	seen := map[workflow.WorkflowState]bool{from: true}
	queue := []step{{state: from}}
	// Edges for event-driven discovery. Guards are ignored here; the caller
	// drives with the real context and surfaces guard errors.
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.state == want {
			return cur.path, nil
		}
		// EventAwaitAuthorization is deliberately ABSENT. Parking at a human
		// boundary is a control-plane decision, never a routing hop: allowing it
		// here would let pathfinding reach Building THROUGH a park, silently
		// inventing a human authorization that never happened. The parked position
		// is reached only via ParkAtAuthorization, and left only via the resume
		// edge the caller drives explicitly.
		for _, ev := range []workflow.WorkflowEvent{
			workflow.EventReset,
			workflow.EventInvestigate,
			workflow.EventPlan,
			workflow.EventBuild,
			workflow.EventReview,
			workflow.EventVerificationPassed,
		} {
			nxt, ok := edge(cur.state, ev)
			if !ok || seen[nxt] {
				continue
			}
			seen[nxt] = true
			queue = append(queue, step{state: nxt, path: append(append([]workflow.WorkflowEvent{}, cur.path...), ev)})
		}
	}
	return nil, fmt.Errorf("no path from %s to %s", from, want)
}

// edge mirrors the workflow SM's unguarded transition lookup for pathfinding.
func edge(from workflow.WorkflowState, ev workflow.WorkflowEvent) (workflow.WorkflowState, bool) {
	// The park is reachable from every live position and from itself, so
	// pathfinding must see it: a resume out of the parked position must not be
	// routed through a reset.
	if ev == workflow.EventAwaitAuthorization {
		return workflow.StateAwaitingAuthorization, true
	}
	switch from {
	case workflow.StateIdle:
		switch ev {
		case workflow.EventInvestigate:
			return workflow.StateInvestigating, true
		case workflow.EventPlan:
			return workflow.StatePlanning, true
		case workflow.EventReset:
			return workflow.StateIdle, true
		}
	case workflow.StateInvestigating:
		switch ev {
		case workflow.EventPlan:
			return workflow.StatePlanning, true
		case workflow.EventReset:
			return workflow.StateIdle, true
		}
	case workflow.StatePlanning:
		switch ev {
		case workflow.EventBuild:
			return workflow.StateBuilding, true
		case workflow.EventReset:
			return workflow.StateIdle, true
		}
	case workflow.StateBuilding:
		switch ev {
		case workflow.EventReview:
			return workflow.StateReviewing, true
		case workflow.EventReset:
			return workflow.StateIdle, true
		}
	case workflow.StateReviewing:
		switch ev {
		case workflow.EventVerificationPassed:
			return workflow.StateVerified, true
		case workflow.EventReset:
			return workflow.StateIdle, true
		}
	case workflow.StateVerified:
		if ev == workflow.EventReset {
			return workflow.StateIdle, true
		}
	case workflow.StateFailed:
		if ev == workflow.EventReset {
			return workflow.StateIdle, true
		}
	case workflow.StateAwaitingAuthorization:
		// The resume edge: a human authorization returns the SAME run to the
		// executable position in one step. A pathfinding route that reset first
		// would silently lose the parked identity.
		switch ev {
		case workflow.EventBuild:
			return workflow.StateBuilding, true
		case workflow.EventReset:
			return workflow.StateIdle, true
		}
	}
	return from, false
}

// NOTE: TransitionError is aliased to domainorch.TransitionError at the top
// of this file (STEP 1 bridge). Its Error method lives in the domain.
