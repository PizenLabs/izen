package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// makeTraceModel builds a ready model with a populated output trace and a tall
// history so a mid scroll offset is meaningful within the 20-line viewport.
func makeTraceModel(traceLines int) *model {
	m := newTestModel()
	m.traceBuffer.Reset()
	for i := 0; i < traceLines; i++ {
		fmt.Fprintf(&m.traceBuffer, "trace-line-%03d\n", i)
	}
	m.thinkingBuffer = nil
	m.thinkingPanel = nil
	m.activityTree = nil
	m.records = make([]record, 30)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "history line " + strings.Repeat("word ", 8)}
	}
	m.PreRenderedHistory = strings.Repeat("history line\n", 60)
	return m
}

// TestTraceWindowFreezesWhileStreaming pins the Ctrl+O flicker fix: while the
// output-trace viewport is expanded during an active stream, the rendered
// window must be anchored once and stay frozen — new chunks must never slide
// the inspected lines out from under the user.
func TestTraceWindowFreezesWhileStreaming(t *testing.T) {
	m := makeTraceModel(30)
	m.streaming = true
	m.traceExpanded = true

	first := m.renderOutputTrace(80)
	if !strings.Contains(first, "trace-line-025") {
		t.Fatalf("first render must show the tail window:\n%s", first)
	}
	if strings.Contains(first, "trace-line-000") {
		t.Fatalf("first render must not show the trace head:\n%s", first)
	}
	if !m.traceWindowAnchored {
		t.Error("render must anchor the trace window while streaming")
	}
	anchor := m.traceWindowStart

	// Simulate the stream growing by 20 more lines.
	for i := 30; i < 50; i++ {
		fmt.Fprintf(&m.traceBuffer, "trace-line-%03d\n", i)
	}

	second := m.renderOutputTrace(80)
	if m.traceWindowStart != anchor {
		t.Errorf("trace window must stay frozen during streaming (anchor %d -> %d)", anchor, m.traceWindowStart)
	}
	// The frozen window HEAD must stay put — the inspected lines must not slide
	// up out of the box. New lines append BELOW the anchored window.
	headLine := fmt.Sprintf("trace-line-%03d", anchor+1)
	if !strings.Contains(second, headLine) {
		t.Errorf("frozen window head must stay visible (%q), window slid:\n%s", headLine, second)
	}
}

// TestTraceWindowReanchorsOnStreamEnd pins the release path: once the stream
// completes, the trace window re-anchors to the tail so the user sees the full
// final output.
func TestTraceWindowReanchorsOnStreamEnd(t *testing.T) {
	m := makeTraceModel(30)
	m.streaming = true
	m.traceExpanded = true
	m.renderOutputTrace(80)
	if !m.traceWindowAnchored {
		t.Fatal("window must anchor while streaming")
	}

	for i := 30; i < 50; i++ {
		fmt.Fprintf(&m.traceBuffer, "trace-line-%03d\n", i)
	}
	m.streaming = false
	out := m.renderOutputTrace(80)
	if m.traceWindowAnchored {
		t.Error("window anchor must be released after the stream ends")
	}
	if !strings.Contains(out, "trace-line-049") {
		t.Errorf("post-stream render must show the full tail:\n%s", out)
	}
}

// TestStreamingTickPreservesOffsetWhileTraceExpanded pins the viewport scroll
// lock at the tick level: a repaint during an active stream MUST NOT yank the
// viewport to the tail while the Ctrl+O output-trace viewport is expanded and
// the user has scrolled away from the tail. Collapsing the trace (and
// re-engaging tail-lock) restores auto-scroll.
func TestStreamingTickPreservesOffsetWhileTraceExpanded(t *testing.T) {
	m := makeTraceModel(30)
	m.streaming = true
	m.traceExpanded = true
	m.currentStreamContent = "streaming output\n"

	// Seed a mid scroll offset the user has settled on.
	m.setScrollLocked(true)
	m.docScrollOffset = 25
	m.refreshViewportContent()
	if m.docScrollOffset != 25 {
		t.Fatalf("test precondition: could not set offset 25, got %d", m.docScrollOffset)
	}

	// Expanded trace: the repaint must preserve the offset.
	m.Update(repaintTickMsg(time.Now()))
	if m.docScrollOffset != 25 {
		t.Errorf("streaming repaint with traceExpanded changed offset 25 -> %d (scroll lock violated)", m.docScrollOffset)
	}

	// Collapsed trace + tail-lock: auto-scroll returns and follows the tail.
	m.traceExpanded = false
	m.setScrollLocked(false)
	m.docScrollOffset = 25
	m.Update(repaintTickMsg(time.Now()))
	if m.docScrollOffset == 25 {
		t.Error("collapsed trace should allow auto-scroll to the bottom")
	}
}

// TestToggleThoughtBlockPreservesOffsetWhileTraceExpanded pins the Ctrl+O
// handler itself: toggling the trace open mid-stream must not force the
// viewport to the bottom while the user is scrolled away from the tail.
func TestToggleThoughtBlockPreservesOffsetWhileTraceExpanded(t *testing.T) {
	m := makeTraceModel(30)
	m.streaming = true
	m.traceBuffer.Reset()
	m.traceBuffer.WriteString("raw output line 1\nraw output line 2\n")
	m.setScrollLocked(true)
	m.docScrollOffset = 10
	m.refreshViewportContent()
	if m.docScrollOffset != 10 {
		t.Fatalf("test precondition: could not set offset 10, got %d", m.docScrollOffset)
	}

	if !m.toggleThoughtBlock() {
		t.Fatal("toggleThoughtBlock should expand the output trace")
	}
	if !m.traceExpanded {
		t.Fatal("trace should be expanded")
	}
	if m.docScrollOffset != 10 {
		t.Errorf("Ctrl+O expansion changed offset 10 -> %d (viewport jumped)", m.docScrollOffset)
	}
}
