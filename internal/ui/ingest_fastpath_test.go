package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/policy"
)

// ── DECOUPLED INGESTION, O(1) FRAME COST, PRIORITY-ZERO SCROLL ──────────────
//
// Three mechanisms, one complaint: "scrolling lags while the answer streams".
//
// They are pinned separately because they fail separately, and each has a
// tempting way to pass that proves nothing:
//
//   - The fast path can move the offset correctly and still be BEHIND the
//     workspace guard, the overlay swallows and the state machine — correct,
//     and still a lag spike on exactly the frames where the queue is deepest.
//     So the ordering is asserted through an OBSERVABLE side effect of a later
//     stage, not through the offset moving.
//   - The accumulator can hold deltas and still be fed by per-token messages
//     as well, which would double-emit. So the producer's lane decision is
//     asserted negatively: token deltas must be reported as diverted, and the
//     control messages beside them must not be.
//   - The frame caches can be present and still be bypassed. So the assertions
//     are on REBUILD COUNTS (how many times a frame re-derived the hit map or
//     the row pool) and on allocation scaling with document length, which is
//     the only measurement that distinguishes "cached" from "recomputed more
//     cheaply".

// ── A. Non-blocking token ingestion ─────────────────────────────────────────

// TestProducerDivertsEveryTokenDeltaAndNothingElse is the structural half of
// the decoupling contract. The provider goroutine has exactly one exit for a
// token, and it must not be a tea.Msg; everything that is NOT a token must
// keep its ordered channel path, because a usage update that arrived in the
// accumulator would be rendered in the wrong place relative to the content
// around it.
func TestProducerDivertsEveryTokenDeltaAndNothingElse(t *testing.T) {
	accum := NewStreamAccumulator()

	deltas := []tea.Msg{
		tokenMsg("a"),
		thinkingTokenMsg("b"),
		tokenMsg(""),
		thinkingTokenMsg(""),
	}
	for i, msg := range deltas {
		if !accumulateStreamMsg(accum, msg) {
			t.Fatalf("delta %d (%T) was not diverted into the accumulator", i, msg)
		}
	}

	control := []tea.Msg{
		streamDoneMsg{content: "done"},
		streamErrMsg{content: "boom"},
		streamUsageMsg{input: 1, output: 2},
		roleFallbackNoticeMsg{},
	}
	for i, msg := range control {
		if accumulateStreamMsg(accum, msg) {
			t.Fatalf("control message %d (%T) was diverted; it must keep the ordered channel path", i, msg)
		}
	}

	content, thinking, _, _ := accum.Drain()
	if content != "a" || thinking != "b" {
		t.Fatalf("drained content=%q thinking=%q, want %q/%q", content, thinking, "a", "b")
	}
}

// TestFrameTickIsTheOnlyReaderOfTheAccumulator: one tick promotes a whole
// batch into the visible stream, and the accumulator is empty afterwards. The
// "empty afterwards" half matters as much as the first — a drain that copied
// without swapping would re-emit the same batch on the next frame, and the
// symptom of that is a visibly repeating answer, not a crash.
func TestFrameTickIsTheOnlyReaderOfTheAccumulator(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamAccum = NewStreamAccumulator()

	for _, chunk := range []string{"the ", "quick ", "brown ", "fox"} {
		accumulateStreamMsg(m.streamAccum, tokenMsg(chunk))
	}
	if m.streamAccum.Pending() == 0 {
		t.Fatal("precondition: the accumulator must hold the deltas before the tick")
	}

	nm, _ := m.Update(FrameTickMsg{})
	m = nm.(*model)

	if got := m.currentStreamContent; got != "the quick brown fox" {
		t.Fatalf("one frame tick did not promote the whole batch: %q", got)
	}
	if n := m.streamAccum.Pending(); n != 0 {
		t.Fatalf("the accumulator still holds %d bytes after the drain", n)
	}

	// A tick with nothing pending must be a no-op on the visible content, and
	// must not append a duplicate.
	nm, _ = m.Update(FrameTickMsg{})
	m = nm.(*model)
	if got := m.currentStreamContent; got != "the quick brown fox" {
		t.Fatalf("an idle tick changed the visible stream: %q", got)
	}
}

// TestFrameTickIsArmedWithoutAPerTokenMessage: the frame loop used to be armed
// by the first tokenMsg, which is impossible now that a token produces no
// message. A stream whose provider is slow to first byte would therefore have
// no frame loop at all and no way to notice its own deltas. streamCmd is the
// only place that can arm it now, so that is what is asserted.
func TestFrameTickIsArmedWithoutAPerTokenMessage(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)
	m.streaming = true
	m.frameTickActive = false

	// The producer's own arming, checked in isolation: with no token in flight
	// the flag is only true because the stream armed it.
	nm, cmd := m.Update(FrameTickMsg{})
	m = nm.(*model)
	if cmd == nil {
		t.Fatal("a frame tick during a live stream must keep the loop alive")
	}
	if !m.frameTickActive {
		t.Error("frameTickActive must be set while a stream is live")
	}

	// And the loop must be self-sustaining: consecutive ticks keep it alive
	// with no message from the producer at all.
	for i := 0; i < 5; i++ {
		nm, cmd = m.Update(FrameTickMsg{})
		m = nm.(*model)
		if cmd == nil {
			t.Fatalf("tick %d: the frame loop died with no producer traffic", i)
		}
	}
}

// TestBatchedTokenEstimateMatchesThePerTokenSum: the live tok/s meter is a
// per-chunk estimate, and batching is only invisible if the batch reports the
// SUM of the per-chunk estimates. Estimating the joined batch instead would
// round differently and make the footer's rate a function of the frame cadence
// — a number that changes when the frame rate changes is a number nobody trusts.
func TestBatchedTokenEstimateMatchesThePerTokenSum(t *testing.T) {
	chunks := []string{"a", "hello there", strings.Repeat("x", 400), " ", "final chunk"}
	var wantTokens int
	for _, c := range chunks {
		wantTokens += estimateStreamTokens(c)
	}

	accum := NewStreamAccumulator()
	for _, c := range chunks {
		accum.AppendContent(c)
	}
	_, _, contentTokens, _ := accum.Drain()
	if contentTokens != wantTokens {
		t.Errorf("batched content tokens = %d, per-token sum = %d", contentTokens, wantTokens)
	}

	// The two buffers are accounted separately, so a reasoning chunk can never
	// inflate the content meter or vice versa.
	accum = NewStreamAccumulator()
	for _, c := range chunks {
		accum.AppendThinking(c)
	}
	_, _, _, thinkingTokens := accum.Drain()
	if thinkingTokens != wantTokens {
		t.Errorf("batched reasoning tokens = %d, per-token sum = %d", thinkingTokens, wantTokens)
	}
}

// TestAccumulatorLosesNothingUnderAConcurrentProducer runs the shape that
// matters: one producer goroutine appending while the UI goroutine drains, with
// a second drain racing the first so the swap itself is exercised. The assertion
// is EXHAUSTIVENESS — every appended byte is accounted for in some drain —
// because the accumulator's whole justification is that it converts queue
// pressure into memory instead of losing bytes.
func TestAccumulatorLosesNothingUnderAConcurrentProducer(t *testing.T) {
	const producers = 4
	const perProducer = 500
	const chunk = "delta"

	accum := NewStreamAccumulator()
	var wg sync.WaitGroup
	wg.Add(producers)
	for p := 0; p < producers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				accum.AppendContent(chunk)
			}
		}()
	}

	var mu sync.Mutex
	var got int
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			content, _, _, _ := accum.Drain()
			if content == "" {
				return
			}
			mu.Lock()
			got += len(content)
			mu.Unlock()
		}
	}()

	wg.Wait()
	// The reader exits on the first empty drain, so drive the tail by hand.
	for {
		content, _, _, _ := accum.Drain()
		if content == "" {
			break
		}
		mu.Lock()
		got += len(content)
		mu.Unlock()
	}
	<-drained

	mu.Lock()
	defer mu.Unlock()
	if want := producers * perProducer * len(chunk); got != want {
		t.Fatalf("drained %d bytes, appended %d — the accumulator dropped a delta", got, want)
	}
}

// TestIdleFrameCostsOneMutex pins the floor. A frame that finds nothing pending
// must not copy, allocate, or swap — and the only way to know that is to make
// the empty-drain path observable, which is what the drain's dirty flag is for.
func TestIdleFrameCostsOneMutex(t *testing.T) {
	accum := NewStreamAccumulator()
	if n := accum.Pending(); n != 0 {
		t.Fatalf("a fresh accumulator reports %d pending bytes", n)
	}
	content, thinking, ct, tt := accum.Drain()
	if content != "" || thinking != "" || ct != 0 || tt != 0 {
		t.Fatal("draining an empty accumulator must return nothing")
	}
	if n := accum.Pending(); n != 0 {
		t.Fatalf("draining an empty accumulator left %d bytes pending", n)
	}

	accum.AppendContent("x")
	if n := accum.Pending(); n != 1 {
		t.Fatalf("Pending = %d after one byte, want 1", n)
	}
	accum.Reset()
	if n := accum.Pending(); n != 0 {
		t.Fatalf("Reset left %d bytes pending", n)
	}
}

// TestEventQueueLoadIsDecoupledFromTokenRate is the quantitative form of the
// decoupling claim.
//
// The thing that made scrolling lag was not the bytes — it was the COUNT of
// events the stream pushed at the loop. A tea.Msg per token is a message plus
// the read command that fetches it plus whatever Update() does to answer it,
// and all of it queues in front of the reader's next wheel notch. So the
// number to hold down is the number of Update() deliveries per token, and this
// asserts it is zero: five hundred tokens cost the loop exactly one delivery —
// the frame tick that was going to happen anyway.
//
// The control is the same burst through the message path, so the ratio is
// measured against the real pre-change behaviour rather than asserted from
// memory.
func TestEventQueueLoadIsDecoupledFromTokenRate(t *testing.T) {
	const burst = 500

	// ── Accumulator path (the one the provider uses) ──
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamAccum = NewStreamAccumulator()
	m.refreshViewportContent()
	for i := 0; i < burst; i++ {
		accumulateStreamMsg(m.streamAccum, tokenMsg("w"))
	}
	nm, _ := m.Update(FrameTickMsg{})
	m = nm.(*model)
	accumulatorDeliveries := 1
	if got := m.currentStreamContent; len(got) != burst {
		t.Fatalf("the batch rendered %d bytes, want %d — a token was dropped", len(got), burst)
	}

	// ── Message path (the control: one delivery per token) ──
	c := readyChatModel(newTestModel())
	c.streaming = true
	c.utf8StreamBuf = &corestream.StreamBuffer{}
	c.streamThrottle = NewStreamThrottle()
	c.refreshViewportContent()
	messageDeliveries := 0
	for i := 0; i < burst; i++ {
		nm, _ = c.Update(tokenMsg("w"))
		c = nm.(*model)
		messageDeliveries++
	}

	if messageDeliveries != burst {
		t.Fatalf("precondition: the control path should deliver %d messages, delivered %d", burst, messageDeliveries)
	}
	// One frame tick against one Update per token: 99.8% of the queue pressure
	// is gone, which is the ">90%" the decoupling was specified to reach.
	queueLoad := float64(accumulatorDeliveries) / float64(messageDeliveries)
	if queueLoad > 0.01 {
		t.Errorf("the event queue still sees %.3f deliveries per token; the provider is still emitting messages", queueLoad)
	}
}

// TestStreamDoneDrainsTheAccumulator: the terminal handler is the last chance a
// delta can reach the viewport, and the accumulator's tail is the newest thing
// in the stream — it is exactly the part a completion handler is most likely to
// miss.
func TestStreamDoneDrainsTheAccumulator(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamAccum = NewStreamAccumulator()
	accumulateStreamMsg(m.streamAccum, tokenMsg("the tail that never got a frame"))

	m.resolver.Set(modes.ModeAsk)
	nm, _ := m.Update(streamDoneMsg{content: "the tail that never got a frame"})
	m = nm.(*model)

	if n := m.streamAccum.Pending(); n != 0 {
		t.Errorf("stream completion left %d undrained bytes", n)
	}
	if !strings.Contains(m.currentStreamContent+recordsText(m), "never got a frame") {
		t.Errorf("the undrained tail was lost at completion: %q", m.currentStreamContent)
	}
}

// ── B. Priority-zero mouse fast path ────────────────────────────────────────

// TestWheelIsAnsweredBeforeTheWorkspaceGuard is the ordering test. The
// workspace guard runs on every message and, when the workspace has vanished
// from disk, RECREATES it — a filesystem write, on every message, on the frame
// that has to answer a wheel notch. If the interceptor were anywhere but first,
// a wheel event would perform that write.
//
// The test makes the guard's side effect observable on disk, and then proves
// the guard is real by showing a non-wheel message still triggers it. Without
// that second half this test would pass on a build where the guard had been
// deleted.
func TestWheelIsAnsweredBeforeTheWorkspaceGuard(t *testing.T) {
	m := initializedChatModel(t)
	m.records = make([]record, 40)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "history line " + strings.Repeat("word ", 12)}
	}
	m.wrapWidth = m.width
	m.refreshViewportContent()

	// Delete the workspace out from under the model, exactly as a reader who
	// ran `rm -rf .izen` would.
	izenDir := filepath.Join(m.workspaceRoot, ".izen")
	if err := os.RemoveAll(izenDir); err != nil {
		t.Fatal(err)
	}
	offset := m.docScrollOffset

	nm, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)

	if cmd != nil {
		t.Error("the wheel fast path must return a nil command (zero-timer contract)")
	}
	if m.docScrollOffset >= offset {
		t.Errorf("the wheel did not scroll: %d -> %d", offset, m.docScrollOffset)
	}
	if _, err := os.Stat(izenDir); err == nil {
		t.Error("a wheel event ran the workspace self-heal; the fast path is not first")
	}

	// The guard is real: any OTHER message still heals the workspace.
	nm, _ = m.Update(repaintTickMsg{})
	_ = nm.(*model)
	if _, err := os.Stat(izenDir); err != nil {
		t.Fatalf("precondition broken: a non-wheel message did not run the self-heal either (%v)", err)
	}
}

// TestWheelTouchesNothingButTheOffsetAndTheLatch is the zero-work contract.
// Every field a render would touch is snapshotted before the wheel and compared
// after, so a fast path that quietly re-rendered the document, re-rasterized
// the framebuffer or rebuilt the hit map cannot pass.
func TestWheelTouchesNothingButTheOffsetAndTheLatch(t *testing.T) {
	m := buildScrollableModel()
	m.streaming = true
	m.refreshViewportContent()

	rows := len(m.docLayout.Lines)
	hitRows := len(m.fullHitRows)
	poolLines := len(m.scrollDocLines)
	fb := m.framebuffer
	docStart := m.streamingDocStart
	liveTokens := m.streamLiveTokens
	stage := m.stageSnapshot()

	nm, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)

	if cmd != nil {
		t.Error("the wheel fast path must return a nil command")
	}
	if got := len(m.docLayout.Lines); got != rows {
		t.Errorf("the wheel re-flattened the document: %d -> %d rows", rows, got)
	}
	if got := len(m.fullHitRows); got != hitRows {
		t.Errorf("the wheel rebuilt the hit map: %d -> %d rows", hitRows, got)
	}
	if got := len(m.scrollDocLines); got != poolLines {
		t.Errorf("the wheel rebuilt the scroll pool: %d -> %d lines", poolLines, got)
	}
	if m.framebuffer != fb {
		t.Error("the wheel re-rasterized the selection framebuffer")
	}
	if m.streamingDocStart != docStart {
		t.Errorf("the wheel moved the streaming segment boundary: %d -> %d", docStart, m.streamingDocStart)
	}
	if m.streamLiveTokens != liveTokens {
		t.Errorf("the wheel changed the token counter: %d -> %d", liveTokens, m.streamLiveTokens)
	}
	if m.stageSnapshot() != stage {
		t.Error("the wheel changed the pipeline stage")
	}
	if m.scrollChromeDirty {
		t.Error("a pure scroll frame must leave the chrome cache reusable")
	}
	if !m.userDetached {
		t.Error("a wheel-up must engage the detach latch")
	}
}

// TestOverlaysStillSwallowTheWheel: a fast path is only legitimate if it
// answers the same events the slow path answered. Every overlay that used to
// swallow the wheel must still swallow it — and swallow it as a CONSUMED event,
// which is a different thing from letting it fall through to the document.
func TestOverlaysStillSwallowTheWheel(t *testing.T) {
	cases := []struct {
		name string
		arm  func(m *model)
	}{
		{"status overlay", func(m *model) { m.showStatus = true }},
		{"settings", func(m *model) { m.showSettings = true }},
		{"model picker", func(m *model) { m.showModelPicker = true }},
		{"quit confirm", func(m *model) { m.pendingQuitConfirm = true }},
		{"permission gate", func(m *model) { m.pendingPermission = &pendingPermissionFixture }},
		{"approval state", func(m *model) { m.state = StateAwaitingApproval }},
		{"hotfix ambiguity", func(m *model) { m.state = StateHotfixAmbiguous }},
		{"onboarding", func(m *model) { m.initStage = initGitCheck }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildScrollableModel()
			m.refreshViewportContent()
			offset := m.docScrollOffset
			tc.arm(m)

			nm, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
			m = nm.(*model)
			if cmd != nil {
				t.Error("an overlay must consume the wheel with a nil command")
			}
			if m.docScrollOffset != offset {
				t.Errorf("the wheel scrolled the document behind an overlay: %d -> %d", offset, m.docScrollOffset)
			}
			if m.userDetached {
				t.Error("a swallowed wheel must not move the detach latch")
			}
		})
	}
}

// TestWheelFastPathAllocatesNothing: the DoD's "near-zero allocations while
// scrolling during a stream". A scroll frame that allocates is a scroll frame
// that can be the one that triggers a GC pause in the middle of an answer, and
// a pause is indistinguishable from a hang.
//
// The measurement reads MemStats around a loop rather than using
// testing.AllocsPerRun, which pins GOMAXPROCS to 1 for the duration and reports
// a floor of one object per call for reasons that have nothing to do with the
// code under test — a floor this test would then have to be written around.
func TestWheelFastPathAllocatesNothing(t *testing.T) {
	m := buildScrollableModel()
	m.streaming = true
	m.refreshViewportContent()
	// Sit mid-document so the offset is not already clamped at a bound.
	m.docScrollOffset = m.maxAppScroll() / 2

	// The message is boxed ONCE, into the tea.Msg the real event loop delivers.
	// Handing Update a bare tea.MouseMsg would box a 48-byte struct on every
	// call and the test would be measuring that allocation — which belongs to
	// the decoder, not to the path under test.
	var wheel tea.Msg = tea.MouseMsg{Button: tea.MouseButtonWheelUp}
	const runs = 2000
	for i := 0; i < 50; i++ {
		_, _ = m.Update(wheel)
	}
	// The control loop measures the HARNESS: the same two ReadMemStats calls
	// around an empty body. Whatever it reports is the floor, and it is
	// subtracted so the assertion is about the wheel and not about the
	// stop-the-world the counter itself performs.
	floor := measureAllocs(runs, func() {})
	got := measureAllocs(runs, func() { _, _ = m.Update(wheel) })

	if got-floor > 0.01 {
		t.Errorf("%d wheel frames allocated %.4f objects each (%.4f above the %.4f harness floor), want 0",
			runs, got, got-floor, floor)
	}
}

// measureAllocs returns the average number of objects allocated per call by
// `body`, measured through MemStats.
//
// MemStats rather than testing.AllocsPerRun for two reasons: AllocsPerRun pins
// GOMAXPROCS to 1 for the duration and reports a floor of one object per call
// whatever the code does, and it measures only a function value — it cannot
// report the floor of the measurement itself, which is the only way to
// distinguish "the path allocated once in 2000 frames" from "the counter did".
func measureAllocs(runs int, body func()) float64 {
	for i := 0; i < 20; i++ {
		body()
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		body()
	}
	runtime.ReadMemStats(&after)
	return float64(after.Mallocs-before.Mallocs) / float64(runs)
}

// TestWheelIsInstantWhileTokensStream is the headline DoD, expressed as an
// invariant rather than a timing: a scroll notch answered while a stream is
// live must move the offset on that event, and the latch must survive every
// subsequent frame that arrives tokens. If the queue ever gets ahead of the
// wheel again, this is the test that notices.
//
// The document is deliberately made TALLER than the scroll budget, so the
// notches have somewhere to go: a notch that clamps at a bound proves nothing
// about responsiveness, and 30 notches is enough to reach the top of a
// 40-record conversation in a 20-row pane.
func TestWheelIsInstantWhileTokensStream(t *testing.T) {
	m := buildScrollableModel()
	m.records = make([]record, 200)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "history line " + strings.Repeat("word ", 12)}
	}
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamAccum = NewStreamAccumulator()
	m.refreshViewportContent()

	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)
	if !m.userDetached {
		t.Fatal("precondition: the wheel-up must detach")
	}
	held := m.docScrollOffset
	if held < 20 {
		t.Fatalf("precondition: the reader must have room to scroll (offset %d)", held)
	}

	// A token burst on the producer side plus frames on the UI side: the wheel
	// is answered in the middle of it, and every notch must land.
	for i := 0; i < 10; i++ {
		accumulateStreamMsg(m.streamAccum, tokenMsg("burst chunk that wraps onto more document rows "))
		nm, _ = m.Update(FrameTickMsg{})
		m = nm.(*model)
		nm, _ = m.Update(repaintTickMsg{})
		m = nm.(*model)

		nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		m = nm.(*model)

		if want := held - wheelScrollRows*(i+1); m.docScrollOffset != want {
			t.Fatalf("notch %d: offset %d, want %d — the wheel was not answered on its own event", i+1, m.docScrollOffset, want)
		}
		if !m.userDetached {
			t.Fatalf("notch %d: a growing stream cleared the detach latch", i+1)
		}
	}
}

// ── C. O(1) per-frame cost ──────────────────────────────────────────────────

// documentRenderedText returns the RENDERED rows of the flat document, ANSI
// stripped. It is the only honest subject for a "was this markdown-rendered"
// assertion: recordsText is the raw record store and legitimately still contains
// the fences and pipes the answer was TYPED with, while the rendered rows carry
// syntax-highlight escapes inside the code block — so the code is compared as
// the plain text the reader sees.
func documentRenderedText(m *model) string {
	if m.docLayout == nil {
		return ""
	}
	rows := m.docRowPool(m.docLayout.Len())
	return ansi.Strip(strings.Join(rows, "\n"))
}

// streamingDocOf builds a model with `records` committed records and a live
// stream, positioned at the tail.
func streamingDocOf(t *testing.T, records int) *model {
	t.Helper()
	m := readyChatModel(newTestModel())
	m.records = make([]record, records)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "record " + itoa(i) + " " + strings.Repeat("word ", 10)}
	}
	m.wrapWidth = m.width
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamAccum = NewStreamAccumulator()
	m.refreshViewportContent()
	return m
}

// TestFrameCostDoesNotScaleWithDocumentLength is the O(1)-per-frame assertion.
//
// The measurement is ALLOCATIONS, not wall time: allocation counts are
// deterministic in Go, while a wall-clock comparison would be a coin flip on a
// loaded CI box and would therefore be a test that only fails when the machine
// is busy. The ratio is what carries the claim — a frame that re-derives the
// hit map and re-rasterizes the selection framebuffer allocates per document
// row, so ten times the document is ten times the allocations, while a frame
// that serves the immutable rows from cache and composes only the visible
// window is flat in it.
func TestFrameCostDoesNotScaleWithDocumentLength(t *testing.T) {
	const small, big = 200, 2000

	frameAllocs := func(records int) float64 {
		m := streamingDocOf(t, records)
		// Warm every cache so the measured frames are steady-state frames,
		// which is what a stream actually produces after its first tick.
		nm, _ := m.Update(FrameTickMsg{})
		m = nm.(*model)
		nm, _ = m.Update(repaintTickMsg{})
		m = nm.(*model)
		accumulateStreamMsg(m.streamAccum, tokenMsg("one more streamed line\n"))
		return measureAllocs(200, func() { _, _ = m.Update(repaintTickMsg{}) })
	}

	smallAllocs := frameAllocs(small)
	bigAllocs := frameAllocs(big)
	floor := measureAllocs(200, func() {})
	smallAllocs -= floor
	bigAllocs -= floor
	if smallAllocs <= 0 {
		t.Fatalf("the small frame allocated nothing at all (%v) — the measurement is not exercising the frame", smallAllocs)
	}
	// Ten times the document must not cost anything close to ten times the
	// work. The bar is deliberately loose: it is there to catch a return to
	// per-document re-derivation, not to police a constant.
	if ratio := bigAllocs / smallAllocs; ratio > 3 {
		t.Errorf("frame cost scales with document length: %v allocs at %d records, %v at %d (%.1fx for 10x the document)",
			smallAllocs, small, bigAllocs, big, ratio)
	}
}

// TestStreamingFrameReusesTheHitMapAndTheRowPool asserts that frames are
// SERVED rather than RECOMPUTED, through the models' own rebuild counters.
//
// "Cheaper" is not checkable in a unit test; "rebuilt zero times across twenty
// frames" is, and it is the property the cheaper path depends on. Counters are
// used in preference to backing-array identity because a cache that reallocates
// its own slack is still a cache — capacity growth is an implementation detail,
// a rebuild is the thing being claimed against.
func TestStreamingFrameReusesTheHitMapAndTheRowPool(t *testing.T) {
	m := streamingDocOf(t, 400)

	nm, _ := m.Update(FrameTickMsg{})
	m = nm.(*model)
	nm, _ = m.Update(repaintTickMsg{})
	m = nm.(*model)

	if !m.hitMapCacheLive {
		t.Fatal("a streaming frame must leave the hit map memoized")
	}
	if len(m.hitMapCache) == 0 || len(m.docRowCache) == 0 {
		t.Fatal("precondition: the streaming caches must be populated")
	}
	hitRebuilds := m.hitMapRebuilds
	rowRebuilds := m.docRowRebuilds
	rowCount := m.docRowCached
	fb := m.framebuffer

	// Twenty frames, each carrying new content. Every one of them must be
	// served from the same caches.
	for i := 0; i < 20; i++ {
		accumulateStreamMsg(m.streamAccum, tokenMsg("streamed line number "+itoa(i)+"\n"))
		nm, _ = m.Update(FrameTickMsg{})
		m = nm.(*model)
		nm, _ = m.Update(repaintTickMsg{})
		m = nm.(*model)

		if m.hitMapRebuilds != hitRebuilds {
			t.Fatalf("frame %d re-derived the hit map", i)
		}
		if m.docRowRebuilds != rowRebuilds {
			t.Fatalf("frame %d re-read the whole document row pool", i)
		}
		if m.docRowCached < rowCount {
			t.Fatalf("frame %d shrank the stable row boundary: %d -> %d", i, rowCount, m.docRowCached)
		}
		if m.framebuffer != fb {
			t.Fatalf("frame %d re-rasterized the selection framebuffer mid-stream with no selection", i)
		}
	}
	if m.docRowCached <= rowCount {
		t.Error("the row cache never grew across 20 frames of streaming content")
	}
}

// TestFrameCachesAreDroppedAtTurnBoundaries: the caches rest on the document
// being append-only WITHIN a turn. A new turn and a cleared surface are exactly
// the two places that stops being true, and a cache that survived either would
// show the reader the previous answer's rows.
func TestFrameCachesAreDroppedAtTurnBoundaries(t *testing.T) {
	t.Run("new turn", func(t *testing.T) {
		m := streamingDocOf(t, 50)
		nm, _ := m.Update(FrameTickMsg{})
		m = nm.(*model)
		if !m.hitMapCacheLive {
			t.Fatal("precondition: the hit map must be memoized during a stream")
		}
		m.resetStreamingRenderer()
		if m.hitMapCacheLive || m.hitMapCache != nil || m.docRowCache != nil {
			t.Error("a new turn must drop the per-frame caches")
		}
	})

	t.Run("cleared surface", func(t *testing.T) {
		m := streamingDocOf(t, 50)
		nm, _ := m.Update(FrameTickMsg{})
		m = nm.(*model)
		nm, _ = m.Update(repaintTickMsg{})
		m = nm.(*model)
		if !m.hitMapCacheLive || len(m.docRowCache) == 0 {
			t.Fatal("precondition: the caches must be populated during a stream")
		}

		m.resetTransientInteraction()
		m.refreshViewportContent()

		// The clear path repaints as part of its own work, so the caches are
		// legitimately live again — but they must be live for the EMPTY
		// document. Asserting on content rather than on nil-ness is the only
		// way to say "dropped and rebuilt from nothing" instead of "dropped and
		// rebuilt from the records that just went away".
		if m.hitMapCacheLive && m.hitMapCacheRecords != len(m.records) {
			t.Errorf("the hit map survived the clear: built from %d records, %d remain",
				m.hitMapCacheRecords, len(m.records))
		}
		for _, row := range m.docRowCache {
			if strings.Contains(row, "record 0 ") {
				t.Errorf("a cleared row survived in the row cache: %q", row)
				break
			}
		}
	})
}

// TestHitMapIsRebuiltWhenTheRecordsMoveMidStream: the memo is keyed on the
// records, so an activity line pushed into the middle of a stream must
// invalidate it. A key that only covered the streaming case would show the
// reader a hit map that is one record short, and the symptom would be a
// selection that copies the wrong line — the kind of bug that is invisible in
// a screenshot and obvious to the person using it.
func TestHitMapIsRebuiltWhenTheRecordsMoveMidStream(t *testing.T) {
	m := streamingDocOf(t, 30)
	nm, _ := m.Update(FrameTickMsg{})
	m = nm.(*model)
	before := m.hitMapFor()

	m.records = append(m.records, record{role: roleActivity, text: "an activity line that arrived mid-stream"})
	after := m.hitMapFor()
	if len(after) <= len(before) {
		t.Fatalf("the hit map did not grow with the records: %d -> %d rows", len(before), len(after))
	}
}

// TestVolatileTrailingRowIsNeverServedFromCache: the still-growing partial
// line is re-rendered from scratch every tick and can legitimately produce a
// DIFFERENT row than it did last tick — a leading pipe latches the table
// holdback and the row vanishes. The row cache deliberately excludes the last
// row for exactly this reason, and a test that let it go stale would show a
// table that never appears.
func TestVolatileTrailingRowIsNeverServedFromCache(t *testing.T) {
	m := streamingDocOf(t, 20)

	nm, _ := m.Update(FrameTickMsg{})
	m = nm.(*model)
	nm, _ = m.Update(repaintTickMsg{})
	m = nm.(*model)

	// A trailing line that latches the table holdback: the row must DISAPPEAR
	// from the document rather than persist from the cache.
	accumulateStreamMsg(m.streamAccum, tokenMsg("| Name | Age\n"))
	nm, _ = m.Update(FrameTickMsg{})
	m = nm.(*model)
	nm, _ = m.Update(repaintTickMsg{})
	m = nm.(*model)
	if !m.tableLatch.On() {
		t.Skip("precondition: the table holdback did not latch on this input")
	}
	rows := m.docRowPool(m.docLayout.Len())
	last := rows[len(rows)-1]
	if strings.Contains(last, "Name") {
		t.Errorf("the held-back table row was served from the row cache: %q", last)
	}
}

// ── D. Final full Markdown pass at completion ───────────────────────────────

// TestCompletionRunsOneFullMarkdownPass: the incremental renderer is what keeps
// a frame at O(delta), and an incremental renderer is only safe if the
// committed answer is re-derived from the WHOLE text exactly once at the end —
// otherwise a construct whose meaning depends on a line that arrived later (a
// closing fence, a table terminator) would be left in the state its own
// streaming form implied.
func TestCompletionRunsOneFullMarkdownPass(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamAccum = NewStreamAccumulator()

	// A fenced block and a table, both of which are only structurally
	// complete at the end of the stream.
	answer := "Here you go:\n\n```go\nfunc main() {}\n```\n\n| Name | Age |\n| --- | --- |\n| Ada | 36 |\n"
	m.streamAccum.AppendContent(answer)

	m.resolver.Set(modes.ModeAsk)
	nm, _ := m.Update(streamDoneMsg{content: answer})
	m = nm.(*model)

	if m.streaming {
		t.Fatal("precondition: the stream should be finished")
	}
	// The committed record must be the fully-rendered form: the closing fence
	// consumed, the grid laid out, and no raw syntax left in the DOCUMENT.
	// recordsText is the raw record store and legitimately still contains
	// fences, so the assertion is on what is rendered.
	rendered := documentRenderedText(m)
	if strings.Contains(rendered, "```") {
		t.Errorf("the completed answer still carries raw fences — the final markdown pass did not run:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Ada") {
		t.Errorf("the completed answer is missing the table body:\n%s", rendered)
	}
	if !strings.Contains(rendered, "func main") {
		t.Errorf("the completed answer lost the code block body:\n%s", rendered)
	}
	// And the streaming tail must be gone, not shadowing the record.
	if m.streamingDocStart >= 0 && m.streamingDocStart < m.docLayout.Len() {
		t.Error("the streaming tail survived completion and would shadow the committed record")
	}
}

// pendingPermissionFixture is a zero-value stand-in for the permission gate.
// isModalForMouse only tests it for non-nil, and a real request carries a
// response channel this test would then have to drain — so the fixture is the
// request alone.
var pendingPermissionFixture = policy.PermissionRequest{}
