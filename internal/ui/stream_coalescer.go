package ui

import (
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/events"
)

// Phase 7 P3: provider stream-delta coalescer.
//
// The RuntimeExecutor publishes one EventProviderStreamDelta per content chunk
// of a live provider stream. Cloud providers stream in many small chunks, so a
// naive bus-to-UI projection forwards hundreds of domainEventMsg per second
// into the Bubble Tea event loop. Bubble Tea processes messages strictly in
// FIFO order, so that flood STARVES the lower-priority shimmerFrameMsg (the
// ~100ms spinner tick): the spinner freezes even though the loop is technically
// alive. The freeze is event-loop backpressure, not a dead loop.
//
// The coalescer throttles the flood at the bus-to-UI boundary: at most ONE
// coalesced stream-delta domainEventMsg is forwarded per interval (default
// 50ms, roughly 20 UI updates/sec). It never drops the final accumulated
// output: any non-delta (authoritative) event first force-flushes the pending
// delta so the last content chunk is delivered BEFORE the authoritative event.
//
// Only EventProviderStreamDelta is coalesced. Every other event type is an
// authoritative barrier: it flushes pending delta state and passes through
// unchanged. Authorization, execution lifecycle, mutation evidence,
// verification results, terminal outcomes, approval state, cancellation and
// workflow transitions are never coalesced, throttled or dropped.
//
// Ordering: all events for this projection route through a single coalescer,
// so within this projection flush-before-authoritative is guaranteed by
// construction. The scheduler is injectable so tests can drive flushes
// deterministically (P4) instead of sleeping on wall-clock timers.

// uiStreamCoalesceInterval is the production coalescing window: one coalesced
// UI update per ~50ms. Fast enough to feel live, slow enough that ~100ms
// spinner frames always get scheduled.
const uiStreamCoalesceInterval = 50 * time.Millisecond

// streamCoalesceScheduler abstracts the delayed-flush scheduling so tests can
// trigger flushes deterministically. afterFuncScheduler is the production one.
type streamCoalesceScheduler interface {
	// Schedule arranges for f to run after d and returns a stop function.
	Schedule(d time.Duration, f func()) func()
}

type afterFuncScheduler struct{}

func (afterFuncScheduler) Schedule(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// maxCoalescedDeltaBytes bounds the accumulated delta text retained for one
// coalesced event. The model's delta handler only observes the streaming
// stage, so retaining the tail is sufficient; the bound prevents an unbounded
// buffer when a provider streams very large payloads.
const maxCoalescedDeltaBytes = 4096

// streamCoalescer bridges bus events into the Bubble Tea event loop with the
// delta throttle described above. It is safe for concurrent use: bus dispatch
// goroutines call Accept; the flush scheduler goroutine calls flushNow; every
// external effect funnels through the single injected send function.
type streamCoalescer struct {
	mu       sync.Mutex
	send     func(tea.Msg)
	interval time.Duration
	sched    streamCoalesceScheduler

	hasPending     bool
	pendingRequest string
	pendingDelta   strings.Builder
	stopTimer      func()
	closed         bool
}

// newStreamCoalescer constructs the coalescer over the injected send function
// (p.Send in production). interval <= 0 falls back to the production default.
func newStreamCoalescer(send func(tea.Msg), interval time.Duration) *streamCoalescer {
	if interval <= 0 {
		interval = uiStreamCoalesceInterval
	}
	return &streamCoalescer{
		send:     send,
		interval: interval,
		sched:    afterFuncScheduler{},
	}
}

// Accept forwards one bus event through the coalescing projection. Stream
// deltas are accumulated into at most one coalesced message per interval; every
// other event flushes pending delta state and passes through unchanged.
func (c *streamCoalescer) Accept(ev events.DomainEvent) {
	if ev == nil {
		return
	}
	if ev.Type() == events.EventProviderStreamDelta {
		p, ok := ev.Payload().(events.ProviderStreamDeltaPayload)
		if !ok {
			return
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.accumulate(p)
		c.mu.Unlock()
		return
	}
	// Authoritative barrier: flush the final accumulated delta FIRST, then
	// forward the event unchanged.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	reqID, delta, has := c.takePendingLocked()
	c.mu.Unlock()
	if has {
		c.sendCoalesced(reqID, delta)
	}
	c.send(domainEventMsg{ev: ev})
}

// Close stops the coalescer: pending timers are cancelled and further events
// are ignored. Idempotent.
func (c *streamCoalescer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.stopTimer != nil {
		c.stopTimer()
		c.stopTimer = nil
	}
	c.hasPending = false
	c.pendingDelta.Reset()
}

// accumulate appends a delta chunk to the pending batch and arms the flush
// timer when the batch was empty. The caller holds c.mu.
func (c *streamCoalescer) accumulate(p events.ProviderStreamDeltaPayload) {
	if !c.hasPending {
		c.hasPending = true
		c.pendingRequest = p.RequestID
		c.stopTimer = c.sched.Schedule(c.interval, c.flushNow)
	}
	if c.pendingDelta.Len() < maxCoalescedDeltaBytes {
		c.pendingDelta.WriteString(p.Delta)
	}
}

// flushNow is the scheduled flush: it emits ONE coalesced delta event for the
// accumulated batch. It is a no-op when there is nothing pending.
func (c *streamCoalescer) flushNow() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	reqID, delta, has := c.takePendingLocked()
	c.mu.Unlock()
	if has {
		c.sendCoalesced(reqID, delta)
	}
}

// takePendingLocked claims and clears the pending batch. The caller holds c.mu.
func (c *streamCoalescer) takePendingLocked() (requestID, delta string, has bool) {
	if !c.hasPending {
		return "", "", false
	}
	if c.stopTimer != nil {
		c.stopTimer()
		c.stopTimer = nil
	}
	requestID = c.pendingRequest
	delta = c.pendingDelta.String()
	c.hasPending = false
	c.pendingDelta.Reset()
	return requestID, delta, true
}

// sendCoalesced forwards the accumulated delta as a single domainEventMsg.
func (c *streamCoalescer) sendCoalesced(requestID, delta string) {
	c.send(domainEventMsg{ev: events.NewProviderStreamDelta(requestID, delta)})
}
