package ui

import (
	"strings"
	"sync"
	"testing"

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

// TestTypewriterStepIsOneThirdBounded pins the drain formula. The step is
// `max(1, pending/3)`, so a one-byte queue still moves (or a stream would stall)
// and a large queue is worked off geometrically without ever releasing the whole
// burst in one frame.
func TestTypewriterStepIsOneThirdBounded(t *testing.T) {
	cases := []struct {
		pending, want int
	}{
		{0, 0},
		{1, 1},
		{2, 1},
		{3, 1},
		{4, 1},
		{6, 2},
		{9, 3},
		{30, 10},
		{90, 30},
		{150, 50},
	}
	for _, tc := range cases {
		if got := typewriterStep(tc.pending); got != tc.want {
			t.Errorf("typewriterStep(%d) = %d, want %d", tc.pending, got, tc.want)
		}
	}
}

// TestTypewriterQueueIsFIFOAndRuneSafe pins the two properties the renderer
// relies on: bytes come out in the order they went in, and a multi-byte rune is
// never split across frames (a split would paint a replacement character for a
// frame). The pop is forced onto a boundary by cutting one byte at a time.
func TestTypewriterQueueIsFIFOAndRuneSafe(t *testing.T) {
	var q typewriterQueue
	const content = "aé漢😀z"
	q.push(content)

	if got := q.len(); got != len(content) {
		t.Fatalf("queue len = %d, want %d", got, len(content))
	}
	var built strings.Builder
	for q.len() > 0 {
		chunk := q.pop(1)
		if len(chunk) == 0 {
			t.Fatal("pop returned nothing while bytes remained")
		}
		built.Write(chunk)
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
	if q.len() != 0 {
		t.Fatalf("queue still holds %d bytes", q.len())
	}
	if got := q.pop(1); len(got) != 0 {
		t.Fatalf("popping an empty queue returned %q", got)
	}
}

// TestTypewriterPacerConvergesAndNeverRevealsTooMuch is the smoothing contract:
// a burst is revealed across several frames, each frame releasing no more than
// its adaptive step (plus at most one rune of boundary extension), and the whole
// burst is eventually on screen. Nothing is dropped.
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
	var revealed int
	for p.Pending() > 0 {
		before := p.Pending()
		if !p.Advance() {
			t.Fatal("Advance reported nothing while bytes were pending")
		}
		frames++
		if frames > total+2 {
			t.Fatal("the reveal did not converge")
		}
		// The bytes released this frame cannot exceed the step plus the at-most
		// three bytes needed to finish a rune.
		newly := before - p.Pending()
		if max := typewriterStep(before) + 3; newly > max {
			t.Fatalf("frame %d revealed %d bytes, over the %d-byte step", frames, newly, max)
		}
		revealed += newly
	}
	if revealed != total {
		t.Errorf("revealed %d of %d bytes", revealed, total)
	}
	if got := p.Revealed(); len(got) != total {
		t.Errorf("revealed prefix = %d bytes, want %d", len(got), total)
	}
}

// TestTypewriterPacerSlowStreamConverges pins the floor: a queue smaller than
// three bytes still moves one byte per frame (step floors at one), so a slow
// stream is revealed within a frame or two rather than stalled.
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
		t.Errorf("slow stream left %d bytes queued", p.Pending())
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
	if got := m.typewriter.Pending(); got != len(burst) {
		t.Fatalf("the burst was not queued: pending = %d, want %d", got, len(burst))
	}
	if m.streamingDocStart >= 0 {
		t.Fatal("the burst was painted whole despite the pacer")
	}

	// Frame-by-frame, the revealed prefix grows and the tail follows it.
	frames := 0
	for m.typewriter.Pending() > 0 {
		if !m.advanceTypewriter() {
			t.Fatal("advanceTypewriter reported no movement while bytes were pending")
		}
		frames++
		if frames > len(burst)+2 {
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
// The assertion is exhaustiveness — every pushed byte is eventually revealed —
// which is what makes the queue safe to move a producer onto.
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
	if got := len(p.Revealed()); got != want {
		t.Fatalf("revealed %d bytes, pushed %d — the queue dropped a push", got, want)
	}
}
