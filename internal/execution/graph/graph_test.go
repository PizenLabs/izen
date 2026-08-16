package graph

import (
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// eventCollector captures events in arrival order through a single subscription.
type eventCollector struct {
	mu     sync.Mutex
	events []events.DomainEvent
}

func subscribeAll(bus *events.Bus) *eventCollector {
	c := &eventCollector{}
	bus.SubscribeAll(func(ev events.DomainEvent) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.events = append(c.events, ev)
	})
	return c
}

func (c *eventCollector) types() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.events))
	for _, ev := range c.events {
		out = append(out, ev.Type())
	}
	return out
}

func (c *eventCollector) typesWhen(count int) []string {
	for i := 0; i < 200; i++ {
		c.mu.Lock()
		n := len(c.events)
		c.mu.Unlock()
		if n >= count {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c.types()
}

func busEmitter(bus *events.Bus) Emitter {
	return func(ev events.DomainEvent) { bus.Publish(ev) }
}

// TestCanonicalTopology pins the deterministic stage ordering.
func TestCanonicalTopology(t *testing.T) {
	g := New("r1", nil)
	want := []StageKind{
		StageUserIntent, StageStrategySelection, StageTargetResolution,
		StageContextCompilation, StageModelInvocation, StageArtifactValidation,
		StageApprovalGate, StageMutationTransaction, StageVerification, StageCompletion,
	}
	if len(g.Stages) != len(want) {
		t.Fatalf("stages = %d, want %d", len(g.Stages), len(want))
	}
	for i, w := range want {
		if g.Stages[i].Kind != w {
			t.Fatalf("stage[%d] = %s, want %s", i, g.Stages[i].Kind, w)
		}
		if g.Stages[i].State != StagePending {
			t.Fatalf("stage[%d] state = %s, want pending", i, g.Stages[i].State)
		}
	}
	// Sequential chain edges.
	if len(g.Edges) != len(want)-1 {
		t.Fatalf("edges = %d, want %d", len(g.Edges), len(want)-1)
	}
	// Runnable frontier: exactly the first stage.
	r := g.Runnable()
	if len(r) != 1 || r[0].Kind != StageUserIntent {
		t.Fatalf("runnable = %v, want [user_intent]", r)
	}
}

// TestSequentialExecution pins the chain driver: stages become runnable only
// after their dependency completes.
func TestSequentialExecution(t *testing.T) {
	g := New("r2", nil)
	if g.Ready(StageModelInvocation) {
		t.Fatal("model_invocation ready before context_compilation")
	}
	g.Complete(StageUserIntent, "")
	if r := g.Runnable(); len(r) != 1 || r[0].Kind != StageStrategySelection {
		t.Fatalf("runnable after user_intent = %v, want [strategy_selection]", r)
	}
	g.Complete(StageStrategySelection, "strategy")
	if r := g.Runnable(); len(r) != 1 || r[0].Kind != StageTargetResolution {
		t.Fatalf("runnable after strategy_selection = %v, want [target_resolution]", r)
	}
	g.Complete(StageTargetResolution, "index.html")
	if !g.Ready(StageContextCompilation) {
		t.Fatal("context_compilation not ready after target_resolution")
	}
	g.Complete(StageContextCompilation, "40 tokens")
	if !g.Ready(StageModelInvocation) {
		t.Fatal("model_invocation not ready after context_compilation")
	}
}

// TestEventsGeneratedFromTransitions pins the core contract: events are
// generated ONLY from graph transitions, and the terminal transition is always
// last.
func TestEventsGeneratedFromTransitions(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	c := subscribeAll(bus)
	g := New("r3", busEmitter(bus))

	g.CompleteExecution("completed")
	types := c.typesWhen(1)
	if len(types) != 1 || types[0] != events.EventExecutionFinished {
		t.Fatalf("events = %v, want exactly [execution.finished] from the terminal transition", types)
	}
	if !g.Terminal() || g.Phase != PhaseCompleted {
		t.Fatalf("phase = %s, want completed terminal", g.Phase)
	}
}

// TestFailExecutionEmitsFailedThenFinished pins the failure transition order:
// execution.failed must precede execution.finished, and finished is terminal.
func TestFailExecutionEmitsFailedThenFinished(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	c := subscribeAll(bus)
	g := New("r4", busEmitter(bus))

	g.FailExecution(events.FailureRecoverable, errBoom, "executor.model")
	types := c.typesWhen(2)
	if len(types) != 2 {
		t.Fatalf("events = %v, want 2", types)
	}
	if types[0] != events.EventExecutionFailed || types[1] != events.EventExecutionFinished {
		t.Fatalf("order = %v, want [execution.failed execution.finished]", types)
	}
	if g.Phase != PhaseFailed {
		t.Fatalf("phase = %s, want failed", g.Phase)
	}
	// A terminal graph never re-emits.
	g.FailExecution(events.FailureRecoverable, errBoom, "executor.model")
	g.CompleteExecution("x")
	if len(c.types()) != 2 {
		t.Fatalf("terminal graph re-emitted events: %v", c.types())
	}
}

// TestCancelExecutionPins the clean-cancellation transition.
func TestCancelExecution(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	c := subscribeAll(bus)
	g := New("r5", busEmitter(bus))

	g.CancelExecution("cancelled")
	types := c.typesWhen(1)
	if len(types) != 1 || types[0] != events.EventExecutionFinished {
		t.Fatalf("events = %v, want exactly [execution.finished]", types)
	}
	if g.Phase != PhaseCancelled {
		t.Fatalf("phase = %s, want cancelled", g.Phase)
	}
}

// TestEvidenceExcludesPending pins that only reached stages produce evidence —
// a pending stage is never fabricated into the proof.
func TestEvidenceExcludesPending(t *testing.T) {
	g := New("r6", nil)
	g.Complete(StageUserIntent, "")
	g.Complete(StageStrategySelection, "targeted_mutation")
	g.Complete(StageTargetResolution, "index.html")

	ev := g.Evidence()
	for _, e := range ev {
		if e.State == string(StagePending) {
			t.Fatalf("evidence contains a pending stage: %+v", e)
		}
	}
	if len(ev) != 3 {
		t.Fatalf("evidence length = %d, want 3 (only reached stages)", len(ev))
	}
	// Stage names map onto the compact proof vocabulary.
	if ev[1].Kind != "strategy_selected" {
		t.Fatalf("evidence[1].Kind = %s, want strategy_selected", ev[1].Kind)
	}
}

// TestAwaitApprovalResumePins the approval pause/resume lifecycle.
func TestAwaitApprovalResume(t *testing.T) {
	g := New("r7", nil)
	g.Wait(StageApprovalGate, "awaiting human")
	if g.Phase != PhaseAwaitingApproval {
		t.Fatalf("phase = %s, want awaiting_approval", g.Phase)
	}
	if g.Terminal() {
		t.Fatal("awaiting_approval must not be terminal (it is a pause)")
	}
	g.Resume()
	if g.Phase != PhaseRunning {
		t.Fatalf("phase after resume = %s, want running", g.Phase)
	}
}

var errBoom = &errType{msg: "boom"}

type errType struct{ msg string }

func (e *errType) Error() string { return e.msg }
