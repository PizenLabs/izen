package ui

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/config"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// ── ADAPTIVE TYPEWRITER ANIMATION PACER ───────────────────────────────────────
//
// The pacer is a production-only render layer: a headless model leaves it nil
// and renders on arrival. These tests exercise BOTH contracts — the nil-model
// immediate reveal that the rest of the suite depends on, and the interpolation
// that the live TUI uses to turn a bursty provider into flowing text.

// TestTypewriterStepIsRuneDominatedInTwoRegimes pins the drain formula, which is
// the whole tunability surface of the reveal:
//
//	pending < 60   →  1..3 runes per frame (a sequential typewriter cadence)
//	pending >= 60  →  min(12, pending/5)      (stream-lag insurance)
//
// Both regimes are asserted at their boundaries, because the boundaries are the
// only places a wrong divisor or a wrong cap is invisible: at 55 runes a
// `pending/3` formula gives 18 and a `pending/5` gives 11, so any formula that
// is not one of these two fails the table rather than passing "most" of it.
func TestTypewriterStepIsRuneDominatedInTwoRegimes(t *testing.T) {
	sequential := []struct{ pending, want int }{
		{0, 0},
		{1, 1},
		{2, 1},
		{3, 1},
		{4, 1},
		{10, 1},
		{20, 1}, // 20*3/60 = 1
		{21, 1},
		{30, 1}, // 30*3/60 = 1
		{40, 2}, // 40*3/60 = 2
		{50, 2},
		{59, 2}, // still inside the sequential regime
	}
	for _, tc := range sequential {
		if got := typewriterStep(tc.pending); got != tc.want {
			t.Errorf("typewriterStep(%d) = %d, want %d (sequential regime)", tc.pending, got, tc.want)
		}
		// The DoD clause: a small backlog drains strictly 1..3 runes per frame.
		if tc.pending > 0 {
			if got := typewriterStep(tc.pending); got < 1 || got > 3 {
				t.Errorf("typewriterStep(%d) = %d, outside the 1..3 sequential range", tc.pending, got)
			}
		}
	}

	burst := []struct{ pending, want int }{
		{60, 12}, // 60/5 = 12
		{59, 2},  // the threshold itself: one below is still sequential
		{61, 12},
		{100, 12}, // 100/5 = 20, capped at 12
		{300, 12},
		{1000, 12},
	}
	for _, tc := range burst {
		if got := typewriterStep(tc.pending); got != tc.want {
			t.Errorf("typewriterStep(%d) = %d, want %d (burst regime)", tc.pending, got, tc.want)
		}
	}

	// The formula, restated independently so a change to one regime cannot be
	// masked by a change to the other.
	for pending := 1; pending < 4000; pending++ {
		got := typewriterStep(pending)
		if got < 1 || got > pending {
			t.Fatalf("typewriterStep(%d) = %d is outside [1, %d]", pending, got, pending)
		}
		if pending < 60 {
			continue // the range assertion above already covered this range
		}
		if want := min(pending/5, 12); got != want {
			t.Fatalf("typewriterStep(%d) = %d, want min(12, pending/5) = %d", pending, got, want)
		}
	}
}

// TestTypewriterStepIsRuneDominatedNotByteDominated is the reason the queue was
// converted from bytes: the cadence a reader perceives is a CHARACTER, and a
// byte-denominated step reveals four times as many glyphs for a CJK answer as for
// an ASCII one. With a rune step, N pending characters of either script take the
// same number of frames to drain.
func TestTypewriterStepIsRuneDominatedNotByteDominated(t *testing.T) {
	framesToDrain := func(text string) int {
		p := newTypewriterPacer()
		p.Push(text)
		frames := 0
		for p.Pending() > 0 {
			if !p.Advance() {
				t.Fatal("Advance reported nothing while characters were pending")
			}
			frames++
			if frames > 10_000 {
				t.Fatal("the reveal did not converge")
			}
		}
		return frames
	}
	// Thirty characters either way: thirty ASCII letters (30 bytes) or fifteen
	// CJK glyphs (45 bytes). A byte-denominated step would drain the CJK answer
	// in half the frames, and a byte-denominated QUEUE would report 45 pending
	// against 30 — so both halves of the conversion are caught by this one
	// comparison.
	const ascii = "abcdefghijklmnopqrstuvwxyz0123"
	cjk := strings.Repeat("漢字", 15)
	framesASCII := framesToDrain(ascii)
	framesCJK := framesToDrain(cjk)
	if framesASCII != framesCJK {
		t.Errorf("the cadence is not character-denominated: %d ASCII characters (%d bytes) "+
			"took %d frames, %d CJK characters (%d bytes) took %d",
			len([]rune(ascii)), len(ascii), framesASCII,
			len([]rune(cjk)), len(cjk), framesCJK)
	}
}

// TestTypewriterQueueIsFIFOAndRuneSafe pins the two properties the renderer
// relies on: characters come out in the order they went in, and a multi-byte
// rune is never split across frames (a split would paint a replacement character
// for a frame). Because the queue is rune-denominated, the boundary is exact —
// there is nothing to repair after the fact.
func TestTypewriterQueueIsFIFOAndRuneSafe(t *testing.T) {
	var q typewriterQueue
	const content = "aé漢😀z"
	q.push(content)

	if got := q.pendingRunes(); got != len([]rune(content)) {
		t.Fatalf("queue depth = %d runes, want %d", got, len([]rune(content)))
	}
	var built strings.Builder
	for q.pendingRunes() > 0 {
		chunk := q.pop(1)
		if len(chunk) == 0 {
			t.Fatal("pop returned nothing while characters remained")
		}
		if len(chunk) != 1 {
			t.Fatalf("pop(1) released %d runes", len(chunk))
		}
		// Every pop is exactly one WHOLE rune: this is the assertion the byte
		// implementation could not make.
		if chunk[0] == utf8.RuneError {
			t.Fatal("pop released a replacement character — a rune was split")
		}
		built.WriteRune(chunk[0])
	}
	if built.String() != content {
		t.Errorf("queue round-trip = %q, want %q", built.String(), content)
	}
}

// TestTypewriterQueuePopClampsToAvailable pins that a pop larger than the queue
// drains it rather than over-reading.
func TestTypewriterQueuePopClampsToAvailable(t *testing.T) {
	var q typewriterQueue
	q.push("abc")
	if got := string(q.pop(100)); got != "abc" {
		t.Fatalf("pop(100) = %q, want the whole queue", got)
	}
	if q.pendingRunes() != 0 {
		t.Fatalf("queue still holds %d characters", q.pendingRunes())
	}
	if got := q.pop(1); len(got) != 0 {
		t.Fatalf("popping an empty queue returned %q", got)
	}
}

// TestTypewriterPacerConvergesAndNeverRevealsTooMuch is the smoothing contract:
// a burst is revealed across several frames, each frame releasing no more than
// its adaptive step, and the whole burst is eventually on screen. Nothing is
// dropped.
//
// The per-frame bound is now EXACT rather than "step plus at most three bytes":
// the queue is rune-denominated, so a frame releases precisely the number of
// characters the step asked for. A frame that released more would be the jump
// this file exists to remove, and it is now detectable without an allowance.
func TestTypewriterPacerConvergesAndNeverRevealsTooMuch(t *testing.T) {
	p := newTypewriterPacer()
	const burst = "the answer arrives in one packet but streams out fluidly. "
	for range 8 {
		p.Push(burst)
	}
	total := p.Pending()
	if total == 0 {
		t.Fatal("precondition: the pacer must hold the burst")
	}

	frames := 0
	revealed := 0
	for p.Pending() > 0 {
		before := p.Pending()
		if !p.Advance() {
			t.Fatal("Advance reported nothing while characters were pending")
		}
		frames++
		if frames > total+2 {
			t.Fatal("the reveal did not converge")
		}
		if newly := before - p.Pending(); newly > typewriterStep(before) {
			t.Fatalf("frame %d revealed %d characters, over the %d-character step",
				frames, newly, typewriterStep(before))
		}
		revealed += before - p.Pending()
	}
	if revealed != total {
		t.Errorf("revealed %d of %d characters", revealed, total)
	}
	if got := len([]rune(p.Revealed())); got != total {
		t.Errorf("revealed prefix = %d characters, want %d", got, total)
	}
	// A burst this size is in the catch-up regime, so it must take several
	// frames — a single-frame drain is exactly the artefact being removed.
	if frames < 2 {
		t.Errorf("a %d-character burst drained in %d frame: the reveal is a jump, not a reveal",
			total, frames)
	}
}

// TestTypewriterPacerSlowStreamConverges pins the floor: a queue smaller than
// three characters still moves one character per frame (the step floors at one),
// so a slow stream is revealed within a frame or two rather than stalled.
func TestTypewriterPacerSlowStreamConverges(t *testing.T) {
	p := newTypewriterPacer()
	p.Push("hi")
	p.Advance()
	if got := p.Revealed(); got != "h" {
		t.Errorf("first advance revealed %q, want %q", got, "h")
	}
	if p.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", p.Pending())
	}
	p.Advance()
	if got := p.Revealed(); got != "hi" {
		t.Errorf("second advance revealed %q, want %q", got, "hi")
	}
	if p.Pending() != 0 {
		t.Errorf("slow stream left %d characters queued", p.Pending())
	}
}

// TestTypewriterIsNilModelImmediateReveal is the regression guard for the rest
// of the suite: with no pacer, emitVisibleContent must render synchronously,
// exactly as before the pacer existed.
func TestTypewriterIsNilModelImmediateReveal(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamingDocStart = -1
	m.docLayout = &DocumentLayout{width: 80}

	if m.typewriter != nil {
		t.Fatal("precondition: a headless model must not construct a pacer")
	}
	m.emitVisibleContent("immediate line\nsecond line\n")
	if m.currentStreamContent != "immediate line\nsecond line\n" {
		t.Fatalf("currentStreamContent = %q", m.currentStreamContent)
	}
	if m.streamingDocStart < 0 {
		t.Fatal("the nil-pacer path must render the tail synchronously")
	}
	if !strings.Contains(tailText(m.docLayout.Lines[m.streamingDocStart:]), "immediate line") {
		t.Error("the synchronously rendered tail is missing the emitted content")
	}
}

// TestModelTypewriterDefersThenReveals is the production integration: with the
// pacer armed, an emitted batch is QUEUED (not painted whole), the authoritative
// buffer still holds every byte immediately, and each advanceTypewriter frame
// reveals another adaptive slice into the rendered tail.
func TestModelTypewriterDefersThenReveals(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamingDocStart = -1
	m.docLayout = &DocumentLayout{width: 80}
	m.typewriter = newTypewriterPacer()

	// A packet-sized burst, exactly the shape the pacer targets.
	burst := strings.Repeat("streaming text ", 6)
	m.emitVisibleContent(burst)

	if m.currentStreamContent != burst {
		t.Fatalf("authoritative content = %q, want the whole burst", m.currentStreamContent)
	}
	if got := m.typewriter.Pending(); got != len([]rune(burst)) {
		t.Fatalf("the burst was not queued: pending = %d runes, want %d", got, len([]rune(burst)))
	}
	if m.streamingDocStart >= 0 {
		t.Fatal("the burst was painted whole despite the pacer")
	}

	// Frame-by-frame, the revealed prefix grows and the tail follows it.
	frames := 0
	for m.typewriter.Pending() > 0 {
		if !m.advanceTypewriter() {
			t.Fatal("advanceTypewriter reported no movement while characters were pending")
		}
		frames++
		if frames > len([]rune(burst))+2 {
			t.Fatal("the model reveal did not converge")
		}
	}
	if m.typewriter.Revealed() != burst {
		t.Errorf("revealed prefix != authoritative content")
	}
	if m.streamingDocStart < 0 {
		t.Fatal("no streaming tail was rendered")
	}
	rendered := tailText(m.docLayout.Lines[m.streamingDocStart:])
	if !strings.Contains(rendered, "streaming text") {
		t.Errorf("the revealed tail is missing the content: %q", rendered)
	}
}

// TestModelTypewriterAdvanceIsNoOpOutsideAStream pins the guard: outside a live
// stream there is no tail to advance, so the frame tick pays nothing.
func TestModelTypewriterAdvanceIsNoOpOutsideAStream(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = false
	m.typewriter = newTypewriterPacer()
	m.typewriter.Push("queued")
	if m.advanceTypewriter() {
		t.Error("advanceTypewriter moved outside a live stream")
	}
	if m.typewriter.Pending() == 0 {
		t.Error("advanceTypewriter drained the queue outside a stream")
	}
}

// TestModelTypewriterBalancesTheRevealedTrailingLine pins the AST-balancer
// integration: the revealed prefix is what the tail renderer parses, so an
// unclosed `**` in the middle of the reveal is styled rather than shown raw.
func TestModelTypewriterBalancesTheRevealedTrailingLine(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamingDocStart = -1
	m.docLayout = &DocumentLayout{width: 80}
	m.typewriter = newTypewriterPacer()

	m.emitVisibleContent("**Migration complete")
	for m.typewriter.Pending() > 0 {
		m.advanceTypewriter()
	}
	rendered := ansi.Strip(tailText(m.docLayout.Lines[m.streamingDocStart:]))
	if strings.Contains(rendered, "**") {
		t.Errorf("the revealed trailing line flashed a raw marker: %q", rendered)
	}
	if !strings.Contains(rendered, "Migration complete") {
		t.Errorf("the revealed trailing line lost its content: %q", rendered)
	}
}

// TestTypewriterLifecycleIsScopedToOneStream pins the arming contract around
// resetStreamingRenderer: a mid-stream rebuild (a resize) keeps the reveal in
// progress, while a non-streaming teardown releases it so a finished answer can
// never leave its backlog for the next turn.
func TestTypewriterLifecycleIsScopedToOneStream(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.typewriter = newTypewriterPacer()

	m.resetStreamingRenderer()
	if m.typewriter == nil {
		t.Error("a mid-stream rebuild dropped the reveal")
	}

	m.streaming = false
	m.resetStreamingRenderer()
	if m.typewriter != nil {
		t.Error("a turn-boundary teardown retained the reveal pacer")
	}
}

// TestTypewriterArmsAtProductionStreamStart is the wiring guard: the pacer is
// inert in every headless harness by construction, so the one place that must
// turn it on is the production stream start. If that arming is ever dropped,
// this fails rather than the smoothing silently disappearing from the TUI.
func TestTypewriterArmsAtProductionStreamStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubPersistBinding(t)

	m := readyChatModel(newTestModel())
	m.cfg = config.Default()
	m.cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "openrouter",
		Model:    "thinkingmachines/inkling-small:free",
	}
	m.modelAuthority = appruntime.NewRuntimeAuthority()
	m.modelAuthority.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("thinkingmachines/inkling-small:free"),
	})
	m.provider = &recordingProvider{
		name:     "openrouter",
		outcomes: []providerOutcome{{content: "ok"}},
	}

	if cmd := m.streamCmd("what is the answer?"); cmd == nil {
		t.Fatal("streamCmd must dispatch a live stream")
	}
	if m.typewriter == nil {
		t.Fatal("the production stream start must arm the typewriter pacer")
	}
}

// TestTypewriterQueueIsSafeUnderConcurrentProducer runs the shape the queue is
// built for: appends from several goroutines while the UI goroutine consumes.
// The assertion is exhaustiveness — every pushed character is eventually
// revealed — which is what makes the queue safe to move a producer onto.
func TestTypewriterQueueIsSafeUnderConcurrentProducer(t *testing.T) {
	p := newTypewriterPacer()
	const producers = 4
	const perProducer = 250
	const chunk = "x"

	var wg sync.WaitGroup
	wg.Add(producers)
	for range producers {
		go func() {
			defer wg.Done()
			for range perProducer {
				p.Push(chunk)
			}
		}()
	}

	// Consume concurrently so Push and pop really do race.
	stop := make(chan struct{})
	var consumer sync.WaitGroup
	consumer.Add(1)
	go func() {
		defer consumer.Done()
		for {
			select {
			case <-stop:
				return
			default:
				p.Advance()
			}
		}
	}()

	wg.Wait()
	close(stop)
	consumer.Wait()

	// Drain whatever the racing consumer left behind.
	for p.Pending() > 0 {
		p.Advance()
	}
	want := producers * perProducer
	if got := len([]rune(p.Revealed())); got != want {
		t.Fatalf("revealed %d characters, pushed %d — the queue dropped a push", got, want)
	}
}
