package ui

import (
	"strings"
	"testing"
	"time"

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

// TestCommandBlockStreamingGate guards the Command-widget streaming gate: an
// INCOMPLETE bash/sh block (no closing fence yet) must NOT render the opening
// fence as a command line, must NOT drop the last line (it is not a closing
// fence), and must never emit the command text twice. The full Command widget
// is produced exactly once — after the closing fence arrives.
func TestCommandBlockStreamingGate(t *testing.T) {
	m := &model{width: 80, resolver: modes.NewResolver()}
	m.streaming = true

	// Stage 1: fence + two command lines, NO closing fence (mid-stream).
	acc := "```bash\ncat > Hello.java\necho done"
	out := m.renderStreamingContent(acc, m.width)
	stripped := ansi.Strip(out)

	if strings.Contains(stripped, "$ ```bash") {
		t.Errorf("opening fence leaked as a command line during streaming:\n%s", stripped)
	}
	if !strings.Contains(stripped, "cat > Hello.java") || !strings.Contains(stripped, "echo done") {
		t.Errorf("streaming raw passthrough dropped command lines:\n%s", stripped)
	}
	if strings.Count(stripped, "cat > Hello.java") > 1 {
		t.Errorf("command text duplicated during streaming:\n%s", stripped)
	}

	// Stage 2: closing fence arrives → the Command widget renders exactly once.
	complete := acc + "\n```"
	out = m.renderStreamingContent(complete, m.width)
	stripped = ansi.Strip(out)

	if !strings.Contains(stripped, "❯ Command") {
		t.Errorf("completed block did not render the Command widget:\n%s", stripped)
	}
	if n := strings.Count(stripped, "cat > Hello.java"); n != 1 {
		t.Errorf("'cat > Hello.java' appears %d times in completed widget, want exactly 1:\n%s", n, stripped)
	}
	if n := strings.Count(stripped, "echo done"); n != 1 {
		t.Errorf("'echo done' appears %d times in completed widget, want exactly 1:\n%s", n, stripped)
	}
}

// TestCommandBlockFenceNeverLeaksDuringIncrementalGrowth grows a bash command
// block one rune at a time (worst-case token alignment) and asserts the
// command text is never duplicated in ANY intermediate render, and the fence
// never renders as a "$ ```bash" command.
func TestCommandBlockFenceNeverLeaksDuringIncrementalGrowth(t *testing.T) {
	m := &model{width: 80, resolver: modes.NewResolver()}
	m.streaming = true

	final := "```bash\ncat > Hello.java\n```"
	var acc strings.Builder
	for _, r := range final {
		acc.WriteRune(r)
		out := m.renderStreamingContent(acc.String(), m.width)
		stripped := ansi.Strip(out)
		if n := strings.Count(stripped, "cat > Hello.java"); n > 1 {
			t.Fatalf("dup at acc=%q count=%d\n%s", acc.String(), n, stripped)
		}
		if strings.Contains(stripped, "$ ```") {
			t.Fatalf("fence leaked as a command at acc=%q\n%s", acc.String(), stripped)
		}
	}

	// Finalized (non-streaming) render must also be exactly once.
	m.streaming = false
	out := m.renderStreamingContent(final, m.width)
	if n := strings.Count(ansi.Strip(out), "cat > Hello.java"); n != 1 {
		t.Fatalf("finalized render has %d occurrences, want 1:\n%s", n, out)
	}
}

// TestIsCommandBlockComplete exercises the completeness predicate used by the
// Command-widget streaming gate.
func TestIsCommandBlockComplete(t *testing.T) {
	complete := []string{
		"```bash\ncat > Hello.java\n```",
		"```sh\ncat > Hello.java\necho done\n```",
		"```\ncat > Hello.java\n```",
	}
	for _, c := range complete {
		if !isCommandBlockComplete(c) {
			t.Errorf("expected complete for %q", c)
		}
	}

	incomplete := []string{
		"```bash",
		"```bash\ncat > Hello.java",
		"```bash\ncat > Hello.java\necho done",
		"cat > Hello.java\n```", // no opening fence
		"",
	}
	for _, c := range incomplete {
		if isCommandBlockComplete(c) {
			t.Errorf("expected incomplete for %q", c)
		}
	}
}

// TestStripCommandFence removes only fence lines, preserving command content.
func TestStripCommandFence(t *testing.T) {
	got := stripCommandFence("```bash\ncat > Hello.java\necho done\n```")
	if got != "cat > Hello.java\necho done" {
		t.Errorf("stripCommandFence = %q, want command lines only", got)
	}
	got = stripCommandFence("```bash\ncat > Hello.java")
	if got != "cat > Hello.java" {
		t.Errorf("stripCommandFence (incomplete) = %q, want 'cat > Hello.java'", got)
	}
}

// TestCommandBlockViewportSingleOccurrence is the end-to-end verification
// guard: a bash command block streamed token-by-token through the real
// throttle + emit + viewport pipeline must show the command text EXACTLY once
// in the rendered viewport at every intermediate stage and in the final view.
func TestCommandBlockViewportSingleOccurrence(t *testing.T) {
	tm := newTestModel()
	m := tm
	m.state = StateChat
	m.streaming = true
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streamThrottle = NewStreamThrottle()
	m.streamThrottle.lastFlush = time.Now().Add(-time.Hour) // bypass the frame-delay gate
	m.streamThrottle.Reset()

	input := "Here is how to create the file:\n```bash\ncat > Hello.java\n```"
	for _, chunk := range splitEvery(input, 4) {
		m.streamThrottle.Write(chunk)
		if emitted, ok := m.streamThrottle.Flush(); ok && emitted != "" {
			m.emitVisibleContent(emitted)
		}
		m.refreshViewportContent()
		view := ansi.Strip(m.Viewport.View())
		if n := strings.Count(view, "cat > Hello.java"); n > 1 {
			t.Fatalf("duplication at acc=%q count=%d\n%s", m.currentStreamContent, n, view)
		}
	}
	// Stream-end drain (mirrors streamDoneMsg) + finalize: the stream flag is
	// cleared BEFORE the accumulated content is committed to history as the AI
	// record (cacheRecordToHistory skips while streaming).
	m.emitVisibleContent(m.streamThrottle.Drain())
	m.streaming = false
	m.push(roleAI, m.currentStreamContent)
	m.refreshViewportContent()
	view := ansi.Strip(m.Viewport.View())

	if n := strings.Count(view, "cat > Hello.java"); n != 1 {
		t.Fatalf("'cat > Hello.java' appears %d times in final viewport, want exactly 1:\n%s", n, view)
	}
}
