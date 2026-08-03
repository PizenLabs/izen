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
// The block is collapsible: by default (IsThinkingExpanded = false) it renders
// a compact one-line widget — a "Thinking.. (Xs)" spinner while the reasoning
// block is still streaming, collapsing to a "▸ Thought for Xs (N tokens)"
// summary once it completes. Toggling expansion (Ctrl+O / Alt+O) renders the
// full reasoning text in a subtle dimmed/italic box.
type ThinkingBuffer struct {
	mu       sync.Mutex
	builder  strings.Builder
	complete bool
	started  time.Time
	maxLines int
	// expanded is the IsThinkingExpanded state: false (default) renders the
	// compact spinner/summary line, true renders the full dimmed reasoning box.
	expanded bool
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

// Toggle flips the IsThinkingExpanded state of the thought block.
func (tb *ThinkingBuffer) Toggle() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.expanded = !tb.expanded
}

// SetExpanded forces the IsThinkingExpanded state of the thought block.
func (tb *ThinkingBuffer) SetExpanded(v bool) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.expanded = v
}

// Expanded reports the IsThinkingExpanded state of the thought block.
func (tb *ThinkingBuffer) Expanded() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.expanded
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
	tb.expanded = false
	tb.started = time.Now()
}

// Elapsed returns the duration since the reasoning block started.
func (tb *ThinkingBuffer) Elapsed() time.Duration {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return time.Since(tb.started)
}

// estimateTokens is a cheap deterministic proxy for "N tokens" in the compact
// summary line. It is display-only and never used for billing/accounting.
func estimateTokens(text string) int {
	t := len(text) / 4
	if t < 1 {
		t = 1
	}
	return t
}

// Render produces the thinking block. It returns "" when there is nothing to
// show. When the block is collapsed (the default), it renders a compact
// one-line widget: "Thinking.. (Xs)" while the reasoning block is still
// streaming, collapsing to "▸ Thought for Xs (N tokens)" once complete (or the
// stream is over). When expanded (IsThinkingExpanded), it renders the full
// reasoning text in a dimmed/italic box — auto-scrolling to the tail while
// streaming, showing the reasoning bounded to maxLines once complete.
func (tb *ThinkingBuffer) Render(width int, streaming bool, spinner string) string {
	tb.mu.Lock()
	content := tb.builder.String()
	complete := tb.complete
	expanded := tb.expanded
	elapsed := time.Since(tb.started)
	tb.mu.Unlock()

	if content == "" {
		return ""
	}

	elapsedStr := formatElapsed(elapsed)

	// ── Expanded: full reasoning box (dimmed/faint) ───────────────────
	if expanded {
		if width < 40 {
			width = 40
		}

		// Sanitize before wrapping so reasoning text with literal \n / \t / \"
		// escapes expands to real characters (idempotent — safe on every tick).
		content = sanitizeText(content)

		// Auto-scroll while streaming: keep only the most recent maxLines
		// worth of lines so the box tracks the tail of the reasoning stream.
		// Once complete, show the reasoning (still bounded so a giant thought
		// block cannot blow out the viewport).
		lines := strings.Split(content, "\n")
		var displayed []string
		if !complete && len(lines) > tb.maxLines {
			lines = lines[len(lines)-tb.maxLines:]
		}
		for _, line := range lines {
			line = strings.TrimRight(line, " \r")
			if line == "" {
				displayed = append(displayed, "")
				continue
			}
			displayed = append(displayed, wrapString(line, width-6)...)
		}

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
		if complete {
			linesOut = append(linesOut, thinkingStyle.Render(fmt.Sprintf("│ %s", mutedStyle.Render(
				fmt.Sprintf("▸ Thought for %s (%d tokens)", elapsedStr, estimateTokens(content))))))
		}

		return strings.Join(linesOut, "\n")
	}

	// ── Collapsed (default) ────────────────────────────────────────────
	// While the reasoning block is still streaming show a compact spinner;
	// once it finishes (terminal event or stream over) collapse into a
	// single-line summary.
	if streaming && !complete {
		sp := spinner
		if sp == "" {
			sp = "✦"
		}
		return thinkingStyle.Render(fmt.Sprintf("%s Thinking.. (%s)", sp, elapsedStr))
	}
	return thinkingStyle.Render(fmt.Sprintf("▸ Thought for %s (%d tokens)", elapsedStr, estimateTokens(content)))
}

// thinkingStyle is the dimmed/italic reasoning style. Reasoning must read as a
// distinct, subordinate stream — never as part of the answer. The muted gray
// foreground guarantees the dim look even on terminals that ignore the Faint
// SGR attribute (where faint alone would render at full brightness).
var thinkingStyle = lipgloss.NewStyle().Faint(true).Italic(true).Foreground(lipgloss.Color(colorMuted))
