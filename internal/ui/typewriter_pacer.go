package ui

import (
	"sync"
	"unicode/utf8"
)

// ── ADAPTIVE TYPEWRITER ANIMATION PACER ───────────────────────────────────────
//
// # THE DEFECT THIS REMOVES
//
// The frame loop already decouples RENDER cadence from ARRIVAL rate: the
// provider appends deltas to the stream accumulator and the FrameTickMsg handler
// promotes one batch into the visible stream. That is why a 138 tok/s burst no
// longer asks the terminal for 138 frames a second.
//
// What it does not do is make the REVEAL smooth. A TCP packet that delivers
// fourteen tokens (~56 characters) in one 100ms window is promoted into
// `currentStreamContent` whole, and the very next frame paints all fifty-six
// characters at once. At 30 FPS that reads as a chunky jump every few frames
// rather than as text being typed, and on a bursty provider it is the dominant
// visual artefact that remains.
//
// # THE SHAPE
//
// The typewriter pacer is a second, character-level stage on top of the frame
// pacer. Visible content is APPENDED here rather than painted, and each frame
// tick releases a small, adaptively-sized slice of it:
//
//	emitVisibleContent ──Push──▶ typewriterQueue (thread-safe)
//	                                      │
//	                            FrameTickMsg ──Advance──▶ render
//
// The step is `max(1, pending/3)`: a third of the backlog per frame. That is
// geometric, so a large burst is caught up in a handful of frames and always
// converges to empty, while a slow stream reveals character by character and is
// indistinguishable from the arrival itself. Nothing is ever dropped — the
// terminal drains move the remaining queue wholesale.
//
// # WHAT IS AUTHORITATIVE
//
// This layer is a RENDER-TIME effect, never a data one. `m.currentStreamContent`
// still receives every byte the instant it is emitted, and the committed record
// is built from it verbatim. Only the still-streaming tail renderer reads the
// revealed prefix, and it reads it through the same transient AST balancer as
// before, so an unclosed `**` is styled on the first revealed frame instead of
// flashing its marker. A paused reveal can therefore delay the pixels and never
// the answer.
//
// # WHY IT IS NIL BY DEFAULT
//
// The pacer is constructed at stream start and released at a turn boundary. A
// model that sets `streaming = true` directly (every headless harness in this
// package) leaves it nil and renders exactly as it always did, so the smoothing
// is an additive production layer rather than a change to the tested contract.

// typewriterQueue is the thread-safe FIFO of not-yet-revealed visible bytes.
//
// The producer side runs on the UI goroutine today, but the queue is
// mutex-guarded anyway: the whole point of a dedicated FIFO is that a future
// producer can append from its own goroutine without the render path having to
// care, and a "safe because it is only called from here" buffer is a comment
// that rots the moment a second caller appears.
type typewriterQueue struct {
	mu      sync.Mutex
	pending []byte
}

// push appends visible content to the tail of the queue.
func (q *typewriterQueue) push(s string) {
	if q == nil || s == "" {
		return
	}
	q.mu.Lock()
	q.pending = append(q.pending, s...)
	q.mu.Unlock()
}

// len reports how many bytes are waiting to be revealed.
func (q *typewriterQueue) len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// step returns the number of bytes this frame should reveal for a queue of the
// given depth: `max(1, pending/3)`.
//
// A third is deliberately aggressive. A gentler fraction would leave a visible
// trail on every burst (the reveal would lag the answer by more than a frame or
// two), while revealing everything is the jump this file exists to remove. One
// third reaches the tail within O(log n) frames and, for the steady state — a
// few characters arriving per frame — releases them in one, so a slow stream is
// not held back at all.
func typewriterStep(pending int) int {
	if pending <= 0 {
		return 0
	}
	step := pending / 3
	if step < 1 {
		step = 1
	}
	if step > pending {
		step = pending
	}
	return step
}

// pop removes and returns up to n bytes from the head of the queue. It never
// splits a UTF-8 rune: if the cut would land inside a multi-byte sequence the
// slice is extended to include the whole rune, so a CJK glyph or an emoji is
// painted complete rather than as a replacement character for one frame.
func (q *typewriterQueue) pop(n int) []byte {
	if q == nil || n <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	if n > len(q.pending) {
		n = len(q.pending)
	}
	for n < len(q.pending) && !utf8.RuneStart(q.pending[n]) {
		n++
	}
	out := q.pending[:n]
	q.pending = q.pending[n:]
	return out
}

// typewriterPacer is the character-level reveal state for one answer stream:
// the queue of pending bytes plus the prefix already revealed to the renderer.
//
// `revealed` is kept as a growing buffer rather than as an index into
// `currentStreamContent` because the renderer's source string is sanitized and
// delimiter-adjusted downstream; what it needs is exactly the bytes that have
// been shown, in order.
type typewriterPacer struct {
	queue typewriterQueue

	mu       sync.Mutex
	revealed []byte
}

// newTypewriterPacer returns an empty pacer ready for one answer stream.
func newTypewriterPacer() *typewriterPacer {
	return &typewriterPacer{}
}

// Push queues visible bytes for a future frame. It is the only producer entry
// point and is O(bytes appended).
func (p *typewriterPacer) Push(s string) {
	if p == nil {
		return
	}
	p.queue.push(s)
}

// Pending reports how many bytes are queued but not yet revealed.
func (p *typewriterPacer) Pending() int {
	if p == nil {
		return 0
	}
	return p.queue.len()
}

// Revealed returns the prefix of the stream that should be rendered on this
// frame. It is what the streaming tail renderer parses.
func (p *typewriterPacer) Revealed() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.revealed)
}

// Advance reveals one adaptive step of queued bytes and reports whether
// anything moved. The revealed bytes are appended to the prefix the renderer
// reads; the boolean is what the frame tick folds into its repaint decision.
func (p *typewriterPacer) Advance() bool {
	if p == nil {
		return false
	}
	step := typewriterStep(p.queue.len())
	if step == 0 {
		return false
	}
	chunk := p.queue.pop(step)
	if len(chunk) == 0 {
		return false
	}
	p.mu.Lock()
	p.revealed = append(p.revealed, chunk...)
	p.mu.Unlock()
	return true
}

// ── Model integration ─────────────────────────────────────────────────────────

// resetTypewriter drops the pacer at a turn boundary so a finished stream can
// never leave its revealed prefix (or its backlog) for the next turn's render.
func (m *model) resetTypewriter() {
	if m == nil {
		return
	}
	m.typewriter = nil
}

// advanceTypewriter reveals one frame's worth of queued answer bytes and
// re-syncs the streaming tail. It reports whether the reveal moved, which the
// frame tick folds into its "did anything change" answer so the single-flight
// repaint gate arms a frame for the newly revealed characters.
//
// It is a no-op unless a pacer exists AND a stream is live: outside a stream the
// tail renderer is not in play, and advancing would only burn a lock.
func (m *model) advanceTypewriter() bool {
	if m == nil || m.typewriter == nil || !m.streaming {
		return false
	}
	if !m.typewriter.Advance() {
		return false
	}
	if m.docLayout != nil {
		m.syncStreamingSegment()
	}
	return true
}
