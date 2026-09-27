// scroll_fastpath_guard_test.go — THE WHEEL IS O(1), ALWAYS.
//
// # WHY THIS FILE EXISTS
//
// A wheel notch that "feels" slow is almost never a slow offset write. It is a
// notch that arrived behind a Markdown re-parse, a chrome re-measure or a layout
// rebuild — work that belongs to the render loop and that the scroll path must
// never join. The cost is invisible in a single frame and cumulative during
// streaming, which is exactly when the queue is deepest and latency is most
// noticeable.
//
// The contract has two halves and this file asserts both:
//
//   - STATE: a notch changes the row offset and nothing else. No repaint is
//     armed, no command is returned, the rendered line cache is untouched, the
//     geometry is not re-measured.
//   - COST: the notch allocates nothing. This is the half that cannot be
//     reviewed by reading, because "there is no fmt on this path" is a claim
//     about a function, and only an allocation counter can tell whether a
//     future edit kept it true.

package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWheelNotchIsPureOffsetMutation asserts the state half: a notch on a
// warm cache mutates the offset, returns a nil command, arms no repaint, and
// leaves the cache and the geometry exactly as it found them.
func TestWheelNotchIsPureOffsetMutation(t *testing.T) {
	m := buildScrollableModel()
	if !m.renderedLineCacheWarm() {
		t.Fatal("precondition: the rendered line cache must be warm")
	}
	m.streaming = true

	before := m.docScrollOffset
	beforeRows := m.lastScrollTotal
	beforeHeight := m.Viewport.Height
	beforeTotal := len(m.renderedLineCache())
	beforeRecords := len(m.records)
	beforeDoc := m.docLayout

	out, cmd, handled := m.interceptWheel(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if !handled {
		t.Fatal("a wheel notch was not handled by the fast path")
	}
	if cmd != nil {
		t.Error("the wheel fast path returned a command — it must emit zero timers")
	}
	if out != tea.Model(m) {
		t.Error("the wheel fast path returned a different model")
	}
	if m.docScrollOffset >= before {
		t.Errorf("an upward notch did not move the offset: %d → %d", before, m.docScrollOffset)
	}
	if m.refreshScheduled {
		t.Error("a wheel notch armed a repaint")
	}
	if m.lastScrollTotal != beforeRows {
		t.Errorf("a wheel notch re-measured the document: %d → %d rows", beforeRows, m.lastScrollTotal)
	}
	if len(m.renderedLineCache()) != beforeTotal {
		t.Errorf("a wheel notch re-rendered the line cache: %d → %d lines", beforeTotal, len(m.renderedLineCache()))
	}
	if m.Viewport.Height != beforeHeight {
		t.Errorf("a wheel notch recomputed the layout: viewport height %d → %d", beforeHeight, m.Viewport.Height)
	}
	if len(m.records) != beforeRecords {
		t.Errorf("a wheel notch mutated the transcript: %d → %d records", beforeRecords, len(m.records))
	}
	if m.docLayout != beforeDoc {
		t.Error("a wheel notch rebuilt the document layout")
	}
}

// TestWheelNotchAllocatesNothing is the cost half, asserted while a stream is
// live — the state in which a stray allocation on the scroll path becomes the
// GC pause that lands in the middle of an answer.
//
// The warm-up call exists because the FIRST notch through any path legitimately
// pays one-time costs (lazily-built caches elsewhere in the model); what is
// being measured is the steady state, which is where a per-notch cost would
// actually accumulate.
func TestWheelNotchAllocatesNothing(t *testing.T) {
	m := buildScrollableModel()
	m.streaming = true
	// Warm the fast path and park the offset away from the bounds so neither
	// the clamp nor the latch re-arms is part of the measured window.
	for range 4 {
		_, _, _ = m.interceptWheel(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		_, _, _ = m.interceptWheel(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	}

	notch := func() {
		if _, cmd, _ := m.interceptWheel(tea.MouseMsg{Button: tea.MouseButtonWheelUp}); cmd != nil {
			t.Fatal("the wheel fast path emitted a command")
		}
	}
	const rounds = 64
	allocs := testing.AllocsPerRun(rounds, notch)
	if allocs != 0 {
		t.Errorf("a wheel notch allocated %.1f objects; the fast path must be O(1) with "+
			"zero allocations (no string building, no layout, no Markdown)", allocs)
	}
}

// TestWheelOnAColdCacheStillDrawsTheDocument is the other half of the O(1)
// promise: it is a promise about the hot path, not a licence to draw a blank
// window. A pool the fast path cannot serve is repaired by exactly one
// self-healing refresh — and the reader still sees the document.
func TestWheelOnAColdCacheStillDrawsTheDocument(t *testing.T) {
	m := buildScrollableModel()
	// Simulate a cold pool: a model whose cache was never populated.
	m.scrollDocLines = nil
	m.lastScrollTotal = 0
	if m.renderedLineCacheWarm() {
		t.Fatal("precondition: the cache should read cold")
	}

	out, cmd, handled := m.interceptWheel(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if !handled {
		t.Fatal("a wheel notch on a cold cache was not handled")
	}
	if cmd != nil {
		t.Error("the fast path emitted a command on the cold path")
	}
	if out == nil {
		t.Fatal("the cold path returned a nil model")
	}
	if !m.renderedLineCacheWarm() {
		t.Error("the self-healing refresh did not repopulate the rendered line cache")
	}
}

// TestWheelBurstIsCoalescedToZeroFrames is the "nothing this path emits can
// generate work for the next event" clause: a burst of notches is N offset
// mutations and ZERO scheduled frames, so a trackpad fling cannot make the UI
// goroutine render N times.
func TestWheelBurstIsCoalescedToZeroFrames(t *testing.T) {
	m := buildScrollableModel()
	m.streaming = true

	const notches = 32
	commands := 0
	for range notches {
		if _, cmd, _ := m.interceptWheel(tea.MouseMsg{Button: tea.MouseButtonWheelUp}); cmd != nil {
			commands++
		}
	}
	if commands != 0 {
		t.Errorf("a burst of %d notches produced %d commands; a burst must coalesce to zero frames",
			notches, commands)
	}
	if m.docScrollOffset != 0 {
		t.Errorf("%d upward notches from the tail landed at %d, want the clamped top", notches, m.docScrollOffset)
	}
	if m.refreshScheduled {
		t.Error("a wheel burst armed a repaint")
	}
}
