package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
)

// buildScrollableModel returns a chat-ready model with enough committed
// records that the scrollable document exceeds the 20-row viewport,
// tail-locked to the bottom via an initial refresh.
func buildScrollableModel() *model {
	m := readyChatModel(newTestModel())
	m.records = make([]record, 40)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "history line " + strings.Repeat("word ", 12)}
	}
	m.wrapWidth = m.width
	m.refreshViewportContent()
	return m
}

// TestPromptIsolation pins PROMPT RENDER ISOLATION: viewport scroll events
// and stream token arrivals reuse the cached prompt frame, while genuine
// prompt state changes (input text) regenerate it and cursor blink ticks
// invalidate it.
func TestPromptIsolation(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.ti.SetValue("hello")
	m.ti.Focus()

	first := m.renderPromptView()
	if m.cachedPromptKey == "" {
		t.Fatal("renderPromptView must memoize the prompt frame")
	}
	if second := m.renderPromptView(); second != first {
		t.Fatal("prompt view must be stable across identical renders")
	}

	// Stream token arrivals must not invalidate the prompt frame.
	if _, _ = m.Update(tokenMsg("streaming chunk")); true {
		if got := m.renderPromptView(); got != first {
			t.Error("tokenMsg must reuse cachedPromptView (prompt re-rendered on stream update)")
		}
	}

	// Mouse wheel scrolling must not invalidate the prompt frame.
	wheel := tea.MouseMsg{Button: tea.MouseButtonWheelUp}
	if _, _ = m.Update(wheel); true {
		if got := m.renderPromptView(); got != first {
			t.Error("MouseMsg wheel must reuse cachedPromptView (prompt re-rendered on scroll)")
		}
	}

	// Genuine input change regenerates the frame.
	m.ti.SetValue("hello world")
	if got := m.renderPromptView(); got == first {
		t.Error("prompt view must regenerate after input text change")
	}

	// Cursor blink ticks invalidate so the blink animation stays live.
	m.renderPromptView()
	if _, _ = m.Update(cursor.BlinkMsg{}); true {
		if m.cachedPromptKey != "" {
			t.Error("cursor.BlinkMsg must invalidate the prompt cache")
		}
	}
}

// TestScrollLock pins MANUAL SCROLL ENGAGEMENT LOCK: scrolling up during an
// active stream locks auto-scroll (tokens preserve yOffset), and scrolling
// back to the absolute bottom deterministically re-engages tailing.
func TestScrollLock(t *testing.T) {
	m := buildScrollableModel()
	if m.userScrollLocked {
		t.Fatal("precondition: fresh model must start tail-locked")
	}
	if m.docScrollOffset != m.maxAppScroll() {
		t.Fatalf("precondition: expected tail offset %d, got %d", m.maxAppScroll(), m.docScrollOffset)
	}

	// Manual wheel-up engages the lock.
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if !m.userScrollLocked {
		t.Fatal("wheel-up must engage userScrollLocked")
	}
	lockedOffset := m.docScrollOffset
	if lockedOffset >= m.maxAppScroll() {
		t.Fatalf("wheel-up must move off the tail (offset %d, max %d)", lockedOffset, m.maxAppScroll())
	}

	// Stream tokens preserve the manual offset while locked.
	m.streaming = true
	_, _ = m.Update(tokenMsg("live token chunk"))
	_, _ = m.Update(repaintTickMsg(time.Now()))
	if m.docScrollOffset != lockedOffset {
		t.Errorf("stream repaint moved yOffset %d -> %d while scroll-locked", lockedOffset, m.docScrollOffset)
	}
	if !m.userScrollLocked {
		t.Error("stream repaint must not clear userScrollLocked")
	}

	// Scrolling back to the absolute bottom re-engages auto-scroll.
	for i := 0; i < 40 && m.userScrollLocked; i++ {
		_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	}
	if m.userScrollLocked {
		t.Error("reaching the absolute bottom must clear userScrollLocked")
	}
	if m.docScrollOffset != m.maxAppScroll() {
		t.Errorf("re-engaged offset %d != tail %d", m.docScrollOffset, m.maxAppScroll())
	}
}

// TestStreamPacing pins STREAM FRAME PACING: token ingestion updates state
// memory synchronously WITHOUT an immediate repaint (no per-token full
// render); the fixed-interval tick loops pace frames behind a single-flight
// repaint gate, and tokens never advance the spinner — only the
// frame-locked tick loops do.
func TestStreamPacing(t *testing.T) {
	m := buildScrollableModel()
	m.streaming = true
	m.refreshScheduled = false
	spinnerBefore := m.spinnerFrame

	// Token arrival buffers in memory but never repaints synchronously.
	_, _ = m.Update(tokenMsg("alpha "))
	if m.refreshScheduled {
		t.Error("tokenMsg must not repaint synchronously (ingestion decoupled from redraw)")
	}
	_, _ = m.Update(tokenMsg("beta "))
	if m.refreshScheduled {
		t.Error("rapid tokens must buffer without arming per-token repaints")
	}

	// Tokens must not drive spinner animation (frame-locked tickers own it).
	if m.spinnerFrame != spinnerBefore {
		t.Errorf("tokenMsg advanced spinnerFrame %d -> %d (must be tick-driven only)", spinnerBefore, m.spinnerFrame)
	}

	// The paced tick loop arms exactly one frame (single-flight gate).
	_, _ = m.Update(smoothStreamTickMsg(time.Now()))
	if !m.refreshScheduled {
		t.Fatal("paced tick must arm the single-flight repaint gate")
	}

	// Frame ticks render the buffered window; ticks + repaints drain both
	// chunks with no token left behind.
	for i := 0; i < 6 && !strings.Contains(m.currentStreamContent, "beta "); i++ {
		_, _ = m.Update(smoothStreamTickMsg(time.Now()))
		_, _ = m.Update(repaintTickMsg(time.Now()))
	}
	if !strings.Contains(m.currentStreamContent, "alpha ") || !strings.Contains(m.currentStreamContent, "beta ") {
		t.Errorf("paced frames dropped tokens: %q", m.currentStreamContent)
	}
}
