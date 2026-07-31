package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/modes"
)

// TestStreamThrottlePreservesContent guards the StreamThrottle remainder bug:
// Flush() used to Reset() the whole buffer before slicing, so the tail of every
// frame window was permanently discarded and long responses rendered truncated.
// Every word of a streamed response must survive chunked writes + frame-paced
// flushes + the stream-end drain, in order.
func TestStreamThrottlePreservesContent(t *testing.T) {
	full := "Go, also known as Golang, is an open-source programming language. " +
		"It's designed for simplicity, efficiency, and concurrency. " +
		"It has a strong typing system and a rich standard library."

	st := NewStreamThrottle()
	var got strings.Builder
	for _, chunk := range splitEvery(full, 13) {
		st.Write(chunk)
		if emitted, ok := st.Flush(); ok {
			got.WriteString(emitted)
		}
	}
	// Stream-end drain — mirrors streamDoneMsg so the residual tail is kept.
	if emitted, ok := st.Flush(); ok {
		got.WriteString(emitted)
	}
	got.WriteString(st.Drain())

	for _, word := range strings.Fields(full) {
		if !strings.Contains(got.String(), word) {
			t.Errorf("word lost in throttle pipeline: %q\n  full: %s\n  got:  %s", word, full, got.String())
		}
	}
}

// TestEmitVisibleContentKeepsEveryWord exercises the real emission path used by
// the smooth-tick loop and the streamDoneMsg drain: raw windows flow through
// reasoning extraction and into currentStreamContent, and every word survives —
// including leftover legacy buffer content around the emitted window.
func TestEmitVisibleContentKeepsEveryWord(t *testing.T) {
	full := "Hello! How can I help you today? Go, also known as Golang, is an " +
		"open-source programming language designed for simplicity and efficiency."
	m := &model{
		width:         80,
		resolver:      modes.NewResolver(),
		thinkingPanel: NewThinkingPanel(),
	}

	// The throttle path: streamBuffer stays empty, content arrives via the
	// throttle window.
	st := NewStreamThrottle()
	for _, chunk := range splitEvery(full, 17) {
		st.Write(chunk)
		emitted, ok := st.Flush()
		if !ok {
			continue
		}
		m.emitVisibleContent(emitted)
	}
	// Stream-end drain path.
	m.emitVisibleContent(st.Drain())

	for _, word := range strings.Fields(full) {
		if !strings.Contains(m.currentStreamContent, word) {
			t.Errorf("word lost in emission pipeline: %q\n  content=%q", word, m.currentStreamContent)
		}
	}
}

// TestEmitVisibleContentPreservesLegacyLeftover ensures the non-throttle path
// (streamBuffer fed directly) does not lose the un-emitted remainder when a
// window is emitted around it: the leftover stays buffered for the next tick
// rather than being wiped by the emission pass.
func TestEmitVisibleContentPreservesLegacyLeftover(t *testing.T) {
	m := &model{width: 80, resolver: modes.NewResolver(), thinkingPanel: NewThinkingPanel()}
	m.streamBuffer = "leftover-pending-content "

	emit := m.streamBuffer[:8]          // "leftover"
	m.streamBuffer = m.streamBuffer[8:] // "-pending-content "
	m.emitVisibleContent(emit)

	if !strings.Contains(m.currentStreamContent, "leftover") {
		t.Errorf("emitted window dropped: %q", m.currentStreamContent)
	}
	// The un-emitted remainder must still be buffered, not wiped.
	if m.streamBuffer != "-pending-content " {
		t.Errorf("legacy remainder lost: %q", m.streamBuffer)
	}

	// A later window emits alongside the preserved remainder.
	m.emitVisibleContent("window ")
	if !strings.Contains(m.currentStreamContent, "leftover") ||
		!strings.Contains(m.currentStreamContent, "window") {
		t.Errorf("window lost: %q", m.currentStreamContent)
	}
	if m.streamBuffer != "-pending-content " {
		t.Errorf("legacy remainder lost on second emission: %q", m.streamBuffer)
	}
}

// TestStreamRenderFullTextNoLoss renders a streamed-style prose response (as
// both the live streaming path and the finalized history record do) and checks
// that every word survives ANSI stripping — no truncation, no isolated
// punctuation.
func TestStreamRenderFullTextNoLoss(t *testing.T) {
	full := "Go, also known as Golang, is an open-source programming language. " +
		"It's designed for simplicity, efficiency, and concurrency. " +
		"It has a strong typing system and a rich standard library."
	m := &model{width: 80, resolver: modes.NewResolver()}

	out := m.renderStreamingContent(full, m.width)
	stripped := ansi.Strip(out)
	for _, word := range strings.Fields(full) {
		clean := strings.Trim(word, "*`.,;:()")
		if clean == "" {
			continue
		}
		if !strings.Contains(stripped, clean) {
			t.Errorf("word lost in render: %q\n  out=%q", clean, stripped)
		}
	}
}

func splitEvery(s string, n int) []string {
	var out []string
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}
