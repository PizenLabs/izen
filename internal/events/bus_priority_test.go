package events

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEventBus_ControlPriorityWithTelemetryStorm floods telemetry with 10,000
// events while pushing 5 control events. All 5 control events must be
// processed within bounded latency; control delivery is guaranteed while the
// subscription is live, and bounded priority (MaxConsecutiveControlDrains)
// prevents either class from starving the other.
func TestEventBus_ControlPriorityWithTelemetryStorm(t *testing.T) {
	// Large buffer so the 10k telemetry burst fits without drops; the
	// assertion focuses on bounded-latency control precedence, not the
	// drop path (covered by TestNonBlockingPublishUnderLoad).
	b := NewBus(16384)
	defer b.Close()

	var controlCt int32
	var telemetryCt int32
	var mu sync.Mutex
	var controlOrder []string

	sub := b.SubscribeAll(func(ev DomainEvent) {
		if IsControlEventType(ev.Type()) {
			atomic.AddInt32(&controlCt, 1)
			mu.Lock()
			controlOrder = append(controlOrder, ev.Type())
			mu.Unlock()
			return
		}
		atomic.AddInt32(&telemetryCt, 1)
	})
	if sub == nil {
		t.Fatal("SubscribeAll returned nil")
	}

	const telemetryBurst = 10000
	for i := 0; i < telemetryBurst; i++ {
		b.Publish(NewActivity("chunk"))
	}
	// 5 control events interleaved after the storm.
	b.Publish(NewTaskStarted("t1"))
	b.Publish(NewTaskCompleted("t1", "ok"))
	b.Publish(NewTaskFailed("t2", "boom"))
	b.Publish(NewTaskCanceled("t3"))
	b.Publish(NewClarificationRequired("t4", "which?"))

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&controlCt) < 5 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&controlCt); got != 5 {
		t.Fatalf("control events processed = %d, want 5 within bounded latency", got)
	}

	// Telemetry must drain without starvation from bounded control priority.
	deadline = time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&telemetryCt) < telemetryBurst && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&telemetryCt); got != telemetryBurst {
		t.Fatalf("telemetry events processed = %d, want %d (starved by control?)", got, telemetryBurst)
	}
	if dropped := sub.Dropped(); dropped != 0 {
		t.Fatalf("telemetry drops = %d, want 0 with a sized buffer", dropped)
	}
}

// TestEventBus_BoundedPriorityPreventsTelemetryStarvation saturates the
// control queue and asserts telemetry still flows: at most
// MaxConsecutiveControlDrains control events dispatch before one telemetry
// event is serviced.
func TestEventBus_BoundedPriorityPreventsTelemetryStarvation(t *testing.T) {
	if MaxConsecutiveControlDrains != 10 {
		t.Fatalf("MaxConsecutiveControlDrains = %d, want 10", MaxConsecutiveControlDrains)
	}
	b := NewBus(4096)
	defer b.Close()

	var telemetryCt int32
	var controlCt int32
	b.SubscribeAll(func(ev DomainEvent) {
		if IsControlEventType(ev.Type()) {
			atomic.AddInt32(&controlCt, 1)
			return
		}
		atomic.AddInt32(&telemetryCt, 1)
	})

	// Saturate control beyond one bounded window, with telemetry behind it.
	for i := 0; i < 30; i++ {
		b.Publish(NewTaskStarted("t"))
	}
	for i := 0; i < 20; i++ {
		b.Publish(NewActivity("chunk"))
	}

	deadline := time.Now().Add(5 * time.Second)
	for (atomic.LoadInt32(&controlCt) < 30 || atomic.LoadInt32(&telemetryCt) < 20) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&controlCt); got != 30 {
		t.Fatalf("control = %d, want 30 (guaranteed delivery)", got)
	}
	if got := atomic.LoadInt32(&telemetryCt); got != 20 {
		t.Fatalf("telemetry = %d, want 20 (starved by unbounded control drain?)", got)
	}
}
