package ui

import (
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/events"
)

// ── Phase 7 P3 / P4: provider stream-delta coalescing ───────────────────────
//
// A flood of EventProviderStreamDelta messages must never saturate the Bubble
// Tea event loop (the spinner freeze). The coalescer throttles deltas to ~one
// delivered message per interval and force-flushes the final accumulated delta
// before any authoritative (non-delta) event. These tests drive flushes
// deterministically through an injected scheduler — no wall-clock sleeps.

// manualCoalesceScheduler records scheduled flushes so a test can trigger them
// at precise points. The returned stop function removes the callback so
// scheduledCount reflects LIVE timers only.
type manualCoalesceScheduler struct {
	mu        sync.Mutex
	nextID    int
	callbacks []scheduledCallback
}

type scheduledCallback struct {
	id int
	f  func()
}

func (m *manualCoalesceScheduler) Schedule(_ time.Duration, f func()) func() {
	m.mu.Lock()
	m.nextID++
	id := m.nextID
	m.callbacks = append(m.callbacks, scheduledCallback{id: id, f: f})
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for i, c := range m.callbacks {
			if c.id == id {
				m.callbacks = append(m.callbacks[:i], m.callbacks[i+1:]...)
				return
			}
		}
	}
}

func (m *manualCoalesceScheduler) runScheduled() {
	m.mu.Lock()
	cbs := m.callbacks
	m.callbacks = nil
	m.mu.Unlock()
	for _, c := range cbs {
		c.f()
	}
}

func (m *manualCoalesceScheduler) scheduledCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.callbacks)
}

// recordingSender captures every message the coalescer forwards.
type recordingSender struct {
	mu    sync.Mutex
	types []string
	alive bool
}

func newRecordingSender() *recordingSender {
	return &recordingSender{alive: true}
}

func (r *recordingSender) send(msg tea.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.alive {
		return
	}
	switch m := msg.(type) {
	case domainEventMsg:
		if m.ev == nil {
			r.types = append(r.types, "<nil>")
			return
		}
		r.types = append(r.types, m.ev.Type())
	default:
		r.types = append(r.types, "<other>")
	}
}

func (r *recordingSender) delivered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.types))
	copy(out, r.types)
	return out
}

func (r *recordingSender) count(typ string) int {
	n := 0
	for _, t := range r.delivered() {
		if t == typ {
			n++
		}
	}
	return n
}

func newTestCoalescer(sched streamCoalesceScheduler) (*streamCoalescer, *recordingSender) {
	send := newRecordingSender()
	co := newStreamCoalescer(send.send, 50*time.Millisecond)
	co.sched = sched
	return co, send
}

// TestCoalescerThrottlesDeltaFlood proves the core P3 invariant: a flood of
// stream deltas collapses into a tiny number of delivered messages (one per
// flush window), so the event loop is never saturated.
func TestCoalescerThrottlesDeltaFlood(t *testing.T) {
	sched := &manualCoalesceScheduler{}
	co, send := newTestCoalescer(sched)

	const deltas = 1000
	for i := 0; i < deltas; i++ {
		co.Accept(events.NewProviderStreamDelta("req-1", "x"))
	}

	// No flush has run yet: exactly ONE scheduler callback is armed, and NOT
	// one delivered delta per input.
	if got := send.count(events.EventProviderStreamDelta); got != 0 {
		t.Fatalf("deltas delivered before any flush = %d, want 0", got)
	}
	if got := sched.scheduledCount(); got != 1 {
		t.Fatalf("flush timers armed = %d, want 1 (one batch per window)", got)
	}

	// One scheduled flush delivers exactly ONE coalesced delta.
	sched.runScheduled()
	if got := send.count(events.EventProviderStreamDelta); got != 1 {
		t.Fatalf("deltas delivered after one flush = %d, want 1", got)
	}

	// Further deltas after a flush arm a fresh timer.
	co.Accept(events.NewProviderStreamDelta("req-1", "y"))
	if got := sched.scheduledCount(); got != 1 {
		t.Fatalf("flush timers armed after next delta = %d, want 1", got)
	}
	sched.runScheduled()
	if got := send.count(events.EventProviderStreamDelta); got != 2 {
		t.Fatalf("deltas delivered after second flush = %d, want 2", got)
	}
	if got := len(send.delivered()); got != 2 {
		t.Fatalf("total delivered messages = %d, want 2 for %d input deltas (event loop not saturated)", got, deltas+1)
	}
}

// TestCoalescerFlushesBeforeAuthoritativeEvent proves the no-loss guarantee:
// when an authoritative event arrives with pending delta state, the final
// accumulated delta is delivered BEFORE the authoritative event.
func TestCoalescerFlushesBeforeAuthoritativeEvent(t *testing.T) {
	sched := &manualCoalesceScheduler{}
	co, send := newTestCoalescer(sched)

	co.Accept(events.NewProviderStreamDelta("req-1", "final-chunk"))
	// The authoritative completion event arrives before any timer fired.
	co.Accept(events.NewExecutionFinished("req-1", true, "changed"))

	delivered := send.delivered()
	if len(delivered) != 2 {
		t.Fatalf("delivered = %v, want [delta, finished]", delivered)
	}
	if delivered[0] != events.EventProviderStreamDelta {
		t.Fatalf("first delivered = %q, want the final delta flushed before the authoritative event", delivered[0])
	}
	if delivered[1] != events.EventExecutionFinished {
		t.Fatalf("second delivered = %q, want the authoritative event passed through", delivered[1])
	}
	// The pending batch was claimed by the barrier flush: no stray timer is armed.
	if got := sched.scheduledCount(); got != 0 {
		t.Fatalf("flush timers armed after barrier = %d, want 0", got)
	}
}

// TestCoalescerNeverDropsAuthoritativeEvents proves the hard rule: terminal /
// lifecycle / approval events are never coalesced or dropped.
func TestCoalescerNeverDropsAuthoritativeEvents(t *testing.T) {
	sched := &manualCoalesceScheduler{}
	co, send := newTestCoalescer(sched)

	authoritative := []events.DomainEvent{
		events.NewExecutionStarted("req-1", "autonomy", "do the thing"),
		events.NewExecutionFinished("req-1", true, "changed"),
		events.NewMutationStarted("req-1", []string{"a.txt"}),
		events.NewProviderUsageUpdate("req-1", "model", 10, 5, 2),
	}
	for _, ev := range authoritative {
		co.Accept(ev)
	}

	delivered := send.delivered()
	if len(delivered) != len(authoritative) {
		t.Fatalf("delivered = %v, want all %d authoritative events passed through unchanged", delivered, len(authoritative))
	}
	for i, ev := range authoritative {
		if delivered[i] != ev.Type() {
			t.Fatalf("delivered[%d] = %q, want %q", i, delivered[i], ev.Type())
		}
	}
}

// TestCoalescerCloseStopsDelivery proves Close cancels the pending timer and
// ignores further events (cancellation-aware, no leaked timers or late sends).
func TestCoalescerCloseStopsDelivery(t *testing.T) {
	sched := &manualCoalesceScheduler{}
	co, send := newTestCoalescer(sched)

	co.Accept(events.NewProviderStreamDelta("req-1", "a"))
	co.Close()
	sched.runScheduled()

	if got := send.count(events.EventProviderStreamDelta); got != 0 {
		t.Fatalf("delta delivered after Close = %d, want 0", got)
	}
	co.Accept(events.NewExecutionFinished("req-1", true, "changed"))
	if got := send.count(events.EventExecutionFinished); got != 0 {
		t.Fatalf("event delivered after Close = %d, want 0", got)
	}
}

// TestCoalescerDoesNotSaturateEventLoopWithRealTimer is the integration
// variant of P4: with a real scheduler and a short window, producing many
// deltas in quick succession delivers a bounded number of messages, and the
// authoritative terminal event is always delivered.
func TestCoalescerDoesNotSaturateEventLoopWithRealTimer(t *testing.T) {
	send := newRecordingSender()
	co := newStreamCoalescer(send.send, 10*time.Millisecond)

	const deltas = 500
	for i := 0; i < deltas; i++ {
		co.Accept(events.NewProviderStreamDelta("req-1", "x"))
	}
	// Barrier: force-flush and deliver the authoritative completion.
	co.Accept(events.NewExecutionFinished("req-1", true, "changed"))
	co.Close()

	// Deterministic upper bound for the real-timer run: the loop delivers at
	// most one delta per 10ms window plus one barrier flush plus the terminal
	// event — far below the 500 inputs. Wait briefly for the terminal event.
	deadline := time.Now().Add(500 * time.Millisecond)
	for send.count(events.EventExecutionFinished) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	deltaDelivered := send.count(events.EventProviderStreamDelta)
	if deltaDelivered > deltas/5 {
		t.Fatalf("deltas delivered = %d of %d, want heavy coalescing (max ~%d)", deltaDelivered, deltas, deltas/5)
	}
	if got := send.count(events.EventExecutionFinished); got != 1 {
		t.Fatalf("terminal event delivered = %d, want exactly 1", got)
	}
}
