package investigate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// TestRunContextShortCircuitPublishesEvents covers the headless event emission
// of the investigate engine through the deterministic deadlock-guard
// short-circuit path: a code-mutation problem exits immediately (no retriever,
// no executor) but still publishes CommandReceived + IntentParsed. The
// short-circuit returns before StageCompleted, so that must NOT be emitted.
func TestRunContextShortCircuitPublishesEvents(t *testing.T) {
	bus := events.NewBus(32)
	defer bus.Close()

	eng := NewEngine(".", "refactor LICENSE from MIT to APACHE", nil, nil)
	eng.WithEventBus(bus)

	var mu sync.Mutex
	var got []events.DomainEvent
	for _, typ := range []string{
		events.EventCommandReceived,
		events.EventIntentParsed,
		events.EventStageCompleted,
		events.EventExecutionFailed,
	} {
		bus.Subscribe(typ, func(ev events.DomainEvent) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, ev)
		})
	}

	result, err := eng.RunContext(context.Background())
	if err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	if result.Resolved {
		t.Fatal("short-circuit result must not be resolved")
	}
	if result.Conclusion == "" {
		t.Fatal("short-circuit result must carry a handoff conclusion")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: got %d events, want >= 2", n)
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
	if types[events.EventExecutionFailed] != 0 {
		t.Errorf("ExecutionFailed emitted %d times, want 0", types[events.EventExecutionFailed])
	}
	for _, ev := range got {
		switch ev.Type() {
		case events.EventStageCompleted:
			p, ok := ev.Payload().(events.StageCompletedPayload)
			if !ok {
				t.Fatalf("StageCompleted payload = %T, want StageCompletedPayload", ev.Payload())
			}
			// The forensic() routing emits investigate.forensic telemetry on the
			// short-circuit path, but the lifecycle "investigate" stage must not
			// fire because RunContext returns before the terminal emit.
			if p.Stage == "investigate" {
				t.Errorf("lifecycle StageCompleted(%q) emitted, want none on short-circuit", p.Stage)
			}
		case events.EventIntentParsed:
			p, ok := ev.Payload().(events.IntentParsedPayload)
			if !ok || p.Intent != "code_mutation" {
				t.Errorf("IntentParsed payload = %#v, want intent %q", ev.Payload(), "code_mutation")
			}
		}
	}
}

// TestRunContextNoBus is the backward-compatibility guard: with no bus wired
// the engine still runs the short-circuit and never panics.
func TestRunContextNoBus(t *testing.T) {
	eng := NewEngine(".", "add a feature to the storefront", nil, nil)
	result, err := eng.RunContext(context.Background())
	if err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	if result.Conclusion == "" {
		t.Fatal("expected a handoff conclusion")
	}
}
