package ui

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/events"
)

// waitForReasoning polls the mutex-protected reasoning slice until the joined
// chunks equal want (bus delivery is async). Returns the joined text.
func waitForReasoning(mu *sync.Mutex, s *[]string, want string) string {
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		joined := strings.Join(*s, "")
		mu.Unlock()
		if joined == want {
			return joined
		}
		if time.Now().After(deadline) {
			return joined
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHandleDomainEventReasoningProjection(t *testing.T) {
	m := &model{}
	m.handleDomainEvent(events.NewReasoningStream("step one ", false))
	m.handleDomainEvent(events.NewReasoningStream("step two", false))

	if m.thinkingBuffer == nil {
		t.Fatal("thinkingBuffer not created")
	}
	if got := m.thinkingBuffer.String(); got != "step one step two" {
		t.Errorf("buffer = %q, want %q", got, "step one step two")
	}
	if m.thinkingBuffer.Complete() {
		t.Error("buffer should not be complete before terminal event")
	}
	if len(m.records) != 0 {
		t.Errorf("reasoning must not create chat records, got %d", len(m.records))
	}

	m.handleDomainEvent(events.NewReasoningStream("", true))
	if !m.thinkingBuffer.Complete() {
		t.Error("buffer should be complete after terminal event")
	}
}

func TestHandleReasoningStreamAppendsAndCompletes(t *testing.T) {
	m := &model{}
	m.handleReasoningStream("a", false)
	m.handleReasoningStream("b", false)
	if got := m.thinkingBuffer.String(); got != "ab" {
		t.Errorf("buffer = %q, want ab", got)
	}
	m.handleReasoningStream("", true)
	if !m.thinkingBuffer.Complete() {
		t.Error("expected complete")
	}
}

func TestThinkingBufferRenderStreaming(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("analyze the failure mode")
	if tb.Render(80, true, "✦") == "" {
		t.Fatal("streaming render is empty")
	}
}

// TestThinkingBufferRenderStreamingCollapsed guards the default (collapsed)
// streaming widget: a compact "Thinking.. (Xs)" spinner line — never the box.
func TestThinkingBufferRenderStreamingCollapsed(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("analyze the failure mode")
	out := tb.Render(80, true, "✦")

	if !strings.Contains(out, "Thinking..") {
		t.Errorf("collapsed streaming line missing Thinking..: %q", out)
	}
	if strings.Contains(out, "│") {
		t.Errorf("collapsed streaming line must not render the box gutter: %q", out)
	}
	if strings.Contains(out, "analyze the failure mode") {
		t.Errorf("collapsed streaming line must not render full reasoning: %q", out)
	}
}

func TestThinkingBufferRenderStreamingBox(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("trace line one\ntrace line two")
	tb.SetExpanded(true)
	out := tb.Render(80, true, "✦")

	if !strings.Contains(out, "│") {
		t.Errorf("box missing gutter bar: %q", out)
	}
	if !strings.Contains(out, "trace line one") {
		t.Errorf("box missing reasoning line: %q", out)
	}
	// NO-DUPLICATE CONTRACT: the expanded box must NEVER re-print its own
	// "Thinking…" header — that status line belongs to the parent indicator
	// (the loading dock or the collapsed one-liner the box replaces).
	if strings.Contains(out, "Thinking") {
		t.Errorf("expanded box must not duplicate the parent Thinking header: %q", out)
	}
	// No response content may leak into the box.
	if strings.Contains(out, "final answer") {
		t.Errorf("box contains response text: %q", out)
	}
}

func TestThinkingBufferRenderAutoScroll(t *testing.T) {
	tb := NewThinkingBuffer()
	for i := 0; i < 50; i++ {
		tb.Append("line\n")
	}
	tb.SetExpanded(true)
	// maxLines defaults to 10 → 10 content lines + the overflow footer. The
	// expanded box is capped and never renders a duplicate Thinking header.
	out := tb.Render(80, true, "✦")
	lines := strings.Count(out, "\n") + 1
	if lines > 11 {
		t.Errorf("box scrolls unbounded: %d lines rendered", lines)
	}
	if strings.Contains(out, "Thinking") {
		t.Errorf("expanded box must not duplicate the parent Thinking header: %q", out)
	}
}

func TestThinkingBufferRenderCompactOnComplete(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("some reasoning")
	tb.SetReasoningTokens(64)
	tb.MarkComplete()
	out := tb.Render(80, true, "✦")

	if !strings.Contains(out, "Thought for") {
		t.Errorf("compact line missing Thought for: %q", out)
	}
	if !strings.Contains(out, "64") {
		t.Errorf("compact line missing authoritative token count: %q", out)
	}
	if strings.Contains(out, "some reasoning") {
		t.Errorf("compact mode must not render full reasoning: %q", out)
	}
	if strings.Contains(out, "│") {
		t.Errorf("compact mode must not render the box gutter: %q", out)
	}
}

// TestThinkingBufferExpandedRendersFullReasoning guards the expanded thought
// block: toggling expansion renders the full reasoning text in the dimmed box.
func TestThinkingBufferExpandedRendersFullReasoning(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("full reasoning text here")
	tb.MarkComplete()
	tb.SetExpanded(true)

	out := tb.Render(80, true, "✦")
	if !strings.Contains(out, "full reasoning text here") {
		t.Errorf("expanded block missing reasoning text: %q", out)
	}
	if !strings.Contains(out, "Thought for") {
		t.Errorf("expanded complete block missing summary footer: %q", out)
	}
}

// TestThinkingBufferToggleFlipsExpansion guards the Ctrl+O toggle contract.
func TestThinkingBufferToggleFlipsExpansion(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("thinking")
	if tb.Expanded() {
		t.Error("new buffer must start collapsed")
	}
	tb.Toggle()
	if !tb.Expanded() {
		t.Error("Toggle must expand the thought block")
	}
	tb.Toggle()
	if tb.Expanded() {
		t.Error("Toggle must collapse the thought block")
	}
	tb.SetExpanded(true)
	if !tb.Expanded() {
		t.Error("SetExpanded(true) must expand the thought block")
	}
}

// TestCtrlOTogglesThoughtBlock guards the Ctrl+O contract: with an active
// thought block it expands/collapses the reasoning text (IsThinkingExpanded);
// with no thought block it falls back to cycling the foldable log entries.
func TestCtrlOTogglesThoughtBlock(t *testing.T) {
	m := newTestModel()
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("reasoning tokens")

	// First Ctrl+O: collapsed → expanded.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.thinkingBuffer.Expanded() {
		t.Fatal("Ctrl+O did not expand the thought block")
	}

	// Second Ctrl+O: expanded → collapsed.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.thinkingBuffer.Expanded() {
		t.Fatal("Ctrl+O did not collapse the thought block")
	}

	// Empty thought buffer → fall back to log entry cycling, never panic.
	m.thinkingBuffer = NewThinkingBuffer()
	m.logStore.Add(LogEdit, "file.go", true, "content")
	if m.logStore.Entries()[0].Expanded {
		t.Fatal("log entry must start collapsed")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.logStore.Entries()[0].Expanded {
		t.Error("Ctrl+O with no thought block must fall back to cycling log entries")
	}
}

// TestCtrlOAsyncToggleDuringStreaming guards the async reasoning toggle: while
// reasoning tokens are still streaming in (m.streaming == true), a Ctrl+O
// keypress must expand the inline faint reasoning box in the Viewport body
// IMMEDIATELY (no waiting for stream completion, no freeze). Collapsed state
// must never leak the full reasoning text into the body; expanded state must
// show it.
func TestCtrlOAsyncToggleDuringStreaming(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("live reasoning tokens")
	m.refreshViewportContent()

	collapsed := m.Viewport.View()
	if strings.Contains(collapsed, "live reasoning tokens") {
		t.Fatalf("collapsed streaming body leaked full reasoning: %q", collapsed)
	}
	if !strings.Contains(collapsed, "Thinking") {
		t.Fatalf("collapsed streaming body missing live thinking indicator: %q", collapsed)
	}

	// Async expand on keypress while still streaming.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.thinkingBuffer.Expanded() {
		t.Fatal("Ctrl+O did not expand the thought block during active streaming")
	}
	expanded := m.Viewport.View()
	if !strings.Contains(expanded, "live reasoning tokens") {
		t.Fatalf("Ctrl+O during streaming did not show the inline reasoning box: %q", expanded)
	}

	// Async collapse on a second keypress while still streaming.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.thinkingBuffer.Expanded() {
		t.Fatal("Ctrl+O did not collapse the thought block during active streaming")
	}
	collapsedAgain := m.Viewport.View()
	if strings.Contains(collapsedAgain, "live reasoning tokens") {
		t.Fatalf("collapsed body leaked reasoning after second Ctrl+O: %q", collapsedAgain)
	}
}

// TestAltOTogglesThinkingBuffer guards the Alt+O alias: it must behave exactly
// like Ctrl+O (expand/collapse the event-driven ThinkingBuffer) rather than
// flipping an unreferenced flag. Alt+O is intercepted at the Update layer
// (global intercept) before handleKey, so the full Update path is exercised.
func TestAltOTogglesThinkingBuffer(t *testing.T) {
	m := newTestModel()
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("reasoning tokens")

	altO := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o"), Alt: true}

	nm, _ := m.Update(altO)
	if !nm.(*model).thinkingBuffer.Expanded() {
		t.Fatal("Alt+O must expand the ThinkingBuffer like Ctrl+O")
	}

	nm2, _ := nm.Update(altO)
	if nm2.(*model).thinkingBuffer.Expanded() {
		t.Fatal("Alt+O must collapse the ThinkingBuffer like Ctrl+O")
	}
}

func TestThinkingBufferRenderEmpty(t *testing.T) {
	tb := NewThinkingBuffer()
	if got := tb.Render(80, true, "✦"); got != "" {
		t.Errorf("empty buffer = %q, want empty", got)
	}
}

func TestThinkingBufferAppendResetsAfterComplete(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("old")
	tb.MarkComplete()
	tb.Append("new")
	if tb.Complete() {
		t.Error("new reasoning block must clear completion")
	}
	if got := tb.String(); got != "new" {
		t.Errorf("buffer = %q, want only new reasoning", got)
	}
}

func TestRenderLiveThinkingPrefersEventBuffer(t *testing.T) {
	m := &model{
		width:          80,
		spinnerFrame:   0,
		streaming:      true,
		thinkingBuffer: NewThinkingBuffer(),
	}
	m.thinkingBuffer.Append("event-driven reasoning")
	m.thinkingBuffer.SetExpanded(true)

	out := m.renderLiveThinking(80)
	if !strings.Contains(out, "event-driven reasoning") {
		t.Errorf("event buffer not rendered: %q", out)
	}
}

func TestRenderLiveThinkingEmpty(t *testing.T) {
	m := &model{width: 80, streaming: true}
	if got := m.renderLiveThinking(80); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
}

// ── end-to-end ingestion tests ──────────────────────────────────────────────

// byteChunkReader returns the input one byte at a time — the worst possible
// chunk alignment for UTF-8 rune boundaries.
type byteChunkReader struct {
	data []byte
	pos  int
}

func (r *byteChunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

func TestIngestLLMStreamRuneSafeAcrossByteBoundaries(t *testing.T) {
	// Multi-byte runes delivered one raw byte at a time must be reassembled
	// without corruption, and reasoning must be routed away from content.
	input := "héllo 你好 <thought>plan it</thought> ```go\ncode\n```"

	bus := events.NewBus(64)
	defer bus.Close()

	var mu sync.Mutex
	var reasoning []string
	bus.Subscribe(events.EventReasoningStream, func(ev events.DomainEvent) {
		p := ev.Payload().(events.ReasoningPayload)
		if p.Chunk == "" {
			return
		}
		mu.Lock()
		reasoning = append(reasoning, p.Chunk)
		mu.Unlock()
	})

	var content strings.Builder
	var thinking strings.Builder
	full, err := ingestLLMStream(&byteChunkReader{data: []byte(input)}, bus, func(s string) {
		content.WriteString(s)
	}, func(s string) {
		thinking.WriteString(s)
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	joined := waitForReasoning(&mu, &reasoning, "plan it")
	if joined != "plan it" {
		t.Errorf("reasoning = %q, want %q", joined, "plan it")
	}
	if content.String() != "héllo 你好  ```go\ncode\n```" {
		t.Errorf("content = %q, want %q", content.String(), "héllo 你好  ```go\ncode\n```")
	}
	if thinking.String() != "plan it" {
		t.Errorf("thinking = %q, want %q", thinking.String(), "plan it")
	}
	if full != content.String() {
		t.Errorf("returned full = %q, want %q", full, content.String())
	}
}

func TestIngestLLMStreamPreservesEscapesVerbatim(t *testing.T) {
	// Long markdown with math/code must round-trip with zero missing words.
	input := "Use `x` = `\\sqrt{2}` and *bold* `_under_` then \\n literal"
	var content strings.Builder
	full, err := ingestLLMStream(strings.NewReader(input), nil, func(s string) {
		content.WriteString(s)
	}, nil)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if full != input {
		t.Errorf("escapes corrupted:\n got  %q\n want %q", full, input)
	}
	if content.String() != input {
		t.Errorf("content mismatch:\n got  %q\n want %q", content.String(), input)
	}
}

func TestIngestLLMStreamPublishesTerminalComplete(t *testing.T) {
	var mu sync.Mutex
	var got []events.ReasoningPayload
	bus := events.NewBus(16)
	defer bus.Close()
	bus.Subscribe(events.EventReasoningStream, func(ev events.DomainEvent) {
		mu.Lock()
		got = append(got, ev.Payload().(events.ReasoningPayload))
		mu.Unlock()
	})

	_, err := ingestLLMStream(strings.NewReader("plain"), bus, nil, nil)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// One complete event with an empty chunk must always close the stream.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal events not delivered: got %d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0].Chunk != "" || !got[0].IsComplete {
		t.Errorf("terminal events = %+v, want single complete", got)
	}
}

func TestIngestLLMStreamEmptyInput(t *testing.T) {
	full, err := ingestLLMStream(strings.NewReader(""), nil, nil, nil)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if full != "" {
		t.Errorf("full = %q, want empty", full)
	}
}

func TestThinkingBufferRenderStyling(t *testing.T) {
	// The style must be precompiled and usable (regression guard for the
	// Faint+Italic thinking style).
	if thinkingStyle.Render("x") == "" {
		t.Error("thinkingStyle renders empty")
	}
	if !thinkingStyle.GetFaint() || !thinkingStyle.GetItalic() {
		t.Errorf("thinkingStyle = faint=%v italic=%v, want both true",
			thinkingStyle.GetFaint(), thinkingStyle.GetItalic())
	}
}
