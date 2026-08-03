package events

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sampleEvent struct {
	id   int
	text string
}

func TestPublishDispatch(t *testing.T) {
	b := New[sampleEvent](16)
	defer b.Close()

	var mu sync.Mutex
	var got []sampleEvent
	sub := b.Subscribe(func(ev sampleEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	})
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}

	b.Publish(sampleEvent{id: 1, text: "one"})

	if !waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}) {
		t.Fatalf("handler did not receive event, got %d", len(got))
	}

	mu.Lock()
	defer mu.Unlock()
	if got[0].text != "one" {
		t.Errorf("received %+v, want {1 one}", got[0])
	}
}

func TestMultipleSubscribers(t *testing.T) {
	b := New[sampleEvent](256)
	defer b.Close()

	const subs = 5
	var counters [subs]int32
	for i := 0; i < subs; i++ {
		b.Subscribe(func(sampleEvent) { atomic.AddInt32(&counters[i], 1) })
	}

	const total = 100
	for i := 0; i < total; i++ {
		b.Publish(sampleEvent{id: i})
	}

	if !waitFor(t, func() bool {
		for i := 0; i < subs; i++ {
			if atomic.LoadInt32(&counters[i]) != total {
				return false
			}
		}
		return true
	}) {
		for i := 0; i < subs; i++ {
			t.Errorf("subscriber %d received %d/%d", i, atomic.LoadInt32(&counters[i]), total)
		}
	}
}

// TestNonBlockingPublishUnderLoad verifies the core headless guarantee: a slow
// consumer never stalls the publisher. Events dropped on a full buffer are
// counted on the subscription.
func TestNonBlockingPublishUnderLoad(t *testing.T) {
	const buffer = 8
	const total = 1000
	slow := func(sampleEvent) { time.Sleep(10 * time.Millisecond) }

	b := New[sampleEvent](buffer)
	sub := b.Subscribe(slow)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}

	start := time.Now()
	for i := 0; i < total; i++ {
		b.Publish(sampleEvent{id: i})
	}
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("Publish blocked on slow consumer: %d publishes took %s", total, elapsed)
	}
	if dropped := sub.Dropped(); dropped == 0 {
		t.Error("expected dropped events under load, got 0")
	}
	b.Close()
}

func TestSubscriptionCancel(t *testing.T) {
	b := New[sampleEvent](16)
	defer b.Close()

	var mu sync.Mutex
	var got []sampleEvent
	sub := b.Subscribe(func(ev sampleEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	})

	b.Publish(sampleEvent{id: 1})
	if !waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}) {
		t.Fatal("event not delivered before cancel")
	}

	sub.Cancel()
	sub.Cancel() // idempotent

	b.Publish(sampleEvent{id: 2})
	time.Sleep(30 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Errorf("handler invoked %d times after Cancel, want 1", len(got))
	}
}

func TestClose(t *testing.T) {
	b := New[sampleEvent](16)

	var count int32
	b.Subscribe(func(sampleEvent) { atomic.AddInt32(&count, 1) })

	b.Publish(sampleEvent{id: 1})
	if !waitFor(t, func() bool { return atomic.LoadInt32(&count) == 1 }) {
		t.Fatal("event not delivered before close")
	}

	b.Close()
	b.Close() // idempotent

	// Publish after close is a silent no-op.
	b.Publish(sampleEvent{id: 2})
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("handler invoked %d times after Close, want 1", got)
	}

	// Subscribe after close returns nil.
	if sub := b.Subscribe(func(sampleEvent) {}); sub != nil {
		t.Error("Subscribe after Close returned a non-nil subscription")
	}
}

func TestSubscribeNilHandler(t *testing.T) {
	b := New[sampleEvent](16)
	defer b.Close()
	if sub := b.Subscribe(nil); sub != nil {
		t.Error("Subscribe(nil) returned a non-nil subscription")
	}
}

func TestConcurrentSubscribePublishCancel(t *testing.T) {
	b := New[sampleEvent](1024)
	defer b.Close()

	var delivered int32
	const publishers = 4
	const eventsPer = 200
	const surviving = 4

	subs := make([]*Subscription[sampleEvent], 0, 8)
	for i := 0; i < 8; i++ {
		sub := b.Subscribe(func(sampleEvent) { atomic.AddInt32(&delivered, 1) })
		if sub == nil {
			t.Fatal("Subscribe returned nil")
		}
		subs = append(subs, sub)
	}
	for _, s := range subs[:8-surviving] {
		s.Cancel()
	}
	time.Sleep(10 * time.Millisecond)

	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < eventsPer; i++ {
				b.Publish(sampleEvent{id: i})
			}
		}()
	}

	var ephemeral int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			e := b.Subscribe(func(sampleEvent) { atomic.AddInt32(&ephemeral, 1) })
			e.Cancel()
		}
	}()

	wg.Wait()

	expected := int32(eventsPer * publishers * surviving)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&delivered) < expected && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&delivered); got != expected {
		t.Errorf("delivered %d events, want exactly %d", got, expected)
	}
}

func TestDefaultBufferSizeFallback(t *testing.T) {
	b := New[sampleEvent](0)
	defer b.Close()
	if b.buffer != DefaultBufferSize {
		t.Errorf("buffer = %d, want %d", b.buffer, DefaultBufferSize)
	}
}

func TestTypeIsolation(t *testing.T) {
	// Two buses over different event types must not interfere.
	ints := New[int](16)
	defer ints.Close()
	strs := New[string](16)
	defer strs.Close()

	var intCount, strCount int32
	ints.Subscribe(func(int) { atomic.AddInt32(&intCount, 1) })
	strs.Subscribe(func(string) { atomic.AddInt32(&strCount, 1) })

	ints.Publish(42)
	ints.Publish(43)
	strs.Publish("hello")

	if !waitFor(t, func() bool {
		return atomic.LoadInt32(&intCount) == 2 && atomic.LoadInt32(&strCount) == 1
	}) {
		t.Errorf("int delivered %d, want 2; str delivered %d, want 1",
			atomic.LoadInt32(&intCount), atomic.LoadInt32(&strCount))
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
