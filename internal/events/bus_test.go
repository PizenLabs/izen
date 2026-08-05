package events

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// collectHandler appends received events to a slice guarded by a mutex.
func collectHandler(t *testing.T, mu *sync.Mutex, got *[]DomainEvent) EventHandler {
	t.Helper()
	return func(ev DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		*got = append(*got, ev)
	}
}

func TestPublishDispatch(t *testing.T) {
	b := NewBus(16)
	defer b.Close()

	var mu sync.Mutex
	var got []DomainEvent

	sub := b.Subscribe(EventCommandReceived, collectHandler(t, &mu, &got))
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}

	ev := NewCommandReceived("/plan fix bug", "plan")
	b.Publish(ev)

	if !waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}) {
		t.Fatalf("handler did not receive the event, got %d", len(got))
	}

	mu.Lock()
	defer mu.Unlock()
	received := got[0]
	if received.Type() != EventCommandReceived {
		t.Errorf("Type() = %q, want %q", received.Type(), EventCommandReceived)
	}
	if received.Timestamp().IsZero() {
		t.Error("Timestamp() is zero")
	}
	p, ok := received.Payload().(CommandReceivedPayload)
	if !ok {
		t.Fatalf("Payload() = %T, want CommandReceivedPayload", received.Payload())
	}
	if p.Command != "/plan fix bug" || p.Mode != "plan" {
		t.Errorf("Payload = %+v, want command %q mode %q", p, "/plan fix bug", "plan")
	}
}

func TestEventTypeRouting(t *testing.T) {
	b := NewBus(16)
	defer b.Close()

	var mu sync.Mutex
	var got []DomainEvent
	b.Subscribe(EventPlanStaged, collectHandler(t, &mu, &got))

	// Publishing an unrelated type must not reach the PlanStaged subscriber.
	b.Publish(NewCommandReceived("/plan", "plan"))
	b.Publish(NewPatchApplied("x.go", 1, 0, time.Millisecond))
	time.Sleep(20 * time.Millisecond)

	if n := countEvents(&mu, &got); n != 0 {
		t.Fatalf("unrelated event delivered to subscriber, got %d events", n)
	}

	// The matching type must arrive.
	b.Publish(NewPlanStaged(2, []string{"a.go", "b.go"}, "plan"))
	if !waitFor(t, func() bool {
		return countEvents(&mu, &got) == 1
	}) {
		t.Fatal("matching event was not delivered")
	}
}

// countEvents returns the number of collected events under lock.
func countEvents(mu *sync.Mutex, got *[]DomainEvent) int {
	mu.Lock()
	defer mu.Unlock()
	return len(*got)
}

func TestSubscribeAllReceivesEveryType(t *testing.T) {
	b := NewBus(64)
	defer b.Close()

	var mu sync.Mutex
	var got []DomainEvent
	sub := b.SubscribeAll(func(ev DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	})
	defer sub.Cancel()

	// All-event subscribers must receive typed events AND envelopes.
	b.Publish(NewCommandReceived("/plan", "plan"))
	b.Publish(NewPlanStaged(1, []string{"a.go"}, "plan"))
	b.PublishEnvelope(NewEnvelope(DomainKindTelemetry, "telemetry", "x"))

	if !waitFor(t, func() bool {
		return countEvents(&mu, &got) == 3
	}) {
		t.Fatalf("SubscribeAll delivered %d events, want 3", countEvents(&mu, &got))
	}

	// A type-scoped subscriber must NOT receive the all-event stream's
	// non-matching types (existing routing is preserved).
	var scoped int32
	b.Subscribe(EventPlanStaged, func(DomainEvent) { atomic.AddInt32(&scoped, 1) })
	b.Publish(NewCommandReceived("/x", "plan"))
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&scoped) != 0 {
		t.Fatal("type-scoped subscriber received a non-matching type")
	}
}

func TestSubscribeAllCancelAndClosed(t *testing.T) {
	b := NewBus(16)
	got := make(chan DomainEvent, 16)
	sub := b.SubscribeAll(func(ev DomainEvent) { got <- ev })
	sub.Cancel()

	b.Publish(NewCommandReceived("/x", "plan"))
	select {
	case <-got:
		t.Fatal("cancelled SubscribeAll still delivered an event")
	default:
	}
	b.Close()
	if s := b.SubscribeAll(func(DomainEvent) {}); s != nil {
		t.Fatal("SubscribeAll on a closed bus must return nil")
	}
	if s := b.SubscribeAll(nil); s != nil {
		t.Fatal("SubscribeAll with a nil handler must return nil")
	}
}

func TestMultipleSubscribersSameType(t *testing.T) { // Buffer exceeds the burst so no events are dropped by the non-blocking
	// fast-path; every subscriber must see every event.
	b := NewBus(256)
	defer b.Close()

	const subs = 5
	var counters [subs]int32
	for i := 0; i < subs; i++ {
		b.Subscribe(EventStageCompleted, func(DomainEvent) {
			atomic.AddInt32(&counters[i], 1)
		})
	}

	for i := 0; i < 100; i++ {
		b.Publish(NewStageCompleted("test", time.Millisecond, ""))
	}

	if !waitFor(t, func() bool {
		for i := 0; i < subs; i++ {
			if atomic.LoadInt32(&counters[i]) != 100 {
				return false
			}
		}
		return true
	}) {
		for i := 0; i < subs; i++ {
			t.Errorf("subscriber %d received %d/100 events", i, atomic.LoadInt32(&counters[i]))
		}
	}
}

// TestNonBlockingPublishUnderLoad is the core headless guarantee: a slow
// consumer must never stall the publisher. When the consumer buffer is full,
// events are dropped and counted rather than blocking the publishing engine.
func TestNonBlockingPublishUnderLoad(t *testing.T) {
	const buffer = 8
	const total = 1000
	// Each event takes 10ms to consume, far slower than the publish burst.
	slow := func(DomainEvent) { time.Sleep(10 * time.Millisecond) }

	b := NewBus(buffer)
	sub := b.Subscribe(EventPatchApplied, slow)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}

	start := time.Now()
	for i := 0; i < total; i++ {
		b.Publish(NewPatchApplied("x.go", 1, 0, time.Millisecond))
	}
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("Publish blocked on slow consumer: %d publishes took %s", total, elapsed)
	}
	if dropped := sub.Dropped(); dropped == 0 {
		t.Error("expected dropped events under load, got 0 (buffer never saturated?)")
	}
	b.Close()
}

func TestSlowConsumerDoesNotBlockOtherSubscribers(t *testing.T) {
	// A buffer larger than the burst means the fast subscriber never drops an
	// event even though the slow consumer is stuck mid-handler.
	b := NewBus(256)
	defer b.Close()

	release := make(chan struct{})
	blocked := make(chan struct{})
	var blockedStarted sync.Once

	b.Subscribe(EventStageCompleted, func(DomainEvent) {
		blockedStarted.Do(func() { close(blocked) })
		<-release
	})

	var fastCount int32
	b.Subscribe(EventStageCompleted, func(DomainEvent) {
		atomic.AddInt32(&fastCount, 1)
	})

	// Saturate the slow consumer's buffer.
	for i := 0; i < 20; i++ {
		b.Publish(NewStageCompleted("test", 0, ""))
	}
	<-blocked

	// The fast subscriber must keep receiving even though the slow one is stuck.
	for i := 0; i < 20; i++ {
		b.Publish(NewStageCompleted("test", 0, ""))
	}
	if !waitFor(t, func() bool {
		return atomic.LoadInt32(&fastCount) >= 20
	}) {
		t.Errorf("fast subscriber starved by slow consumer, got %d events", atomic.LoadInt32(&fastCount))
	}
	close(release)
}

func TestSubscribeUnsubscribe(t *testing.T) {
	b := NewBus(16)
	defer b.Close()

	var count int32
	h := func(DomainEvent) { atomic.AddInt32(&count, 1) }
	b.Subscribe(EventPlanStaged, h)

	b.Publish(NewPlanStaged(1, nil, "plan"))
	if !waitFor(t, func() bool { return atomic.LoadInt32(&count) == 1 }) {
		t.Fatal("event not delivered before unsubscribe")
	}

	b.Unsubscribe(EventPlanStaged, h)
	b.Publish(NewPlanStaged(2, nil, "plan"))
	time.Sleep(30 * time.Millisecond)

	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("handler invoked %d times after unsubscribe, want 1", got)
	}
}

func TestSubscriptionCancel(t *testing.T) {
	b := NewBus(16)
	defer b.Close()

	var mu sync.Mutex
	var got []DomainEvent
	sub := b.Subscribe(EventStageCompleted, collectHandler(t, &mu, &got))
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}

	b.Publish(NewStageCompleted("a", 0, ""))
	if !waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}) {
		t.Fatal("event not delivered before cancel")
	}

	sub.Cancel()
	sub.Cancel() // idempotent

	b.Publish(NewStageCompleted("b", 0, ""))
	time.Sleep(30 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Errorf("handler invoked %d times after Cancel, want 1", len(got))
	}
}

func TestUnsubscribeDoesNotAffectOtherHandlers(t *testing.T) {
	b := NewBus(16)
	defer b.Close()

	var a, c int32
	hA := func(DomainEvent) { atomic.AddInt32(&a, 1) }
	hC := func(DomainEvent) { atomic.AddInt32(&c, 1) }
	b.Subscribe(EventIntentParsed, hA)
	b.Subscribe(EventIntentParsed, hC)

	b.Publish(NewIntentParsed("x", "", 0.5))
	if !waitFor(t, func() bool { return atomic.LoadInt32(&a) == 1 && atomic.LoadInt32(&c) == 1 }) {
		t.Fatal("initial delivery failed")
	}

	b.Unsubscribe(EventIntentParsed, hA)
	b.Publish(NewIntentParsed("y", "", 0.5))
	if !waitFor(t, func() bool { return atomic.LoadInt32(&c) == 2 }) {
		t.Fatal("remaining handler stopped receiving after unrelated unsubscribe")
	}
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&a); got != 1 {
		t.Errorf("unsubscribed handler invoked %d times, want 1", got)
	}
}

func TestClose(t *testing.T) {
	b := NewBus(16)

	var count int32
	b.Subscribe(EventPlanStaged, func(DomainEvent) { atomic.AddInt32(&count, 1) })

	b.Publish(NewPlanStaged(1, nil, "plan"))
	if !waitFor(t, func() bool { return atomic.LoadInt32(&count) == 1 }) {
		t.Fatal("event not delivered before close")
	}

	b.Close()
	b.Close() // idempotent

	// Publish after close is a silent no-op.
	b.Publish(NewPlanStaged(2, nil, "plan"))
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("handler invoked %d times after Close, want 1", got)
	}

	// Subscribe after close returns nil.
	if sub := b.Subscribe(EventPlanStaged, func(DomainEvent) {}); sub != nil {
		t.Error("Subscribe after Close returned a non-nil subscription")
	}
}

func TestSubscribeNilHandler(t *testing.T) {
	b := NewBus(16)
	defer b.Close()
	if sub := b.Subscribe(EventPlanStaged, nil); sub != nil {
		t.Error("Subscribe(nil handler) returned a non-nil subscription")
	}
}

func TestPublishNilEvent(t *testing.T) {
	b := NewBus(16)
	defer b.Close()

	var count int32
	b.Subscribe(EventPlanStaged, func(DomainEvent) { atomic.AddInt32(&count, 1) })

	b.Publish(nil)
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 0 {
		t.Errorf("nil event delivered %d times", got)
	}
}

func TestFailureClassificationPayloads(t *testing.T) {
	for _, tc := range []struct {
		class FailureClassification
		err   string
	}{
		{FailureTransient, "flaky test"},
		{FailureRecoverable, "compile error"},
		{FailurePermanent, "no provider configured"},
	} {
		ev := NewExecutionFailed(tc.class, nil, "build")
		p, ok := ev.Payload().(ExecutionFailedPayload)
		if !ok {
			t.Fatalf("payload = %T, want ExecutionFailedPayload", ev.Payload())
		}
		if p.Classification != tc.class {
			t.Errorf("Classification = %q, want %q", p.Classification, tc.class)
		}
		if p.Error != "" {
			t.Errorf("Error = %q, want empty for nil error", p.Error)
		}
		if p.Stage != "build" {
			t.Errorf("Stage = %q, want %q", p.Stage, "build")
		}
	}
}

func TestConcurrentSubscribePublishUnsubscribe(t *testing.T) {
	// Buffer exceeds the total burst so the surviving subscriptions are
	// guaranteed to receive every event despite concurrent publishing.
	b := NewBus(1024)
	defer b.Close()

	var delivered int32
	const publishers = 4
	const eventsPer = 200
	const surviving = 4

	// Subscribe all, then cancel half. Surviving subscriptions must each see
	// every published event — the delivery count is deterministic.
	subs := make([]*Subscription, 0, 8)
	for i := 0; i < 8; i++ {
		sub := b.Subscribe(EventStageCompleted, func(DomainEvent) {
			atomic.AddInt32(&delivered, 1)
		})
		if sub == nil {
			t.Fatal("Subscribe returned nil")
		}
		subs = append(subs, sub)
	}
	for _, s := range subs[:8-surviving] {
		s.Cancel()
	}
	// Give the cancelled dispatch loops time to stop.
	time.Sleep(10 * time.Millisecond)

	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < eventsPer; i++ {
				b.Publish(NewStageCompleted("concurrent", 0, ""))
			}
		}()
	}

	// Concurrently subscribe and unsubscribe ephemeral handlers while the
	// publishers are mid-flight. These are not counted in `delivered`.
	var ephemeral int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			e := b.Subscribe(EventStageCompleted, func(DomainEvent) {
				atomic.AddInt32(&ephemeral, 1)
			})
			e.Cancel()
		}
	}()

	wg.Wait()

	expected := int32(eventsPer * publishers * surviving)
	// Delivery is async; give the dispatch loops time to drain.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&delivered) < expected && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&delivered); got != expected {
		t.Errorf("delivered %d events, want exactly %d", got, expected)
	}
}

// waitFor polls cond until it returns true or times out.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}
