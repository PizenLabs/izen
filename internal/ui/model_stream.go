package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	objengine "github.com/PizenLabs/izen/internal/engine"
)

// ── UI Token Pacer (frame-paced token buffer) ────────────────────────────────
//
// THE BOTTLENECK THIS SOLVES
//
// A high-throughput provider emits 100+ tokens/sec. Dispatching one tea.Msg per
// token forces Bubble Tea to run a full Update + View cycle per token, so the
// render cadence becomes a function of the MODEL's cadence: at 150 tok/s the
// renderer is asked for 150 frames/sec (more than the terminal can paint, so
// frames stutter and the layout visibly jitters), while at 5 tok/s the same
// path renders at 5 fps and the text crawls. The visual cadence is coupled to
// data arrival — the classic "the UI feels laggy but the CPU is idle" report.
//
// THE PACER
//
// TokenPacer decouples the two:
//
//	engine goroutine ──Push──▶ thread-safe token queue (streamRing)
//	                                        │
//	                          FrameTickMsg (30ms) ──Drain──▶ viewport
//
//   - PRODUCER: lock-free Push. A burst never blocks the engine worker, so the
//     provider SSE reader keeps draining its socket at full speed.
//   - CONSUMER: the frame ticker drains a bounded batch per tick. Rendering
//     therefore happens at a STEADY 30 FPS no matter whether 5 or 500 tokens
//     arrived in the interval.
//
// BATCH SIZING: a fixed batch would cap throughput (a 4000-token burst at 64
// tokens/frame needs 62 frames ≈ 2s of displayed lag), so Drain is adaptive: it
// releases the whole queue whenever it fits inside one frame's batch, and
// otherwise releases a geometrically-shrinking slice of it capped at
// BurstTokensPerFrame. Per-frame cost is therefore bounded (a frame always hits
// its deadline) and the backlog always empties (a burst never accumulates
// display lag). Correctness never depends on the batch: every terminal handler
// calls DrainAll, so no token can outlive its stream.

const (
	// FrameInterval is the default render cadence: 30ms ≈ 33 FPS. Fast enough
	// that streamed text reads as continuous, slow enough that the terminal is
	// never asked to paint more frames than it can display.
	FrameInterval = 30 * time.Millisecond

	// HighFrameInterval is the 60 FPS cadence (16ms) used when a low-latency
	// interaction demands it. It is a per-model setting, not a per-token one:
	// the pacer paces the FRAME, never the data.
	HighFrameInterval = 16 * time.Millisecond

	// TokensPerFrame is the steady-state number of tokens released per frame
	// tick. At 33 FPS this sustains ~2100 tok/s of display throughput, which is
	// an order of magnitude above the fastest provider stream while keeping a
	// single frame's ingest cost bounded.
	TokensPerFrame = 64

	// BurstTokensPerFrame is the hard per-frame ceiling on released tokens. It
	// bounds the cost of ONE Update: a frame that ingests an unbounded batch
	// misses its own deadline, which is the stutter the pacer exists to remove.
	// Total work is separately bounded by the geometric term in Drain, so a
	// queue of any size is worked off in O(log n) frames.
	BurstTokensPerFrame = 512

	// EventChannelBuffer is the engine→UI event channel depth. It is floored at
	// objengine.MinEventBuffer (256) so a slow render frame can never apply
	// backpressure to an engine worker.
	EventChannelBuffer = objengine.DefaultEventBuffer
)

// TokenPacer is the frame-paced token buffer: a thread-safe queue plus the
// batch policy that turns an arbitrary token arrival rate into a constant
// frame cadence. It is safe for concurrent use by the engine producer
// goroutine (Push) and the Bubble Tea Update goroutine (Drain/Pending).
type TokenPacer struct {
	ring     *streamRing
	interval time.Duration
	batch    int
	burst    int
}

// NewTokenPacer builds a pacer over a token ring. A nil ring allocates a fresh
// one at streamRingCapacity; a non-positive batch or burst selects the package
// defaults; a non-positive interval selects FrameInterval.
func NewTokenPacer(ring *streamRing, batch, burst int, interval time.Duration) *TokenPacer {
	if ring == nil {
		ring = newStreamRing(streamRingCapacity)
	}
	if batch <= 0 {
		batch = TokensPerFrame
	}
	if burst <= 0 {
		burst = BurstTokensPerFrame
	}
	if interval <= 0 {
		interval = FrameInterval
	}
	return &TokenPacer{ring: ring, interval: interval, batch: batch, burst: burst}
}

// Ring exposes the underlying lock-free token queue so a producer can Push
// directly and a test can inspect occupancy. It is never nil for a
// constructor-built pacer.
func (p *TokenPacer) Ring() *streamRing {
	if p == nil {
		return nil
	}
	return p.ring
}

// Interval returns the pacer's configured frame cadence. Inside the model loop
// the effective cadence is m.frameInterval(), which re-evaluates every tick so it
// can adapt to the live backlog.
func (p *TokenPacer) Interval() time.Duration {
	if p == nil {
		return FrameInterval
	}
	return p.interval
}

// Pending returns the number of tokens currently queued. It is informational
// (flow diagnostics, tests, footer throughput display) — Drain never relies on
// it to decide how much to release.
func (p *TokenPacer) Pending() int {
	if p == nil {
		return 0
	}
	return p.ring.Len()
}

// Backlogged reports whether the pacer is more than a frame behind: one frame's
// retire rate can no longer keep up with the arrival rate, so the frame loop
// must drop to the 60 FPS profile to shrink the displayed lag.
//
// Comparing the backlog against the BATCH size (not against 1) is the hysteresis
// that keeps a stream hovering at the boundary from oscillating between the two
// pacing profiles.
func (p *TokenPacer) Backlogged() bool {
	return p != nil && p.Pending() >= p.batch
}

// Push enqueues one token message. It is non-blocking and wait-free in the
// uncontended case. It reports false only when the queue is genuinely
// saturated, in which case the caller must fall back to the primary channel.
func (p *TokenPacer) Push(msg tea.Msg) bool {
	if p == nil {
		return false
	}
	return p.ring.Push(msg)
}

// Drain releases up to one frame's worth of queued tokens, oldest first.
//
// The policy bounds the per-frame cost of INGEST while guaranteeing the queue
// converges to empty, so a burst is never displayed with a growing delay:
//
//	pending <= batch  → release all (zero display latency)
//	otherwise         → release min(burst, max(batch, ceil(pending/2)))
//
// The two halves do different jobs. The `burst` cap bounds a SINGLE frame: a
// frame that ingests an unbounded batch misses its own deadline, so the pacer
// would be scheduling frames it cannot hit — exactly the stutter it exists to
// remove. The `ceil(pending/2)` term makes the backlog shrink geometrically once
// it is large, so the TOTAL ingest work for a queue of n tokens stays O(n)
// however big n is. Together: bounded per-frame cost, bounded total cost, and a
// backlog that always empties.
//
// The batch is a LATENCY bound only, never a correctness one: every terminal
// handler calls DrainAll, so a paced frame can never drop a token. Draining an
// empty queue returns nil.
func (p *TokenPacer) Drain() []tea.Msg {
	if p == nil {
		return nil
	}
	pending := p.ring.Len()
	if pending == 0 {
		return nil
	}

	limit := pending
	if pending > p.batch {
		limit = min(p.burst, max(p.batch, (pending+1)/2))
	}

	out := make([]tea.Msg, 0, min(limit, pending))
	for range limit {
		msg, ok := p.ring.Pop()
		if !ok {
			break
		}
		out = append(out, msg)
	}
	return out
}

// DrainAll releases every queued token regardless of batch policy. It is the
// terminal-handler drain: stream end, error, and interrupt teardown call it so
// no received token is ever left unrendered.
func (p *TokenPacer) DrainAll() []tea.Msg {
	if p == nil {
		return nil
	}
	var out []tea.Msg
	for {
		msg, ok := p.ring.Pop()
		if !ok {
			return out
		}
		out = append(out, msg)
	}
}

// Reset releases the queue reference. The pacer re-arms with a fresh ring on
// the next stream.
func (p *TokenPacer) Reset() {
	if p == nil {
		return
	}
	p.ring = nil
}

// Tick returns the re-armed frame-tick command at the pacer's configured
// cadence. Standard Bubble Tea discipline: every tick handler must re-arm
// through this (or the model's frameTickCmd) or the render loop dies.
func (p *TokenPacer) Tick() tea.Cmd {
	return frameTickCmdEvery(p.Interval())
}

// ── Model integration ─────────────────────────────────────────────────────────

// ensureTokenPacer lazily constructs the frame pacer and binds it to the
// model's current overflow ring. Binding on first use (rather than only at
// stream start) keeps the pacer and the ring a single source of truth even when
// a test or a harness installs a ring directly.
//
// The interval passed here is only the pacer's CONFIGURED default. The
// authoritative cadence is m.frameInterval(), re-evaluated on every tick so it
// can adapt to the live backlog; TokenPacer.Tick() is for standalone use outside
// the model loop.
func (m *model) ensureTokenPacer() *TokenPacer {
	if m.tokenPacer == nil {
		// Pass the model's ring straight in: constructing with a nil ring would
		// allocate a fresh one and strand any tokens already parked in the
		// model's ring.
		m.tokenPacer = NewTokenPacer(m.streamRing, 0, 0, m.frameInterval())
	}
	if m.tokenPacer.ring == nil {
		m.tokenPacer.ring = m.streamRing
	}
	return m.tokenPacer
}

// frameInterval returns the render cadence currently armed for this model.
//
// The cadence is ADAPTIVE, and the reason is throughput rather than taste: at
// 30 FPS each frame retires 33ms worth of arriving tokens. A stream that keeps
// the queue topped up is arriving faster than that, so the only way to shrink its
// displayed lag is to paint more often — the 60 FPS profile. A quiet stream gains
// nothing from a finer cadence (it already drains fully every frame), so it stays
// at 30 FPS and leaves terminal headroom for the rest of the UI.
//
// The transition is free: the frame tick re-arms on every tick, so the next tick
// is simply scheduled at the new interval. Comparing the backlog against the
// BATCH size (not against 1) is the hysteresis — a stream hovering around the
// threshold cannot oscillate between the two profiles.
func (m *model) frameInterval() time.Duration {
	if m.tokenPacer.Backlogged() {
		return HighFrameInterval
	}
	return FrameInterval
}

// frameTickCmd returns the model's frame tick at its armed cadence. Every
// re-arm site (FrameTickMsg handler, stream start, key interrupt) goes through
// it so a 60 FPS model never silently reverts to 30 FPS.
func (m *model) frameTickCmd() tea.Cmd {
	return frameTickCmdEvery(m.frameInterval())
}

// resetTokenPacer releases the frame pacer along with its ring. Every terminal
// path that nils m.streamRing must call it, so a finished stream can never
// leave a pacer holding a stale ring reference (which would re-emit the
// previous turn's queued tokens into the next one).
func (m *model) resetTokenPacer() {
	if m.tokenPacer == nil {
		return
	}
	m.tokenPacer.Reset()
	m.tokenPacer = nil
}

// ingestPacedTokens drains one frame's worth of paced tokens through the shared
// ingestion helpers. It is the single consumer of the pacer: the FrameTickMsg
// pass and the terminal handlers both land here, so a token is ingested exactly
// once no matter which path observes it first.
func (m *model) ingestPacedTokens(all bool) {
	pacer := m.ensureTokenPacer()
	if pacer == nil || pacer.ring == nil {
		return
	}
	var batch []tea.Msg
	if all {
		batch = pacer.DrainAll()
	} else {
		batch = pacer.Drain()
	}
	for _, msg := range batch {
		switch t := msg.(type) {
		case tokenMsg:
			m.ingestContentToken(SanitizeForIngest(string(t)))
		case thinkingTokenMsg:
			m.ingestThinkingToken(SanitizeForIngest(string(t)))
		case streamUsageMsg:
			m.ingestStreamUsage(t.input, t.output, t.reasoning)
		}
	}
}

// ── Event channel construction ───────────────────────────────────────────────

// flushStreamContentToView promotes everything the byte-level stream buffers
// have accumulated into the visible streaming tail, and reports whether it
// emitted anything.
//
// STREAM BUFFER CONTRACT (Option A — Cumulative Overwrite):
// ReadValidString returns the FULL accumulated string from the start of the
// stream, never a drained delta, so this MUST NOT do
// `m.currentStreamContent += full` (that duplicates bytes already rendered). It
// overwrites the view from the full text by emitting ONLY the unread suffix
// beyond currentStreamContent. The HasPrefix guard handles a mid-stream reset
// (the sanitizer rewrote earlier bytes, or the content shortened): there is then
// no safe delta, so nothing is emitted rather than corrupting the record.
//
// It lives here — not inline in the frame handler — because TWO paths must
// promote the buffer with identical semantics: the frame tick (the steady path)
// and the interrupt teardown (which must surface the bytes the provider had
// already delivered before the cancel). Duplicating the logic is how one path
// ends up emitting bytes the other drops.
func (m *model) flushStreamContentToView() bool {
	if m.utf8StreamBuf != nil {
		content, updated := m.utf8StreamBuf.ReadValidString()
		if !updated || content == "" {
			return false
		}
		switch {
		case strings.HasPrefix(content, m.currentStreamContent):
			if delta := content[len(m.currentStreamContent):]; delta != "" {
				m.emitVisibleContent(delta)
				return true
			}
		case m.currentStreamContent == "":
			m.emitVisibleContent(content)
			return true
		}
		return false
	}
	if m.streamThrottle != nil {
		if content, ok := m.streamThrottle.Flush(); ok && content != "" {
			m.emitVisibleContent(content)
			return true
		}
	}
	return false
}

// newEventChannel builds the primary engine→UI message channel. The depth is
// floored at objengine.MinEventBuffer so a mid-frame render stall can never
// apply backpressure to an engine worker; the caller may request a larger depth
// but never a smaller one.
func newEventChannel(depth int) chan tea.Msg {
	if depth < objengine.MinEventBuffer {
		depth = EventChannelBuffer
	}
	return make(chan tea.Msg, depth)
}
