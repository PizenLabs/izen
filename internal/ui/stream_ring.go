package ui

import (
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Engine→UI Stream Ring Buffer (decoupled token transport) ────────────────
//
// The producer goroutine that reads the LLM stream writes each token with a
// NON-BLOCKING channel send. When the bounded streamCh is temporarily full —
// the UI event loop is busy and hasn't re-armed the next read yet — the token
// is pushed into this lock-free ring instead of stalling the LLM thread.
// The UI drains the ring inside its frame-flush pass (FrameTickMsg) and in
// every terminal stream handler, so high-throughput bursts (50+ tok/s) never
// create event-loop backpressure and no byte is ever dropped.
//
// This is the classic Vyukov bounded MPMC ring (seq-lock per slot) reduced to
// single-producer/single-consumer. Both Push and Pop are wait-free in the
// uncontended case and require no locks; the only cross-goroutine access is
// through atomic operations on slot sequence numbers and the head/tail
// cursors, so the producer goroutine may hold its own captured *streamRing
// reference while the main Update goroutine drains it concurrently.

// streamRingCapacity is the overflow depth. Streaming tokens are typically a
// handful of runes; 4096 cells (~64KB of msg slots) comfortably absorbs
// worst-case burst lag while the channel + frame loop drains.
const streamRingCapacity = 4096

// streamRingSlot holds one queued message and its sequence permit.
type streamRingSlot struct {
	seq atomic.Uint64
	msg tea.Msg
}

// streamRing is a lock-free single-producer/single-consumer message ring.
// The buffer size must be a power of two.
type streamRing struct {
	mask  uint64
	buf   []streamRingSlot
	head  atomic.Uint64 // consumer cursor (indicates used slot)
	tail  atomic.Uint64 // producer cursor (indicates used slot)
	count atomic.Uint64 // current occupancy (informational)
}

// newStreamRing creates a ring of capacity n (rounded up to a power of two).
func newStreamRing(n int) *streamRing {
	if n <= 0 {
		n = streamRingCapacity
	}
	cap := 1
	for cap < n {
		cap <<= 1
	}
	r := &streamRing{mask: uint64(cap - 1), buf: make([]streamRingSlot, cap)}
	for i := range r.buf {
		r.buf[i].seq.Store(uint64(i))
	}
	return r
}

// Push appends a message to the ring. It returns false only when the ring is
// full (in which case the caller must fall back to a blocking channel send).
// SPSC simplification of the Vyukov sequence test: a slot is writable when its
// sequence equals the producer cursor — a mismatch means the consumer has not
// yet released it (ring full). No wrap-bound sign check is needed because the
// consumer always frees a slot one lap ahead (Pop stores seq = head+cap), so
// at most one lap of slack exists — a signed wrap can never be observed.
func (r *streamRing) Push(msg tea.Msg) bool {
	if r == nil {
		return false
	}
	tail := r.tail.Load()
	for {
		cell := &r.buf[tail&r.mask]
		seq := cell.seq.Load()
		if seq != tail {
			return false // full: consumer has not released the slot
		}
		if r.tail.CompareAndSwap(tail, tail+1) {
			break
		}
		tail = r.tail.Load()
	}
	cell := &r.buf[tail&r.mask]
	cell.msg = msg
	cell.seq.Store(tail + 1)
	r.count.Add(1)
	return true
}

// Pop removes and returns the next message. ok is false when the ring is
// empty.
func (r *streamRing) Pop() (tea.Msg, bool) {
	if r == nil {
		return nil, false
	}
	head := r.head.Load()
	for {
		cell := &r.buf[head&r.mask]
		seq := cell.seq.Load()
		if seq != head+1 {
			return nil, false // empty: producer has not filled the slot
		}
		if r.head.CompareAndSwap(head, head+1) {
			break
		}
		head = r.head.Load()
	}
	cell := &r.buf[head&r.mask]
	msg := cell.msg
	cell.msg = nil
	// Release the slot for reuse: the next producer wrap writes it at
	// sequence head+cap (one lap ahead of its reuse position).
	cell.seq.Store(head + r.mask + 1)
	r.count.Add(^uint64(0))
	return msg, true
}

// Len returns the current occupancy. It is a best-effort informational count
// for flow debugging; drains never rely on it.
func (r *streamRing) Len() int {
	if r == nil {
		return 0
	}
	return int(r.count.Load())
}
