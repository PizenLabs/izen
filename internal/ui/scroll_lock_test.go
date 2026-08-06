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
// lock at the tick level: a smoothStreamTickMsg during an active stream MUST
// NOT yank the viewport to the bottom while the Ctrl+O output-trace viewport
// is expanded. Collapsing the trace restores auto-scroll.
func TestStreamingTickPreservesOffsetWhileTraceExpanded(t *testing.T) {
	m := makeTraceModel(30)
	m.streaming = true
	m.traceExpanded = true
	m.currentStreamContent = "streaming output\n"

	// Seed a mid scroll offset the user has settled on.
	m.Viewport.SetContent(strings.Repeat("seed line\n", 60))
	m.Viewport.SetYOffset(25)
	if m.Viewport.YOffset != 25 {
		t.Fatalf("test precondition: could not set YOffset=25, got %d", m.Viewport.YOffset)
	}

	// Expanded trace: the streaming tick must preserve the offset.
	m.Update(smoothStreamTickMsg(time.Now()))
	if m.Viewport.YOffset != 25 {
		t.Errorf("streaming tick with traceExpanded changed YOffset 25 -> %d (scroll lock violated)", m.Viewport.YOffset)
	}

	// Collapsed trace: auto-scroll returns and the viewport follows the tail.
	m.traceExpanded = false
	m.Viewport.SetYOffset(25)
	m.Update(smoothStreamTickMsg(time.Now()))
	if m.Viewport.YOffset == 25 {
		t.Error("collapsed trace should allow auto-scroll to the bottom")
	}
}

// TestToggleThoughtBlockPreservesOffsetWhileTraceExpanded pins the Ctrl+O
// handler itself: toggling the trace open mid-stream must not force the
// viewport to the bottom.
func TestToggleThoughtBlockPreservesOffsetWhileTraceExpanded(t *testing.T) {
	m := makeTraceModel(30)
	m.streaming = true
	m.traceBuffer.Reset()
	m.traceBuffer.WriteString("raw output line 1\nraw output line 2\n")
	m.Viewport.SetContent(strings.Repeat("seed line\n", 60))
	m.Viewport.SetYOffset(10)
	if m.Viewport.YOffset != 10 {
		t.Fatalf("test precondition: could not set YOffset=10, got %d", m.Viewport.YOffset)
	}
	m.userIsScrollingUp = true

	if !m.toggleThoughtBlock() {
		t.Fatal("toggleThoughtBlock should expand the output trace")
	}
	if !m.traceExpanded {
		t.Fatal("trace should be expanded")
	}
	if m.Viewport.YOffset != 10 {
		t.Errorf("Ctrl+O expansion changed YOffset 10 -> %d (viewport jumped)", m.Viewport.YOffset)
	}
}
