package plan

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// TestProcessFromLedgerPublishesEvents exercises the headless event emission of
// the plan engine through the deterministic direct-mutation fast-track: the
// provider must never be called, yet the engine still publishes the full
// CommandReceived → IntentParsed → PlanStaged → StageCompleted lifecycle.
func TestProcessFromLedgerPublishesEvents(t *testing.T) {
	bus := events.NewBus(32)
	defer bus.Close()

	providerCalled := false
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		providerCalled = true
		return nil, errors.New("provider must not be called for direct mutation fast-track")
	})
	e.WithEventBus(bus)

	var mu sync.Mutex
	var got []events.DomainEvent
	subscribeAll := func() {
		for _, typ := range []string{
			events.EventCommandReceived,
			events.EventIntentParsed,
			events.EventPlanStaged,
			events.EventStageCompleted,
			events.EventExecutionFailed,
		} {
			bus.Subscribe(typ, func(ev events.DomainEvent) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, ev)
			})
		}
	}
	subscribeAll()

	tasks, err := e.ProcessFromLedger(context.Background(), "", "refactor LICENSE from MIT to APACHE", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if providerCalled {
		t.Fatal("provider was called on direct-mutation fast-track path")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: got %d events, want >= 4", n)
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	types := make(map[string]int, len(got))
	for _, ev := range got {
		types[ev.Type()]++
	}
	if types[events.EventCommandReceived] != 1 {
		t.Errorf("CommandReceived emitted %d times, want 1", types[events.EventCommandReceived])
	}
	if types[events.EventIntentParsed] != 1 {
		t.Errorf("IntentParsed emitted %d times, want 1", types[events.EventIntentParsed])
	}
	if types[events.EventPlanStaged] != 1 {
		t.Errorf("PlanStaged emitted %d times, want 1", types[events.EventPlanStaged])
	}
	if types[events.EventStageCompleted] != 1 {
		t.Errorf("StageCompleted emitted %d times, want 1", types[events.EventStageCompleted])
	}
	if types[events.EventExecutionFailed] != 0 {
		t.Errorf("ExecutionFailed emitted %d times, want 0", types[events.EventExecutionFailed])
	}

	for _, ev := range got {
		switch ev.Type() {
		case events.EventIntentParsed:
			if intent, ok := ev.Payload().(events.IntentParsedPayload); !ok || intent.Intent != "direct_mutation" {
				t.Errorf("IntentParsed payload = %#v, want intent %q", ev.Payload(), "direct_mutation")
			}
		case events.EventPlanStaged:
			if staged, ok := ev.Payload().(events.PlanStagedPayload); !ok || staged.TaskCount != 1 {
				t.Errorf("PlanStaged payload = %#v, want 1 task", ev.Payload())
			}
		}
	}
}

// TestProcessFromLedgerNoBus is the backward-compatibility guard: with no bus
// wired the engine still works and never panics.
func TestProcessFromLedgerNoBus(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return nil, errors.New("must not be called")
	})
	tasks, err := e.ProcessFromLedger(context.Background(), "", "refactor LICENSE from MIT to APACHE", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Target != "LICENSE" {
		t.Fatalf("unexpected tasks: %+v", tasks)
	}
}
