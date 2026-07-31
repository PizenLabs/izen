package build

import (
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// collect subscribes to an event type, runs trigger, and waits for the
// published events. Subscription happens before trigger so no event is missed.
func collect(t *testing.T, bus *events.Bus, eventType string, want int, trigger func()) []events.DomainEvent {
	t.Helper()
	var mu sync.Mutex
	var got []events.DomainEvent
	bus.Subscribe(eventType, func(ev events.DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	})

	trigger()

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= want {
			mu.Lock()
			defer mu.Unlock()
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d %q events, got %d", want, eventType, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestEnginePublishesExecutionFailed(t *testing.T) {
	bus := events.NewBus(16)
	defer bus.Close()

	e := NewEngine().WithEventBus(bus)
	evs := collect(t, bus, events.EventExecutionFailed, 1, func() {
		e.RecordCompilationFailure("broken.go")
	})
	p, ok := evs[0].Payload().(events.ExecutionFailedPayload)
	if !ok {
		t.Fatalf("payload = %T, want ExecutionFailedPayload", evs[0].Payload())
	}
	if p.Classification != events.FailureRecoverable {
		t.Errorf("classification = %q, want %q", p.Classification, events.FailureRecoverable)
	}
	if p.Stage != "build.compilation" {
		t.Errorf("stage = %q, want %q", p.Stage, "build.compilation")
	}
}

func TestEnginePublishesStageCompleted(t *testing.T) {
	bus := events.NewBus(16)
	defer bus.Close()

	e := NewEngine().WithEventBus(bus)
	evs := collect(t, bus, events.EventStageCompleted, 1, func() {
		e.RecordCompilationSuccess()
	})
	p, ok := evs[0].Payload().(events.StageCompletedPayload)
	if !ok {
		t.Fatalf("payload = %T, want StageCompletedPayload", evs[0].Payload())
	}
	if p.Stage != "build.compilation" {
		t.Errorf("stage = %q, want %q", p.Stage, "build.compilation")
	}
}

func TestEnginePublishesPatchAttempted(t *testing.T) {
	bus := events.NewBus(16)
	defer bus.Close()

	e := NewEngine().WithEventBus(bus)
	evs := collect(t, bus, events.EventPatchAttempted, 1, func() {
		e.QueueProposal(Proposal{File: "x.go", Strategy: "ATOMIC_REPLACE", TaskID: 1})
	})
	p, ok := evs[0].Payload().(events.PatchAttemptedPayload)
	if !ok {
		t.Fatalf("payload = %T, want PatchAttemptedPayload", evs[0].Payload())
	}
	if p.File != "x.go" || p.Strategy != "ATOMIC_REPLACE" || p.Attempt != 1 {
		t.Errorf("payload = %+v", p)
	}
}

func TestEngineNoEventsWithoutBus(t *testing.T) {
	e := NewEngine()
	// Must not panic when no bus is wired.
	e.RecordCompilationFailure("a.go")
	e.RecordCompilationSuccess()
	e.QueueProposal(Proposal{File: "x.go"})
	if err := e.ExecuteFileMutation(t.Context(), "x.go", "content"); err == nil {
		t.Fatal("expected ErrHumanValidationRequired")
	}
}
