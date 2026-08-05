package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/pkg/engine/ir"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// drainFact forwards the next tea.Msg on ch, failing the test on timeout.
func drainFact(t *testing.T, ch <-chan tea.Msg, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(timeout):
		t.Fatal("timed out waiting for forwarded control fact")
		return nil
	}
}

// TestListenControlEventsForwardsIteration verifies control.iteration facts are
// forwarded into the Bubble Tea loop as controlFactMsg.
func TestListenControlEventsForwardsIteration(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	defer bus.Close()
	ch := make(chan tea.Msg, 16)
	send := func(m tea.Msg) { ch <- m }

	cmd := ListenControlEvents(bus, send)
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("setup cmd returned %v, want nil", msg)
	}

	bus.Publish(telemetry.NewControlIteration("run-1",
		map[string]ir.NodeState{"a": ir.StateRunning}, map[string]int{"a": 1}))

	got, ok := drainFact(t, ch, 2*time.Second).(controlFactMsg)
	if !ok {
		t.Fatalf("got %T, want controlFactMsg", got)
	}
	if got.ev.Type() != telemetry.EventControlIteration {
		t.Errorf("event type = %q, want %q", got.ev.Type(), telemetry.EventControlIteration)
	}
	p, ok := got.ev.Payload().(*telemetry.ControlIterationPayload)
	if !ok {
		t.Fatalf("payload = %T, want *ControlIterationPayload", got.ev.Payload())
	}
	if p.RunID != "run-1" || p.NodeStates["a"] != "running" || p.Attempts["a"] != 1 {
		t.Errorf("payload = %+v", p)
	}
}

// TestListenControlEventsForwardsNodeObserved verifies control.node_observed
// facts are forwarded into the Bubble Tea loop as controlFactMsg.
func TestListenControlEventsForwardsNodeObserved(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	defer bus.Close()
	ch := make(chan tea.Msg, 16)
	send := func(m tea.Msg) { ch <- m }

	cmd := ListenControlEvents(bus, send)
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("setup cmd returned %v, want nil", msg)
	}

	bus.Publish(telemetry.NewControlNodeObserved("run-1", ir.ObservationPayload{
		NodeID: "execute",
		OK:     true,
		Output: "produced 1 patch",
	}))

	got, ok := drainFact(t, ch, 2*time.Second).(controlFactMsg)
	if !ok {
		t.Fatalf("got %T, want controlFactMsg", got)
	}
	if got.ev.Type() != telemetry.EventControlNodeObserved {
		t.Errorf("event type = %q, want %q", got.ev.Type(), telemetry.EventControlNodeObserved)
	}
	p, ok := got.ev.Payload().(*telemetry.ControlNodeObservedPayload)
	if !ok {
		t.Fatalf("payload = %T, want *ControlNodeObservedPayload", got.ev.Payload())
	}
	if p.NodeID != "execute" || !p.OK {
		t.Errorf("payload = %+v", p)
	}
}

// TestListenControlEventsIsNonBlocking verifies the publisher is never stalled
// even when the subscriber buffer is full: publishing a burst beyond the
// capacity must still return immediately (facts are dropped, never blocking).
func TestListenControlEventsIsNonBlocking(t *testing.T) {
	bus := telemetry.NewEventBus(2)
	defer bus.Close()
	ch := make(chan tea.Msg, 2)
	send := func(m tea.Msg) { ch <- m }

	cmd := ListenControlEvents(bus, send)
	cmd()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			bus.Publish(telemetry.NewControlNodeObserved("run-1", ir.ObservationPayload{
				NodeID: "execute", OK: true,
			}))
		}
		close(done)
	}()
	select {
	case <-done:
		// The burst completed without blocking.
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscription buffer")
	}
}

// TestListenControlEventsNilSafe verifies nil bus / sender never panic and
// never deliver, while still yielding a non-nil (batch-safe) cmd.
func TestListenControlEventsNilSafe(t *testing.T) {
	if cmd := ListenControlEvents(nil, nil); cmd == nil {
		t.Fatal("nil bus/send must still yield a batch-safe no-op cmd")
	}
	sent := false
	cmd := ListenControlEvents(nil, func(m tea.Msg) { sent = true })
	if msg := cmd(); msg != nil {
		t.Errorf("nil bus cmd = %v, want nil", msg)
	}
	if sent {
		t.Error("nil bus must not deliver messages")
	}
}
