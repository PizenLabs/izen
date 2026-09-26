package engine

import (
	"sync"
	"sync/atomic"
)

// ── Engine→UI Event Bridge (non-blocking, bounded, lossless-under-burst) ───────
//
// Engine worker routines (LLM stream readers, tool executors, control loops)
// dispatch UI events to the Bubble Tea event loop. A naive `ch <- msg` send
// couples producer throughput to render throughput: while the UI is mid-frame
// (markdown AST parse, viewport reflow) a full channel stalls the engine
// worker, which in turn stalls the provider SSE reader and freezes the very
// tokens the UI is waiting to display.
//
// EventBridge fixes both failure modes:
//
//   - DEPTH: the channel is buffered with a capacity floored at
//     MinEventBuffer (256), so a full render frame never blocks a worker.
//   - NON-BLOCKING: Send is a single non-blocking select. It NEVER parks the
//     caller on a slow UI, so engine progress and UI rendering are decoupled
//     in both directions.
//   - ACCOUNTED: when the buffer is genuinely saturated the send is REFUSED
//     (reported via ok=false and counted by Dropped) instead of silently
//     blocking. The UI layer pairs this bridge with its own frame-paced token
//     ring for the overflow path, so a refused send is a measured condition,
//     never a hang.
//
// EventBridge is generic over the message type and imports nothing: the engine
// knows about "events", never about any particular UI framework. The
// presentation layer instantiates it as EventBridge[tea.Msg].
//
// SHUTDOWN SAFETY: Close marks the bridge closed and signals Done, but never
// closes the message channel. A channel closed while any worker is mid-Send
// panics with "send on closed channel"; because Send is deliberately lock-free
// there is no lock to serialize against, so the closure signal (not channel
// close) is the contract. Consumers stop on Done and then flush the tail with
// DrainAll, which is exactly the frame-pass → terminal-handler ordering the UI
// already uses.
//
// Zero value: a nil *EventBridge is safe — every method is nil-tolerant and
// degrades to a no-op / zero value, so composition roots that never wire a UI
// can hold a nil bridge.

const (
	// MinEventBuffer is the mandatory floor for an engine→UI event channel.
	// A burst of 100+ tokens/sec at a 33 FPS render cadence produces ~3 tokens
	// per frame; 256 slots absorbs ~85 frames (≈2.5s) of total render stall
	// before the UI is forced to fall back to its overflow ring.
	MinEventBuffer = 256

	// DefaultEventBuffer is the capacity used when a caller does not care.
	// It matches the production stream channel depth.
	DefaultEventBuffer = 1024
)

// EventBridge is a bounded, non-blocking, concurrency-safe channel between
// engine worker routines and a UI event loop. T is the message type (the
// presentation layer uses tea.Msg).
type EventBridge[T any] struct {
	ch      chan T
	done    chan struct{}
	cap     int
	dropped atomic.Uint64
	closed  atomic.Bool
	once    sync.Once
}

// NewEventBridge returns a bridge whose channel depth is max(capacity,
// MinEventBuffer). A non-positive capacity selects DefaultEventBuffer. The
// returned bridge is ready for concurrent use by any number of producers and
// one consumer.
func NewEventBridge[T any](capacity int) *EventBridge[T] {
	if capacity < MinEventBuffer {
		capacity = DefaultEventBuffer
	}
	return &EventBridge[T]{
		ch:   make(chan T, capacity),
		done: make(chan struct{}),
		cap:  capacity,
	}
}

// Chan returns the receive side of the bridge for the UI event loop. It is the
// only handle a consumer needs; producers must use Send.
func (b *EventBridge[T]) Chan() <-chan T {
	if b == nil {
		return nil
	}
	return b.ch
}

// Done returns a channel closed exactly once by Close. A consumer frame loop
// selects on it to learn when to stop reading and flush its tail.
func (b *EventBridge[T]) Done() <-chan struct{} {
	if b == nil {
		return nil
	}
	return b.done
}

// Cap returns the channel depth actually allocated.
func (b *EventBridge[T]) Cap() int {
	if b == nil {
		return 0
	}
	return b.cap
}

// Len returns the number of buffered events awaiting consumption. It is
// informational (flow diagnostics / tests) and is never used to make
// scheduling decisions.
func (b *EventBridge[T]) Len() int {
	if b == nil {
		return 0
	}
	return len(b.ch)
}

// Send performs a NON-BLOCKING send. It reports whether the event was accepted.
// A false result means the buffer is saturated (or the bridge is closed): the
// caller decides whether to retry, spill to an overflow buffer, or degrade —
// the bridge itself never blocks the calling worker.
func (b *EventBridge[T]) Send(msg T) (ok bool) {
	if b == nil || b.closed.Load() {
		return false
	}
	select {
	case b.ch <- msg:
		return true
	default:
		b.dropped.Add(1)
		return false
	}
}

// Dropped returns the number of sends refused because the buffer was full. It
// is the health signal for "the UI cannot keep up with the engine" and feeds
// the UI's stall telemetry.
func (b *EventBridge[T]) Dropped() uint64 {
	if b == nil {
		return 0
	}
	return b.dropped.Load()
}

// TryReceive performs a non-blocking receive. It reports ok=false only when the
// bridge is empty (or nil), so a UI frame pass can drain whatever is available
// without ever waiting on the engine.
//
// Close does NOT stop the receive side: the message channel is never closed, so
// a consumer that has seen Done can still flush the events that were already
// buffered. That is the documented shutdown order — stop on Done, then drain the
// tail — and refusing the tail after Close would silently lose the last tokens of
// a stream.
func (b *EventBridge[T]) TryReceive() (msg T, ok bool) {
	if b == nil {
		var zero T
		return zero, false
	}
	select {
	case msg, ok = <-b.ch:
		return msg, ok
	default:
		var zero T
		return zero, false
	}
}

// Drain removes and returns up to max buffered events, oldest first. It is the
// canonical frame-pass read: the caller (a Bubble Tea Update handler) consumes
// one frame's worth of events per tick. A max <= 0 drains everything currently
// buffered.
func (b *EventBridge[T]) Drain(max int) []T {
	if b == nil {
		return nil
	}
	if max <= 0 {
		return b.DrainAll()
	}
	out := make([]T, 0, min(max, len(b.ch)))
	for range max {
		msg, ok := b.TryReceive()
		if !ok {
			break
		}
		out = append(out, msg)
	}
	return out
}

// DrainAll removes and returns every buffered event, oldest first, without
// blocking. It is used by terminal handlers so no event is left unrendered
// when a stream ends. An empty bridge yields nil, not an empty slice, so a
// caller can distinguish "nothing to do" from "drained zero events".
func (b *EventBridge[T]) DrainAll() []T {
	if b == nil {
		return nil
	}
	var out []T
	for {
		msg, ok := b.TryReceive()
		if !ok {
			return out
		}
		out = append(out, msg)
	}
}

// Close marks the bridge closed and signals Done. It is idempotent, safe to
// call concurrently with Send from any number of workers, and never closes the
// message channel (see SHUTDOWN SAFETY above).
func (b *EventBridge[T]) Close() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		b.closed.Store(true)
		close(b.done)
	})
}

// Closed reports whether Close has been called.
func (b *EventBridge[T]) Closed() bool {
	return b != nil && b.closed.Load()
}
