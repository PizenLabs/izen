package ui

import (
	"sync"
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
// The step is not one constant; it is a two-regime function of the backlog:
//
//	pending < 60   →  1..3 runes per frame (a natural, sequential cadence)
//	pending >= 60  →  min(12, pending/5)     (stream-lag insurance)
//
// The reason it is measured in RUNES rather than bytes is that a "step" the
// user perceives is a character, not a byte: revealing 12 bytes of a CJK answer
// is 4 glyphs, and revealing 12 bytes of ASCII is 12 glyphs, so a byte-denominated
// step animates at two different speeds depending on the language of the answer.
//
// The first regime is the one that carries the EFFECT. A fixed slice of 1..3
// makes a short backlog arrive as a sequence — one character, then another, then
// another — which is what a typewriter is. Draining a fixed FRACTION instead
// (the old max(1, pending/3)) reveals 2 of 6, then 2 of 4, then all 2: the
// second frame already shows half the burst, and the eye reads a jump rather than
// a typing. Bounding the step at three also bounds the latency the reveal adds
// to the answer, so the pacer can never become the reason a turn feels slow.
//
// The second regime is insurance, not choreography. A burst that deep is not
// being read — it is a provider dumping a paragraph, and holding all of it back
// to animate it character by character would make the pacer lag the stream by
// seconds. pending/5 catches the backlog up geometrically (converging in a
// handful of frames) and the cap of 12 keeps a single frame from revealing a
// whole screenful at once, which is the artefact this file exists to remove.
//
// Nothing is ever dropped — the terminal drain moves the remaining queue
// wholesale, and pop() never splits a UTF-8 rune, so a CJK glyph or an emoji is
// painted complete rather than as a replacement character for one frame.
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

// typewriterQueue is the thread-safe FIFO of not-yet-revealed visible runes.
//
// The producer side runs on the UI goroutine today, but the queue is
// mutex-guarded anyway: the whole point of a dedicated FIFO is that a future
// producer can append from its own goroutine without the render path having to
// care, and a "safe because it is only called from here" buffer is a comment
// that rots the moment a second caller appears.
//
// It is rune-denominated rather than byte-denominated, and that is not
// bookkeeping — it is what makes the reveal cadence a property of the TEXT
// instead of a property of its encoding. A byte queue cannot be sliced to "three
// characters" without either re-scanning for boundaries (a rune walk per frame)
// or splitting one, so either the step is approximate or a CJK glyph is painted
// as a replacement character for a frame. A rune queue makes both impossible by
// construction.
type typewriterQueue struct {
	mu      sync.Mutex
	pending []rune
}

// push appends visible content to the tail of the queue.
//
// The append is rune-by-rune rather than a []rune(s) conversion so the producer
// path allocates nothing once the buffer has grown: a conversion would allocate
// a fresh slice per pushed batch, and this sits behind the token handler.
func (q *typewriterQueue) push(s string) {
	if q == nil || s == "" {
		return
	}
	q.mu.Lock()
	for _, r := range s {
		q.pending = append(q.pending, r)
	}
	q.mu.Unlock()
}

// pendingRunes reports how many characters are waiting to be revealed.
func (q *typewriterQueue) pendingRunes() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// ── THE STEP FUNCTION ─────────────────────────────────────────────────────────

// The two regimes of typewriterStep, as named constants. They are the whole
// tunability surface of the reveal, and they are named rather than inlined in
// the function because both are asserted directly by the pacer's tests: a
// literal in a comparison is a literal a test cannot pin.
const (
	// typewriterMinStep is the floor: a mounted queue always moves, or the
	// animation stops dead at whatever depth it happened to be at.
	typewriterMinStep = 1
	// typewriterMaxCadenceStep is the ceiling of the SEQUENTIAL regime. Three
	// characters a frame at 30 FPS is ~90 characters a second — fast enough to
	// feel live, slow enough that each character is a separate event rather than
	// part of a smear.
	typewriterMaxCadenceStep = 3
	// typewriterBurstThreshold is the backlog depth at which the reveal stops
	// being a cadence and becomes catch-up. Sixty pending characters is roughly
	// two full lines of prose: past that the user is not reading character by
	// character, they are waiting for the paragraph.
	typewriterBurstThreshold = 60
	// typewriterBurstDivisor scales the catch-up step, and
	// typewriterMaxBurstStep caps it. pending/5 converges geometrically (the
	// backlog shrinks to a fifth each frame) so a huge burst is gone in a
	// handful of frames instead of a second of stalling; the cap keeps any
	// single frame from revealing a whole screenful, which is the jump this
	// file exists to remove.
	typewriterBurstDivisor = 5
	typewriterMaxBurstStep = 12
)

// typewriterStep returns how many RUNES this frame should reveal for a queue of
// the given depth.
//
// Runes, not bytes: a step is what the reader perceives as motion, and a
// byte-denominated step reveals four times as many glyphs for a CJK answer as
// for an ASCII one at the same step size. Draining by rune makes the cadence
// identical in both.
//
// It always returns at least typewriterMinStep for a non-empty queue and never
// more than the queue holds, so a caller can pass the result straight to pop
// and Advance cannot stall on a backlog it declined to move.
func typewriterStep(pending int) int {
	if pending <= 0 {
		return 0
	}
	var step int
	if pending >= typewriterBurstThreshold {
		// Geometric catch-up, hard-capped. The cap is what keeps this regime
		// from becoming the artefact: without it a 900-character burst reveals
		// 180 characters in one frame, which is a page flip, not a reveal.
		step = min(pending/typewriterBurstDivisor, typewriterMaxBurstStep)
	} else {
		// Sequential cadence: ramp by depth so a full 60-character backlog
		// still drains at the top of the range rather than crawling at one
		// character per frame for two seconds.
		step = pending * typewriterMaxCadenceStep / typewriterBurstThreshold
	}
	// One clamp for both regimes rather than one per branch: the floor is the
	// property that matters (a mounted queue always moves, or the animation
	// stops dead at whatever depth it happened to be at) and the burst branch can
	// produce a zero for a backlog just over the threshold only on an
	// integer-division edge, which the floor then covers.
	if step < typewriterMinStep {
		step = typewriterMinStep
	}
	if step > typewriterMaxCadenceStep && pending < typewriterBurstThreshold {
		step = typewriterMaxCadenceStep
	}
	if step > pending {
		step = pending
	}
	return step
}

// pop removes and returns up to n RUNES from the head of the queue.
//
// It cannot split a UTF-8 rune, and it does not need to try: the queue holds
// runes, so index n is a character boundary by construction. That is the whole
// reason the buffer is rune-denominated — the old byte implementation had to
// walk forward to the next RuneStart on every pop, which made the step
// approximate and a CJK glyph briefly visible as a replacement character.
func (q *typewriterQueue) pop(n int) []rune {
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
	out := q.pending[:n]
	q.pending = q.pending[n:]
	return out
}

// typewriterPacer is the character-level reveal state for one answer stream:
// the queue of pending runes plus the prefix already revealed to the renderer.
//
// `revealed` is kept as a growing buffer rather than as an index into
// `currentStreamContent` because the renderer's source string is sanitized and
// delimiter-adjusted downstream; what it needs is exactly the characters that
// have been shown, in order.
type typewriterPacer struct {
	queue typewriterQueue

	mu       sync.Mutex
	revealed []rune
}

// newTypewriterPacer returns an empty pacer ready for one answer stream.
func newTypewriterPacer() *typewriterPacer {
	return &typewriterPacer{}
}

// Push queues visible content for a future frame. It is the only producer entry
// point and is O(runes appended).
func (p *typewriterPacer) Push(s string) {
	if p == nil {
		return
	}
	p.queue.push(s)
}

// Pending reports how many characters are queued but not yet revealed.
func (p *typewriterPacer) Pending() int {
	if p == nil {
		return 0
	}
	return p.queue.pendingRunes()
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

// Advance reveals one adaptive step of queued runes and reports whether
// anything moved. The revealed runes are appended to the prefix the renderer
// reads; the boolean is what the frame tick folds into its repaint decision.
func (p *typewriterPacer) Advance() bool {
	if p == nil {
		return false
	}
	step := typewriterStep(p.queue.pendingRunes())
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

// advanceTypewriter reveals one frame's worth of queued answer characters and
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
