package orchestrator

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
)

func testRuntime(t *testing.T) *runtime.RuntimeContext {
	t.Helper()
	return runtime.New(
		artifact.NewStore(filepath.Join(t.TempDir(), ".izen")),
		capability.NewCapabilitySet(),
		budget.NewBudget(10, 1000, 100000, 3, 30*time.Second, 10),
	)
}

func TestValidTransitions(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)

	transitions := []struct {
		to  Phase
		ctx workflow.TransitionContext
	}{
		{PhaseInvestigate, workflow.TransitionContext{}},
		{PhasePlan, workflow.TransitionContext{}},
		{PhaseBuild, workflow.TransitionContext{HasPlan: true, HasCapabilities: true}},
		{PhaseReview, workflow.TransitionContext{}},
		{PhaseAsk, workflow.TransitionContext{}},
	}

	for _, tr := range transitions {
		if err := o.Transition(tr.to, tr.ctx); err != nil {
			t.Fatalf("Transition(%s): %v", tr.to, err)
		}
		if o.Current() != tr.to {
			t.Errorf("Current() = %s, want %s", o.Current(), tr.to)
		}
	}
}

func TestRuntimeContextPersistsAcrossTransitions(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)

	for _, ph := range []Phase{PhaseInvestigate, PhasePlan, PhaseBuild, PhaseReview, PhaseAsk} {
		ctx := workflow.TransitionContext{}
		if ph == PhaseBuild {
			ctx = workflow.TransitionContext{HasPlan: true, HasCapabilities: true}
		}
		if err := o.Transition(ph, ctx); err != nil {
			t.Fatalf("Transition(%s): %v", ph, err)
		}
		if got := o.RuntimeContext(); got != rt {
			t.Fatalf("RuntimeContext() = %p, want shared instance %p across %s", got, rt, ph)
		}
	}
}

func TestInvalidTransitionRejected(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)

	// Idle -> Review is not a valid logical phase hop.
	if err := o.Transition(PhaseReview, workflow.TransitionContext{}); err == nil {
		t.Fatal("expected error for Idle -> Review, got nil")
	} else {
		var te *TransitionError
		if !errors.As(err, &te) {
			t.Fatalf("error type = %T, want *TransitionError", err)
		}
	}
	if o.Current() != PhaseIdle {
		t.Errorf("Current() = %s, want idle after rejected transition", o.Current())
	}
}

func TestNoopTransitionSamePhase(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)

	if err := o.Transition(PhaseIdle, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(PhaseIdle): %v", err)
	}
	if len(o.History()) != 1 {
		t.Errorf("History() length = %d, want 1 (no new entry on no-op)", len(o.History()))
	}
}

func TestGuardErrorSurfaces(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)

	if err := o.Transition(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Plan): %v", err)
	}
	// Building without a plan or capabilities must be rejected by the SM.
	if err := o.Transition(PhaseBuild, workflow.TransitionContext{}); err == nil {
		t.Fatal("expected guard error building without plan, got nil")
	}
	if o.Current() != PhasePlan {
		t.Errorf("Current() = %s, want plan after failed build", o.Current())
	}
}

func TestPhaseChangedEventsEmitted(t *testing.T) {
	rt := testRuntime(t)
	bus := events.NewBus(events.DefaultBufferSize)
	o := New(workflow.NewWorkflowStateMachine(), rt).WithEventBus(bus)

	var mu sync.Mutex
	var seen []events.DomainEvent
	sub := bus.Subscribe(events.EventPhaseChanged, func(ev events.DomainEvent) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	})
	defer sub.Cancel()

	if err := o.Transition(PhaseInvestigate, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Investigate): %v", err)
	}
	if err := o.Transition(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Plan): %v", err)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 2
	})

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("PhaseChanged events = %d, want 2", len(seen))
	}
	p0, ok := seen[0].Payload().(events.PhaseChangedPayload)
	if !ok || p0.From != "idle" || p0.To != "investigate" {
		t.Errorf("event[0] payload = %#v, want idle -> investigate", seen[0].Payload())
	}
	p1, ok := seen[1].Payload().(events.PhaseChangedPayload)
	if !ok || p1.From != "investigate" || p1.To != "plan" {
		t.Errorf("event[1] payload = %#v, want investigate -> plan", seen[1].Payload())
	}
}

func TestNoBusDoesNotPanic(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)
	if err := o.Transition(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Plan) without bus: %v", err)
	}
}

func TestCurrentWorkflowStateSynced(t *testing.T) {
	rt := testRuntime(t)
	sm := workflow.NewWorkflowStateMachine()
	o := New(sm, rt)

	if o.CurrentWorkflowState() != workflow.StateIdle {
		t.Fatalf("initial workflow state = %s, want idle", o.CurrentWorkflowState())
	}
	if err := o.Transition(PhaseInvestigate, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Investigate): %v", err)
	}
	if o.CurrentWorkflowState() != workflow.StateInvestigating {
		t.Errorf("workflow state = %s, want investigating", o.CurrentWorkflowState())
	}
}

func TestForceBypassesInvalidEdge(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)

	// Review -> Plan is not a valid logical edge, but Force must reach it by
	// resetting the SM to idle and driving forward along the canonical path.
	if err := o.Transition(PhaseReview, workflow.TransitionContext{}); err == nil {
		t.Fatal("expected error for Idle -> Review, got nil")
	}
	// Enter a legitimate path to Review: Plan -> Build -> Review.
	for _, ph := range []Phase{PhasePlan, PhaseBuild, PhaseReview} {
		ctx := workflow.TransitionContext{}
		if ph == PhaseBuild {
			ctx = workflow.TransitionContext{HasPlan: true, HasCapabilities: true}
		}
		if err := o.Transition(ph, ctx); err != nil {
			t.Fatalf("Transition(%s): %v", ph, err)
		}
	}

	if err := o.Force(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Force(Plan): %v", err)
	}
	if o.Current() != PhasePlan {
		t.Errorf("Current() = %s, want plan after Force", o.Current())
	}
	if o.CurrentWorkflowState() != workflow.StatePlanning {
		t.Errorf("workflow state = %s, want planning after Force", o.CurrentWorkflowState())
	}
}

func TestForceNoopSamePhase(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)
	if err := o.Force(PhaseIdle, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Force(Idle): %v", err)
	}
	if len(o.History()) != 1 {
		t.Errorf("History() length = %d, want 1 (no new entry on no-op)", len(o.History()))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
