package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ── ThinkingBuffer ─────────────────────────────────────────────────────────
//
// ThinkingBuffer is the event-driven reasoning projection. It is fed by
// EventReasoningStream domain events (never by raw string parsing of the
// response pipeline), so thinking content is structurally incapable of mixing
// with or corrupting the final answer.
//
// While the reasoning block is still streaming it renders an auto-scrolling
// dimmed/italic box with a "│ Thinking…" gutter; the moment the terminal
// IsComplete event arrives it collapses to a single compact line
// ("Thought: 1.2s").
type ThinkingBuffer struct {
	mu       sync.Mutex
	builder  strings.Builder
	complete bool
	started  time.Time
	maxLines int
}

// NewThinkingBuffer constructs an empty reasoning buffer.
func NewThinkingBuffer() *ThinkingBuffer {
	return &ThinkingBuffer{
		started:  time.Now(),
		maxLines: 8,
	}
}

// Append adds a verbatim reasoning chunk. If the buffer was already marked
// complete, the completion flag is cleared and the timer restarts so a new
// reasoning block starts fresh.
func (tb *ThinkingBuffer) Append(chunk string) {
	if chunk == "" {
		return
	}
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if tb.complete {
		tb.builder.Reset()
		tb.complete = false
		tb.started = time.Now()
	}
	tb.builder.WriteString(chunk)
}

// MarkComplete marks the reasoning block as complete so Render collapses to
// compact mode.
func (tb *ThinkingBuffer) MarkComplete() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.complete = true
}

// Complete reports whether the reasoning block has been marked complete.
func (tb *ThinkingBuffer) Complete() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.complete
}

// Len returns the number of reasoning bytes accumulated.
func (tb *ThinkingBuffer) Len() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.builder.Len()
}

// String returns the full reasoning text.
func (tb *ThinkingBuffer) String() string {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.builder.String()
}

// Reset clears the buffer and restarts the timer.
func (tb *ThinkingBuffer) Reset() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.builder.Reset()
	tb.complete = false
	tb.started = time.Now()
}

// Elapsed returns the duration since the reasoning block started.
func (tb *ThinkingBuffer) Elapsed() time.Duration {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return time.Since(tb.started)
}

// Render produces the thinking block. It returns "" when there is nothing to
// show. While streaming (streaming=true and not complete) it renders an
// auto-scrolling dimmed/italic box bounded to maxLines; once the reasoning
// block is complete (or the stream is over) it collapses to a single compact
// line: "Thought: 1.2s".
func (tb *ThinkingBuffer) Render(width int, streaming bool, spinner string) string {
	tb.mu.Lock()
	content := tb.builder.String()
	complete := tb.complete
	elapsed := time.Since(tb.started)
	tb.mu.Unlock()

	if content == "" {
		return ""
	}

	// ── Compact mode (IsComplete or stream over) ─────────────────────────
	if complete || !streaming {
		return thinkingStyle.Render(fmt.Sprintf("Thought: %s", formatElapsed(elapsed)))
	}

	if width < 40 {
		width = 40
	}

	availWidth := width - 6
	if availWidth < 10 {
		availWidth = 10
	}

	// Sanitize before wrapping so reasoning text with literal \n / \t / \"
	// escapes expands to real characters (idempotent — safe on every tick).
	content = sanitizeText(content)

	// Auto-scroll: keep only the most recent maxLines worth of lines so the
	// box tracks the tail of the reasoning stream.
	lines := strings.Split(content, "\n")
	var displayed []string
	if len(lines) > tb.maxLines {
		lines = lines[len(lines)-tb.maxLines:]
	}
	for _, line := range lines {
		line = strings.TrimRight(line, " \r")
		if line == "" {
			displayed = append(displayed, "")
			continue
		}
		displayed = append(displayed, wrapString(line, availWidth)...)
	}

	elapsedStr := formatElapsed(elapsed)

	linesOut := make([]string, 0, len(displayed)+2)
	if spinner != "" {
		linesOut = append(linesOut, thinkingStyle.Render(fmt.Sprintf("%s Thinking… %s", spinner, mutedStyle.Render(elapsedStr))))
	} else {
		linesOut = append(linesOut, thinkingStyle.Render(fmt.Sprintf("│ Thinking… %s", mutedStyle.Render(elapsedStr))))
	}
	for _, line := range displayed {
		if line == "" {
			linesOut = append(linesOut, thinkingStyle.Render("│"))
		} else {
			linesOut = append(linesOut, thinkingStyle.Render("│ "+line))
		}
	}

	return strings.Join(linesOut, "\n")
}

// thinkingStyle is the dimmed/italic reasoning style. Reasoning must read as a
// distinct, subordinate stream — never as part of the answer. The muted gray
// foreground guarantees the dim look even on terminals that ignore the Faint
// SGR attribute (where faint alone would render at full brightness).
var thinkingStyle = lipgloss.NewStyle().Faint(true).Italic(true).Foreground(lipgloss.Color(colorMuted))
