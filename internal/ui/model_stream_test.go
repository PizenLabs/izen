package ui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
)

// TestTokenPacerStabilisesRenderCadence is the core pacing contract: however
// many tokens arrive in a frame, the pacer releases a BOUNDED number, so the
// render loop's per-frame cost stays constant and the visual cadence never
// mirrors the provider's token rate. Unbounded draining (the previous
// behaviour) meant one frame per burst, which is the "the UI stutters at 150
// tok/s" report.
func TestTokenPacerStabilisesRenderCadence(t *testing.T) {
	p := NewTokenPacer(newStreamRing(4096), 0, 0, FrameInterval)
	for i := range 500 {
		if !p.Push(tokenMsg(fmt.Sprintf("%d", i))) {
			t.Fatalf("push %d rejected below capacity", i)
		}
	}
	if got := p.Pending(); got != 500 {
		t.Fatalf("Pending() = %d, want 500", got)
	}

	first := p.Drain()
	if len(first) > BurstTokensPerFrame {
		t.Fatalf("one frame released %d tokens, want <= BurstTokensPerFrame (%d)", len(first), BurstTokensPerFrame)
	}
	// The queue must be strictly decreasing and strictly ordered: a pacer that
	// reorders or re-emits is worse than one that renders too fast.
	for i, msg := range first {
		if want := tokenMsg(fmt.Sprintf("%d", i)); msg != want {
			t.Fatalf("frame[%d] = %q, want %q (order must be FIFO)", i, msg, want)
		}
	}
	if p.Pending() >= 500 {
		t.Fatalf("frame released nothing: Pending() = %d", p.Pending())
	}
}

// TestTokenPacerConvergesToEmpty proves the pacer is not merely throttling: a
// backlog must be worked off, never displayed with a growing delay. At 33 FPS
// with the burst rate, a 4000-token burst is fully displayed in well under a
// second.
func TestTokenPacerConvergesToEmpty(t *testing.T) {
	p := NewTokenPacer(newStreamRing(4096), 0, 0, FrameInterval)
	const n = 4000
	for i := range n {
		p.Push(tokenMsg(fmt.Sprintf("%d", i)))
	}
	frames := 0
	for p.Pending() > 0 {
		frames++
		if frames > 1000 {
			t.Fatalf("pacer failed to converge: %d tokens still queued after %d frames", p.Pending(), frames)
		}
		p.Drain()
	}
	if frames >= 20 {
		t.Errorf("a %d-token burst took %d frames; it must be drained in a handful, not displayed with a lag", n, frames)
	}
}

// TestTokenPacerBoundsPerFrameCostAndConverges pins the pacing policy: no single
// frame may do unbounded work (a frame that misses its deadline defeats the
// point of pacing), yet the queue must always converge to empty so a burst is
// never displayed with a growing delay. The geometric halving satisfies both:
// per-frame work is capped at BurstTokensPerFrame, and a 4000-token burst is
// fully displayed in a handful of frames.
func TestTokenPacerBoundsPerFrameCostAndConverges(t *testing.T) {
	p := NewTokenPacer(newStreamRing(streamRingCapacity), 0, 0, FrameInterval)
	const n = 4000
	for i := range n {
		p.Push(tokenMsg(fmt.Sprintf("%d", i)))
	}

	released, frames := 0, 0
	for p.Pending() > 0 {
		batch := p.Drain()
		frames++
		if len(batch) > BurstTokensPerFrame {
			t.Fatalf("frame %d released %d tokens, want <= %d (per-frame cost is unbounded)",
				frames, len(batch), BurstTokensPerFrame)
		}
		released += len(batch)
		if frames > 40 {
			t.Fatalf("no convergence: %d tokens left after %d frames", p.Pending(), frames)
		}
	}
	if released != n {
		t.Fatalf("frames released %d of %d tokens", released, n)
	}
	// Geometric halving: a 4000-token burst is worked off in O(log n) frames,
	// never one token per frame.
	if frames > 16 {
		t.Errorf("a %d-token burst took %d frames, want <= 16", n, frames)
	}
}

// TestTokenPacerIsTransparentBelowTheBatchSize is the zero-latency guarantee: a
// stream that fits inside one frame's batch is displayed IMMEDIATELY, never held
// over to the next tick. This is the regime every normal stream runs in.
func TestTokenPacerIsTransparentBelowTheBatchSize(t *testing.T) {
	p := NewTokenPacer(newStreamRing(1024), 0, 0, FrameInterval)
	for i := range TokensPerFrame {
		p.Push(tokenMsg(fmt.Sprintf("%d", i)))
	}
	if got := len(p.Drain()); got != TokensPerFrame {
		t.Errorf("a queue of exactly one frame's batch released %d, want %d (no token may wait for the next tick)",
			got, TokensPerFrame)
	}
	if p.Drain() != nil {
		t.Error("draining an emptied queue must return nil")
	}
}

// TestTokenPacerDrainAllIsExhaustive is the no-token-left-behind guarantee for
// the terminal handlers: the batch policy is a latency bound, never a drop.
func TestTokenPacerDrainAllIsExhaustive(t *testing.T) {
	p := NewTokenPacer(newStreamRing(4096), 0, 0, FrameInterval)
	const n = 1500
	for i := range n {
		p.Push(tokenMsg(fmt.Sprintf("%d", i)))
	}
	got := p.DrainAll()
	if len(got) != n {
		t.Fatalf("DrainAll released %d of %d", len(got), n)
	}
	for i, msg := range got {
		if want := tokenMsg(fmt.Sprintf("%d", i)); msg != want {
			t.Fatalf("DrainAll[%d] = %q, want %q", i, msg, want)
		}
	}
	if p.Pending() != 0 {
		t.Errorf("Pending() = %d after DrainAll", p.Pending())
	}
	if again := p.DrainAll(); again != nil {
		t.Errorf("DrainAll on an empty queue = %v, want nil", again)
	}
}

// TestTokenPacerRejectsWhenSaturated proves the producer gets an honest signal
// (so it can fall back to the primary channel) instead of blocking or silently
// overwriting a token.
func TestTokenPacerRejectsWhenSaturated(t *testing.T) {
	ring := newStreamRing(4)
	p := NewTokenPacer(ring, 0, 0, FrameInterval)
	for i := range 4 {
		if !p.Push(tokenMsg("x")) {
			t.Fatalf("push %d rejected below capacity", i)
		}
	}
	if p.Push(tokenMsg("overflow")) {
		t.Fatal("Push must report false when the queue is saturated")
	}
	if p.Pending() != 4 {
		t.Errorf("a rejected push changed the queue: Pending() = %d", p.Pending())
	}
	for range 4 {
		p.DrainAll()
	}
	if !p.Push(tokenMsg("after-drain")) {
		t.Error("Push must succeed again once slots are consumed")
	}
}

// TestTokenPacerConcurrentProducerAndFrameDrain is the -race drill: an engine
// worker pushes at 100+ tok/s while the Bubble Tea goroutine drains a frame at
// a time. Nothing is lost, nothing is duplicated, order is FIFO.
func TestTokenPacerConcurrentProducerAndFrameDrain(t *testing.T) {
	p := NewTokenPacer(newStreamRing(4096), 0, 0, FrameInterval)
	const n = 5000

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range n {
			for !p.Push(tokenMsg(fmt.Sprintf("%d", i))) {
				// A real producer would fall back to the primary channel here;
				// spinning keeps the test focused on the queue.
			}
		}
	}()

	seen := 0
	for seen < n {
		batch := p.Drain()
		for _, msg := range batch {
			if want := tokenMsg(fmt.Sprintf("%d", seen)); msg != want {
				t.Fatalf("order break at %d: got %q want %q", seen, msg, want)
			}
			seen++
		}
		if len(batch) == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	wg.Wait()
	if seen != n {
		t.Fatalf("lost %d of %d tokens", n-seen, n)
	}
}

// TestTokenPacerNilSafe keeps harnesses that never stream panic-free.
func TestTokenPacerNilSafe(t *testing.T) {
	var p *TokenPacer
	if p.Push(tokenMsg("x")) {
		t.Error("nil pacer Push must report false")
	}
	if p.Drain() != nil || p.DrainAll() != nil || p.Pending() != 0 {
		t.Error("nil pacer drain/Pending must degrade to a no-op")
	}
	if p.Interval() != FrameInterval {
		t.Errorf("nil pacer Interval() = %v, want the default %v", p.Interval(), FrameInterval)
	}
	if p.Ring() != nil {
		t.Error("nil pacer Ring() must be nil")
	}
	p.Reset()
}

// TestTokenPacerResetReleasesQueue guards stream teardown: a finished stream
// must never leave queued tokens behind to surface in the next turn.
func TestTokenPacerResetReleasesQueue(t *testing.T) {
	p := NewTokenPacer(newStreamRing(64), 0, 0, FrameInterval)
	p.Push(tokenMsg("stale"))
	p.Reset()
	if p.Ring() != nil {
		t.Error("Reset must release the ring reference")
	}
	if got := p.DrainAll(); got != nil {
		t.Errorf("Reset must discard queued tokens, got %v", got)
	}
}

// TestTokenPacerDefaultsAreApplied pins the constructor's normalisation: a
// non-positive batch, burst, or interval must select the documented package
// defaults rather than a zero-length drain or a busy loop.
func TestTokenPacerDefaultsAreApplied(t *testing.T) {
	p := NewTokenPacer(nil, 0, 0, 0)
	if p.Interval() != FrameInterval {
		t.Errorf("Interval() = %v, want %v", p.Interval(), FrameInterval)
	}
	if p.batch != TokensPerFrame {
		t.Errorf("batch = %d, want %d", p.batch, TokensPerFrame)
	}
	if p.burst != BurstTokensPerFrame {
		t.Errorf("burst = %d, want %d", p.burst, BurstTokensPerFrame)
	}
	if p.Ring() == nil {
		t.Fatal("a nil ring must be allocated, never left nil")
	}
	if p.Tick() == nil {
		t.Error("Tick must return a live command")
	}
}

// TestPacerBindsToTheModelRing guards the one real footgun in the integration:
// constructing the pacer with a nil ring allocates a fresh queue, which would
// strand every token already parked in the model's ring.
func TestPacerBindsToTheModelRing(t *testing.T) {
	m := newTestModel()
	m.streamRing = newStreamRing(256)
	m.streamRing.Push(tokenMsg("parked"))

	pacer := m.ensureTokenPacer()
	if pacer.Ring() != m.streamRing {
		t.Fatal("ensureTokenPacer must adopt the model's existing ring, not allocate a new one")
	}
	m.drainStreamRing()
	if m.streamRing.Len() != 0 {
		t.Fatalf("drainStreamRing left %d tokens stranded in the model's ring", m.streamRing.Len())
	}
	// The token reaches the byte-level stream buffer; promoting it to visible
	// content is the frame tick's job (see TestFrameTickDrainsPacedTokensOnly).
	if m.streamBuffer != "parked" {
		t.Errorf("stranded token never ingested: streamBuffer = %q", m.streamBuffer)
	}
}

// TestFrameCadenceIsAdaptive pins the 30/60 FPS seam. The cadence is chosen by
// throughput, not configuration: a stream that keeps the token queue topped up is
// arriving faster than 30 FPS can display, so the frame loop drops to the 16ms
// profile to shrink its displayed lag; a quiet stream stays at 30ms and leaves the
// terminal headroom. The shared FrameTickMsg type keeps every consumer
// single-shaped across the switch.
func TestFrameCadenceIsAdaptive(t *testing.T) {
	if FrameInterval != 30*time.Millisecond {
		t.Errorf("FrameInterval = %v, want 30ms (~33 FPS)", FrameInterval)
	}
	if HighFrameInterval != 16*time.Millisecond {
		t.Errorf("HighFrameInterval = %v, want 16ms (~60 FPS)", HighFrameInterval)
	}

	// No pacer yet (pre-stream): the steady-state cadence.
	m := newTestModel()
	if got := m.frameInterval(); got != FrameInterval {
		t.Errorf("idle frameInterval() = %v, want %v", got, FrameInterval)
	}
	if m.frameTickCmd() == nil {
		t.Error("frameTickCmd must return a live command")
	}

	// A quiet stream: fewer queued tokens than one frame's batch.
	m.streamRing = newStreamRing(1024)
	m.tokenPacer = NewTokenPacer(m.streamRing, 0, 0, FrameInterval)
	m.tokenPacer.Push(tokenMsg("one"))
	if m.tokenPacer.Backlogged() {
		t.Error("a one-token queue is not backlogged")
	}
	if got := m.frameInterval(); got != FrameInterval {
		t.Errorf("quiet stream frameInterval() = %v, want %v", got, FrameInterval)
	}

	// A saturated stream: the queue is at least one frame's batch deep.
	for range TokensPerFrame {
		m.tokenPacer.Push(tokenMsg("x"))
	}
	if !m.tokenPacer.Backlogged() {
		t.Error("a queue at one frame's batch must report backlogged")
	}
	if got := m.frameInterval(); got != HighFrameInterval {
		t.Errorf("saturated stream frameInterval() = %v, want %v", got, HighFrameInterval)
	}

	// A non-positive interval falls back to the default rather than busy-looping.
	if frameTickCmdEvery(0) == nil {
		t.Error("frameTickCmdEvery(0) must still return a live command")
	}
	if frameTickCmdEvery(-time.Second) == nil {
		t.Error("frameTickCmdEvery(negative) must still return a live command")
	}
}

// TestInterruptDrainsThePacedQueue is the no-token-left-behind guarantee on the
// Ctrl+C path. The interrupt teardown releases the ring (via reconcileSpinner)
// and abandons the producer's terminal message, so anything still parked in the
// queue at the instant the user cancels would otherwise be discarded even though
// the provider had already delivered those bytes. The partial answer the user
// cancelled after must survive.
func TestInterruptDrainsThePacedQueue(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamCh = newEventChannel(EventChannelBuffer)
	m.streamRing = newStreamRing(streamRingCapacity)
	m.tokenPacer = NewTokenPacer(m.streamRing, 0, 0, FrameInterval)
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamThrottle = NewStreamThrottle()

	for range 3 {
		m.streamRing.Push(tokenMsg("delivered-"))
	}
	if m.streamRing.Len() != 3 {
		t.Fatalf("precondition: 3 tokens parked, got %d", m.streamRing.Len())
	}

	um, _ := m.handleEmergencyInterrupt("ctrl-c test")
	m2 := um.(*model)

	if m2.streamRing != nil {
		t.Error("the interrupt teardown must still release the ring")
	}
	if got := strings.Count(m2.currentStreamContent, "delivered-"); got != 3 {
		t.Fatalf("interrupt surfaced %d of 3 delivered tokens: currentStreamContent = %q",
			got, m2.currentStreamContent)
	}
}

// TestInterruptWithAnEmptyQueueIsInert guards the degenerate case: the drain must
// be a no-op when nothing is parked, so an interrupt with no active stream never
// emits a stray empty delta.
func TestInterruptWithAnEmptyQueueIsInert(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = false
	m.streamCh = newEventChannel(EventChannelBuffer)
	m.streamRing = newStreamRing(streamRingCapacity)
	m.tokenPacer = NewTokenPacer(m.streamRing, 0, 0, FrameInterval)
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamThrottle = NewStreamThrottle()
	before := m.currentStreamContent

	um, _ := m.handleEmergencyInterrupt("ctrl-c test")
	m2 := um.(*model)

	if m2.currentStreamContent != before {
		t.Errorf("an idle interrupt changed visible content: %q -> %q", before, m2.currentStreamContent)
	}
}

// TestEventChannelDepthIsFloored pins the engine→UI buffer contract: no call
// site can construct a shallower channel than MinEventBuffer, so a mid-frame
// render stall can never apply backpressure to an engine worker.
func TestEventChannelDepthIsFloored(t *testing.T) {
	for _, requested := range []int{-1, 0, 1, 8, 64, 255} {
		if got := cap(newEventChannel(requested)); got < 256 {
			t.Errorf("newEventChannel(%d) depth = %d, want >= 256", requested, got)
		}
	}
	if got := cap(newEventChannel(4096)); got != 4096 {
		t.Errorf("newEventChannel(4096) depth = %d, want 4096 (an explicit larger request is honoured)", got)
	}
	if got := cap(newEventChannel(EventChannelBuffer)); got != EventChannelBuffer {
		t.Errorf("production depth = %d, want %d", got, EventChannelBuffer)
	}
}

// TestEventChannelAbsorbsBurstWithoutBlocking is the responsiveness drill: with
// the UI not draining at all, a full 100+ tokens/sec burst lands in the channel
// without the producer ever parking.
func TestEventChannelAbsorbsBurstWithoutBlocking(t *testing.T) {
	ch := newEventChannel(EventChannelBuffer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range cap(ch) {
			ch <- tokenMsg(fmt.Sprintf("%d", i))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a burst that fits the buffer must never block the producer")
	}
	if len(ch) != cap(ch) {
		t.Errorf("buffered %d of %d", len(ch), cap(ch))
	}
}

// TestFrameTickDrainsPacedTokensOnly drives the real Update loop: tokens parked
// in the ring reach currentStreamContent through the frame tick, and the tick
// re-arms so the loop cannot die.
func TestFrameTickDrainsPacedTokensOnly(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamCh = newEventChannel(EventChannelBuffer)
	m.streamRing = newStreamRing(streamRingCapacity)
	m.tokenPacer = NewTokenPacer(m.streamRing, 0, 0, FrameInterval)
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamThrottle = NewStreamThrottle()

	m.streamRing.Push(tokenMsg("paced-"))
	if _, cmd := m.Update(FrameTickMsg{}); cmd == nil {
		t.Fatal("the frame tick must re-arm while a stream is live")
	}
	if got := m.currentStreamContent; got != "paced-" {
		t.Fatalf("frame tick did not drain the paced token: %q", got)
	}
}
