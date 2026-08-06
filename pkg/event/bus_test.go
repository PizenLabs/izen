package event

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until it returns true or the deadline elapses, then calls
// onTimeout to fail the test.
func waitFor(t *testing.T, cond func() bool, onTimeout func()) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	onTimeout()
}

func TestSubscribeUnsubscribe(t *testing.T) {
	bus := NewMemoryEventBus(16)

	var received atomic.Int32
	unsub := bus.Subscribe([]EventType{TypeTaskStarted}, func(Event) { received.Add(1) })
	if unsub == nil {
		t.Fatal("expected non-nil unsubscribe")
	}

	bus.Publish(NewEvent(TypeTaskStarted, "a", nil))
	bus.Publish(NewEvent(TypeTaskStarted, "b", nil))

	waitFor(t, func() bool { return received.Load() == 2 }, func() {
		t.Fatalf("expected 2 events, got %d", received.Load())
	})

	unsub()
	unsub() // idempotent

	bus.Publish(NewEvent(TypeTaskStarted, "c", nil))
	time.Sleep(50 * time.Millisecond)
	if got := received.Load(); got != 2 {
		t.Fatalf("expected no delivery after unsubscribe, got %d", got)
	}
}

func TestSubscribeFiltersByType(t *testing.T) {
	bus := NewMemoryEventBus(16)

	var started, completed atomic.Int32
	unsubStarted := bus.Subscribe([]EventType{TypeTaskStarted}, func(Event) { started.Add(1) })
	unsubCompleted := bus.Subscribe([]EventType{TypeTaskCompleted}, func(Event) { completed.Add(1) })
	defer unsubStarted()
	defer unsubCompleted()

	bus.Publish(NewEvent(TypeTaskStarted, "a", nil))
	bus.Publish(NewEvent(TypeTaskCompleted, "a", nil))
	bus.Publish(NewEvent(TypeBudgetExceeded, "a", nil))

	waitFor(t, func() bool {
		return started.Load() == 1 && completed.Load() == 1
	}, func() {
		t.Fatalf("started=%d completed=%d, want 1 and 1", started.Load(), completed.Load())
	})
}

func TestSubscribeAll(t *testing.T) {
	bus := NewMemoryEventBus(16)

	var count atomic.Int32
	unsub := bus.Subscribe(nil, func(Event) { count.Add(1) })
	defer unsub()

	bus.Publish(NewEvent(TypeTaskStarted, "a", nil))
	bus.Publish(NewEvent(TypeStateCheckpt, "a", nil))
	bus.Publish(NewEvent(TypeTaskCompleted, "a", nil))

	waitFor(t, func() bool { return count.Load() == 3 }, func() {
		t.Fatalf("expected 3 events, got %d", count.Load())
	})
}

func TestSubscriptionOrderPreserved(t *testing.T) {
	bus := NewMemoryEventBus(64)

	const pairs = 10
	var mu sync.Mutex
	order := make([]Event, 0, pairs*2)
	unsub := bus.Subscribe([]EventType{TypeTaskStarted, TypeTaskCompleted}, func(e Event) {
		mu.Lock()
		order = append(order, e)
		mu.Unlock()
	})
	defer unsub()

	for i := 0; i < pairs; i++ {
		bus.Publish(NewEvent(TypeTaskStarted, "t", nil))
		bus.Publish(NewEvent(TypeTaskCompleted, "t", nil))
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == pairs*2
	}, func() {
		t.Fatal("expected all events to be delivered")
	})

	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < pairs; i++ {
		if order[i*2].Type != TypeTaskStarted || order[i*2+1].Type != TypeTaskCompleted {
			t.Fatalf("order violated at pair %d: %s then %s", i, order[i*2].Type, order[i*2+1].Type)
		}
	}
}

func TestConcurrentPublish(t *testing.T) {
	const publishers = 8
	const perPublisher = 512

	bus := NewMemoryEventBus(publishers * perPublisher)

	var received atomic.Int32
	unsub := bus.Subscribe([]EventType{TypeTaskStarted}, func(Event) { received.Add(1) })
	defer unsub()

	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perPublisher; j++ {
				bus.Publish(NewEvent(TypeTaskStarted, "t", nil))
			}
		}()
	}
	wg.Wait()

	want := int32(publishers * perPublisher)
	waitFor(t, func() bool { return received.Load() == want }, func() {
		t.Fatalf("expected %d events, got %d", want, received.Load())
	})
}

func TestPublishAfterCloseIsNoop(t *testing.T) {
	bus := NewMemoryEventBus(16)

	var received atomic.Int32
	bus.Subscribe([]EventType{TypeTaskStarted}, func(Event) { received.Add(1) })

	bus.Close()
	bus.Publish(NewEvent(TypeTaskStarted, "a", nil))
	time.Sleep(50 * time.Millisecond)
	if got := received.Load(); got != 0 {
		t.Fatalf("expected no delivery after Close, got %d", got)
	}

	if unsub := bus.Subscribe([]EventType{TypeTaskStarted}, func(Event) {}); unsub == nil {
		t.Fatal("expected a no-op unsubscribe from a closed bus")
	}
}
