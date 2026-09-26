package engine

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestEventBridgeDepthIsFloored pins the depth contract from the spec: no
// engine→UI event channel may be shallower than MinEventBuffer. A shallow
// channel couples an engine worker's progress to the render loop's latency,
// which is how a slow frame ends up stalling the provider SSE reader.
func TestEventBridgeDepthIsFloored(t *testing.T) {
	if MinEventBuffer < 256 {
		t.Fatalf("MinEventBuffer = %d, want >= 256", MinEventBuffer)
	}
	for _, requested := range []int{-100, -1, 0, 1, 8, 64, 255, MinEventBuffer - 1} {
		b := NewEventBridge[int](requested)
		if b.Cap() < MinEventBuffer {
			t.Errorf("NewEventBridge(%d).Cap() = %d, want >= %d", requested, b.Cap(), MinEventBuffer)
		}
	}
	// An explicit larger request is honoured.
	if got := NewEventBridge[int](4096).Cap(); got != 4096 {
		t.Errorf("NewEventBridge(4096).Cap() = %d, want 4096", got)
	}
}

// TestEventBridgeSendNeverBlocks is the load-bearing property: with a consumer
// that never reads, a burst that fits the buffer must still complete. A blocking
// send here is the "UI render starvation" failure the bridge exists to prevent.
func TestEventBridgeSendNeverBlocks(t *testing.T) {
	b := NewEventBridge[int](MinEventBuffer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range b.Cap() {
			if !b.Send(i) {
				t.Errorf("send %d refused below capacity", i)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a burst that fits the buffer blocked the producer")
	}
	if b.Len() != b.Cap() {
		t.Errorf("buffered %d of %d", b.Len(), b.Cap())
	}
}

// TestEventBridgeRefusesRatherThanBlocks pins the saturation contract: once the
// buffer is full a send is refused and COUNTED, never parked and never silently
// overwritten. The caller can then decide (the UI spills to its token ring);
// the bridge itself never makes that decision for it.
func TestEventBridgeRefusesRatherThanBlocks(t *testing.T) {
	b := NewEventBridge[int](MinEventBuffer)
	for i := range b.Cap() {
		if !b.Send(i) {
			t.Fatalf("send %d refused below capacity", i)
		}
	}
	if b.Send(-1) {
		t.Error("a full buffer must refuse the send")
	}
	if got := b.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d, want 1 (a refusal must be measured, not silent)", got)
	}
	// Still refused, and still counted — the counter is a health signal.
	b.Send(-1)
	if got := b.Dropped(); got != 2 {
		t.Errorf("Dropped() = %d, want 2", got)
	}
	// Draining frees capacity and the bridge accepts again.
	b.DrainAll()
	if !b.Send(-1) {
		t.Error("a drained bridge must accept sends again")
	}
	if b.Dropped() != 2 {
		t.Errorf("a successful send must not increment Dropped(): got %d", b.Dropped())
	}
}

// TestEventBridgeDrainIsFramePacedAndOrdered is the frame-pass contract: a
// consumer takes at most `max` events per tick, oldest first, and a `max <= 0`
// drains everything buffered. This is what lets the UI consume one frame's worth
// of engine events per tick without racing the producer.
func TestEventBridgeDrainIsFramePacedAndOrdered(t *testing.T) {
	const n = 500
	b := NewEventBridge[int](n + 16) // deep enough that every send below is accepted
	for i := range n {
		b.Send(i)
	}
	frames := 0
	seen := 0
	for seen < n {
		batch := b.Drain(64)
		if len(batch) > 64 {
			t.Fatalf("Drain(64) returned %d events", len(batch))
		}
		for _, v := range batch {
			if v != seen {
				t.Fatalf("frame %d: got %d, want %d (drain must be FIFO)", frames, v, seen)
			}
			seen++
		}
		if len(batch) == 0 {
			break
		}
		frames++
	}
	if seen != n {
		t.Fatalf("drained %d of %d events", seen, n)
	}
	if frames != 8 {
		t.Errorf("500 events at 64/frame took %d frames, want 8", frames)
	}

	b.Send(1)
	b.Send(2)
	if got := b.Drain(0); len(got) != 2 {
		t.Errorf("Drain(0) = %v, want both buffered events", got)
	}
	if got := b.Drain(0); got != nil {
		t.Errorf("Drain(0) on an empty bridge = %v, want nil", got)
	}
}

// TestEventBridgeCloseIsSafeUnderConcurrentSend is the shutdown drill. Close
// signals closure via Done and NEVER closes the message channel, because a
// channel closed while a worker is mid-Send panics with "send on closed channel"
// — and Send is deliberately lock-free, so there is no lock to serialize
// against.
func TestEventBridgeCloseIsSafeUnderConcurrentSend(t *testing.T) {
	b := NewEventBridge[int](MinEventBuffer)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.Send(1) // may be refused; must never panic
				}
			}
		}()
	}

	time.Sleep(20 * time.Millisecond)
	b.Close()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()

	if !b.Closed() {
		t.Error("Closed() = false after Close()")
	}
	if b.Send(1) {
		t.Error("a closed bridge must refuse sends")
	}
	select {
	case <-b.Done():
	default:
		t.Error("Done() must be closed after Close()")
	}
	// Idempotent: a second Close must not panic on an already-closed signal.
	b.Close()

	// Everything buffered before the close is still readable, so a terminal
	// handler can flush its tail.
	if _, ok := b.TryReceive(); !ok {
		t.Error("buffered events must remain readable after Close")
	}
}

// TestEventBridgeCloseReleasesProducers is the liveness guarantee: after Close a
// producer must not block or spin forever, it must be told the bridge is gone.
func TestEventBridgeCloseReleasesProducers(t *testing.T) {
	b := NewEventBridge[int](MinEventBuffer)
	b.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			if !b.Send(1) {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a closed bridge must release producers immediately")
	}
}

// TestEventBridgeGenericOverMessageType proves the bridge is framework
// agnostic: the engine knows about "events", never about any UI framework. The
// presentation layer instantiates it as EventBridge[tea.Msg].
func TestEventBridgeGenericOverMessageType(t *testing.T) {
	type engineEvent struct {
		Kind string
		N    int
	}
	b := NewEventBridge[engineEvent](MinEventBuffer)
	if !b.Send(engineEvent{Kind: "token", N: 1}) {
		t.Fatal("send refused below capacity")
	}
	got, ok := b.TryReceive()
	if !ok || got.Kind != "token" || got.N != 1 {
		t.Fatalf("TryReceive() = (%+v, %v)", got, ok)
	}

	strings := NewEventBridge[string](MinEventBuffer)
	strings.Send("hello")
	if v, ok := strings.TryReceive(); !ok || v != "hello" {
		t.Fatalf("TryReceive() = (%q, %v)", v, ok)
	}
}

// TestEventBridgeTryReceiveNeverBlocks is the consumer-side half: a frame pass
// that drains an idle bridge must return immediately, never park the render
// goroutine waiting for the next engine event.
func TestEventBridgeTryReceiveNeverBlocks(t *testing.T) {
	b := NewEventBridge[int](MinEventBuffer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			if _, ok := b.TryReceive(); ok {
				t.Error("TryReceive reported a message on an empty bridge")
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TryReceive must never block")
	}
}

// TestEventBridgeHighThroughputOrdering is the -race drill for the real load
// profile: several engine workers publishing concurrently while the UI drains a
// frame's worth at a time. Nothing is lost and nothing is corrupted.
func TestEventBridgeHighThroughputOrdering(t *testing.T) {
	b := NewEventBridge[string](MinEventBuffer)
	const workers, perWorker = 4, 500

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				for !b.Send(strconv.Itoa(w*perWorker + i)) {
					// Saturated: retry, mirroring a producer that waits for the
					// next frame instead of dropping the event.
				}
			}
		}()
	}

	received := map[string]int{}
	deadline := time.Now().Add(10 * time.Second)
	for len(received) < workers*perWorker {
		batch := b.Drain(128)
		if len(batch) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("timed out with %d of %d events", len(received), workers*perWorker)
			}
			continue
		}
		for _, v := range batch {
			received[v]++
		}
	}
	wg.Wait()
	for v, n := range received {
		if n != 1 {
			t.Errorf("event %q received %d times, want exactly 1", v, n)
		}
	}
}

// TestEventBridgeNilSafe keeps composition roots that never wire a UI
// panic-free — the zero value of the bridge is a usable no-op.
func TestEventBridgeNilSafe(t *testing.T) {
	var b *EventBridge[int]
	if b.Send(1) {
		t.Error("nil bridge Send must report false")
	}
	if _, ok := b.TryReceive(); ok {
		t.Error("nil bridge TryReceive must report false")
	}
	if b.Drain(10) != nil || b.DrainAll() != nil {
		t.Error("nil bridge drain must return nil")
	}
	if b.Cap() != 0 || b.Len() != 0 || b.Dropped() != 0 || b.Closed() {
		t.Error("nil bridge accessors must return zero values")
	}
	if b.Chan() != nil {
		t.Error("nil bridge Chan() must be nil")
	}
	b.Close() // must not panic
}
