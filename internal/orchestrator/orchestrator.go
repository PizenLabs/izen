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
	"fmt"
	"sync"

	"github.com/PizenLabs/izen/internal/core/classifier"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
)

// Phase is a logical execution phase within the workflow.
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseAsk
	PhaseInvestigate
	PhasePlan
	PhaseBuild
	PhaseReview
)

func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseAsk:
		return "ask"
	case PhaseInvestigate:
		return "investigate"
	case PhasePlan:
		return "plan"
	case PhaseBuild:
		return "build"
	case PhaseReview:
		return "review"
	default:
		return fmt.Sprintf("Phase(%d)", int(p))
	}
}

// Valid reports whether the phase is a known logical execution phase.
func (p Phase) Valid() bool { return p >= PhaseIdle && p <= PhaseReview }

// workflowStateFor maps a logical phase onto its matching workflow SM state.
func (p Phase) workflowState() workflow.WorkflowState {
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

// validEdge reports whether a logical transition from `from` to `to` is
// permitted by the orchestrator's phase table.
func validEdge(from, to Phase) bool {
	if to == from {
		return true
	}
	switch from {
	case PhaseIdle:
		return to == PhaseAsk || to == PhaseInvestigate || to == PhasePlan
	case PhaseAsk:
		return to == PhaseInvestigate || to == PhasePlan
	case PhaseInvestigate:
		return to == PhasePlan || to == PhaseAsk
	case PhasePlan:
		return to == PhaseBuild || to == PhaseAsk || to == PhaseInvestigate
	case PhaseBuild:
		return to == PhaseReview || to == PhaseAsk
	case PhaseReview:
		return to == PhaseBuild || to == PhaseAsk
	default:
		return false
	}
}

// Orchestrator drives the workflow state machine across execution phases while
// keeping a single persistent RuntimeContext. It is safe for concurrent use.
type Orchestrator struct {
	mu       sync.RWMutex
	sm       *workflow.WorkflowStateMachine
	rt       *runtime.RuntimeContext
	current  Phase
	bus      *events.Bus
	history  []Phase
	pipeline *pipeline.Engine

	// planAuthorization carries the explicitly authorized execution plan the
	// workflow guard consults (see plan_authorization.go): a human-approved
	// DECOMPOSITION_PROPOSAL micro-plan or a fast-path ephemeral plan.
	planAuthorized bool
	microPlan      *MicroPlan
	ephemeral      *EphemeralPlan
}

// New creates an Orchestrator bound to the shared WorkflowStateMachine and
// RuntimeContext. The RuntimeContext is persistent for the lifetime of the
// orchestrator; every phase transition shares the same instance.
func New(sm *workflow.WorkflowStateMachine, rt *runtime.RuntimeContext) *Orchestrator {
	return &Orchestrator{
		sm:      sm,
		rt:      rt,
		current: PhaseIdle,
		history: []Phase{PhaseIdle},
	}
}

// WithEventBus wires the event bus so PhaseChanged transitions are published.
// Nil disables emission. Returns the orchestrator for chaining.
func (o *Orchestrator) WithEventBus(bus *events.Bus) *Orchestrator {
	if o != nil {
		o.bus = bus
	}
	return o
}

// WithPipeline wires the layered Pipeline Engine (Layers 0-5) onto the
// orchestrator. The pipeline supplies knowledge resolution, capability
// detection, governed context, intent-based model routing and validation for
// the execution phases. Nil detaches the pipeline. Returns the orchestrator
// for chaining.
func (o *Orchestrator) WithPipeline(pe *pipeline.Engine) *Orchestrator {
	if o != nil {
		o.pipeline = pe
	}
	return o
}

// Pipeline returns the wired layered Pipeline Engine, if any.
func (o *Orchestrator) Pipeline() *pipeline.Engine {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.pipeline
}

// RuntimeContext returns the shared, persistent runtime context. The returned
// pointer is stable across all phase transitions.
func (o *Orchestrator) RuntimeContext() *runtime.RuntimeContext {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.rt
}

// Current returns the current execution phase.
func (o *Orchestrator) Current() Phase {
	if o == nil {
		return PhaseIdle
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.current
}

// History returns the ordered phase-transition history, oldest first.
func (o *Orchestrator) History() []Phase {
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
func (o *Orchestrator) CurrentWorkflowState() workflow.WorkflowState {
	if o == nil || o.sm == nil {
		return workflow.StateIdle
	}
	return o.sm.State()
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
func (o *Orchestrator) Transition(next Phase, tctx workflow.TransitionContext) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	if !next.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", next)
	}

	o.mu.Lock()
	defer o.mu.Unlock()

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

	if o.sm != nil {
		if err := driveSM(o.sm, next, tctx); err != nil {
			return err
		}
	}

	from := o.current
	o.current = next
	o.history = append(o.history, next)

	if o.bus != nil {
		o.bus.Publish(events.NewPhaseChanged(from.String(), next.String()))
	}
	return nil
}

// Force advances the workflow to the given logical phase even when the phase
// graph forbids a direct logical hop (e.g. Review -> Plan). The underlying SM
// is reset to idle and driven forward along the canonical path. It preserves
// the shared RuntimeContext and emits a PhaseChanged event. It is the UI's
// explicit user-mode-switch entry: user intent always wins over the phase
// graph, unlike Transition which enforces validEdge.
func (o *Orchestrator) Force(next Phase, tctx workflow.TransitionContext) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	if !next.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", next)
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if next == o.current {
		return nil
	}

	if o.sm != nil {
		// Reset to idle first so any phase hop becomes reachable, then drive
		// forward along the canonical path. Guards are still evaluated through
		// the provided context (e.g. EventBuild requires HasPlan).
		if o.sm.State() != workflow.StateIdle {
			if err := o.sm.SendEvent(workflow.EventReset, tctx); err != nil {
				return err
			}
		}
		tctx = o.authorizeContextLocked(tctx)
		if err := driveSM(o.sm, next, tctx); err != nil {
			return err
		}
	}

	from := o.current
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
func (o *Orchestrator) Fail(class classifier.FailureClass) error {
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
	want := target.workflowState()

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
	}
	return from, false
}

// TransitionError reports an invalid logical phase transition.
type TransitionError struct {
	From Phase
	To   Phase
	Msg  string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("orchestrator: invalid transition %s -> %s: %s", e.From, e.To, e.Msg)
}
