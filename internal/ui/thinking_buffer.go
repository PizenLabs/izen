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
	// reasoningTokens is the provider-reported reasoning-token count for the
	// captured thinking block. It is set ONLY from the authoritative
	// ProviderUsage.ReasoningTokens (via streamUsageMsg); when unknown it stays
	// 0 and the compact summary omits the token count entirely rather than
	// deriving one from the reasoning text length.
	reasoningTokens int
	// expanded is the IsThinkingExpanded state: false (default) renders the
	// compact spinner/summary line, true renders the full dimmed reasoning box.
	expanded bool
	// scrollOffset is the reasoning line window start when the user has scrolled
	// up inside the expanded box. It is ignored (auto-scroll to the tail) until
	// the user explicitly scrolls up; ScrolledUp reports that state.
	scrollOffset int
	scrolledUp   bool
	// lastLineCount caches the total wrapped line count from the last Render so
	// HasOverflow is cheap and deterministic without re-wrapping.
	lastLineCount int
}

// NewThinkingBuffer constructs an empty reasoning buffer.
func NewThinkingBuffer() *ThinkingBuffer {
	return &ThinkingBuffer{
		started:  time.Now(),
		maxLines: 10,
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

// SetReasoningTokens records the provider-reported reasoning-token count. It
// is authoritative usage only — never a character-count estimate.
func (tb *ThinkingBuffer) SetReasoningTokens(n int) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.reasoningTokens = n
}

// ReasoningTokens reports the provider-reported reasoning-token count (0 when
// unknown).
func (tb *ThinkingBuffer) ReasoningTokens() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.reasoningTokens
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
	tb.reasoningTokens = 0
	tb.started = time.Now()
	tb.scrollOffset = 0
	tb.scrolledUp = false
	tb.lastLineCount = 0
}

// Elapsed returns the duration since the reasoning block started.
func (tb *ThinkingBuffer) Elapsed() time.Duration {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return time.Since(tb.started)
}

// HasOverflow reports whether the expanded reasoning box exceeds maxLines and
// therefore supports in-box scrolling. Based on the cached line count from the
// last Render; false before any Render call.
func (tb *ThinkingBuffer) HasOverflow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.expanded && tb.lastLineCount > tb.maxLines
}

// ScrolledUp reports whether the user has scrolled up inside the expanded box,
// which suppresses auto-scroll-to-tail while new reasoning tokens stream in.
func (tb *ThinkingBuffer) ScrolledUp() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.scrolledUp
}

// ScrollUp moves the expanded-box window up by amount wrapped lines (clamped).
// It also latches scrolledUp so streaming does not yank the view back to the
// tail while the user reads earlier reasoning. No-op when the box fits.
func (tb *ThinkingBuffer) ScrollUp(amount int) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	maxOffset := tb.lastLineCount - tb.maxLines
	if maxOffset <= 0 {
		return
	}
	if amount <= 0 {
		return
	}
	tb.scrollOffset = min(max(0, tb.scrollOffset-amount), maxOffset)
	tb.scrolledUp = true
}

// ScrollDown moves the expanded-box window down by amount wrapped lines
// (clamped). Reaching the tail clears scrolledUp so auto-scroll resumes.
// No-op when the box fits.
func (tb *ThinkingBuffer) ScrollDown(amount int) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	maxOffset := tb.lastLineCount - tb.maxLines
	if maxOffset <= 0 {
		return
	}
	if amount <= 0 {
		return
	}
	tb.scrollOffset = min(max(0, tb.scrollOffset+amount), maxOffset)
	if tb.scrollOffset >= maxOffset {
		tb.scrolledUp = false
	}
}

// ResetScroll clears any in-box scroll and resumes auto-scroll-to-tail.
func (tb *ThinkingBuffer) ResetScroll() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.scrollOffset = 0
	tb.scrolledUp = false
}

// renderThoughtSummary renders the compact "▸ Thought for Xs" summary line. It
// appends "(N tokens)" ONLY when the provider-reported reasoning-token count is
// known — a character-derived estimate is never presented as a token count.
func renderThoughtSummary(elapsed string, reasoningTokens int) string {
	if reasoningTokens > 0 {
		return fmt.Sprintf("▸ Thought for %s (%d tokens)", elapsed, reasoningTokens)
	}
	return fmt.Sprintf("▸ Thought for %s", elapsed)
}

// Render produces the thinking block. It returns "" when there is nothing to
// show. When the block is collapsed (the default), it renders a compact
// one-line widget: "Thinking.. (Xs)  (Ctrl+O to expand)" while the reasoning
// block is still streaming, collapsing to "▸ Thought for Xs (N tokens)" once
// complete (or the stream is over). When expanded (IsThinkingExpanded), it
// renders the full reasoning text in a dimmed/italic box — auto-scrolling to
// the tail while streaming, but scrollable with j/k (and PgUp/PgDn) once the
// user scrolls up, showing the reasoning bounded to maxLines once complete.
//
// NO-DUPLICATE CONTRACT: the expanded box NEVER re-prints its own
// "Thinking…" status header. That status is owned by the parent indicator —
// the loading dock's "✻ Thinking... (Xs)" line while the dock is live, or the
// collapsed one-liner that the box replaces — so adding a header here would
// stack two "Thinking…" lines in the viewport. The box renders the reasoning
// window plus an optional scroll affordance footer only.
func (tb *ThinkingBuffer) Render(width int, streaming bool, spinner string) string {
	tb.mu.Lock()
	content := tb.builder.String()
	complete := tb.complete
	expanded := tb.expanded
	reasoningTokens := tb.reasoningTokens
	elapsed := time.Since(tb.started)
	scrollOffset := tb.scrollOffset
	scrolledUp := tb.scrolledUp
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

		lines := strings.Split(content, "\n")
		var allLines []string
		for _, line := range lines {
			line = strings.TrimRight(line, " \r")
			if line == "" {
				allLines = append(allLines, "")
				continue
			}
			allLines = append(allLines, wrapString(line, width-6)...)
		}

		total := len(allLines)
		overflow := total > tb.maxLines

		// Auto-scroll while streaming unless the user scrolled up to inspect
		// earlier reasoning (scrolledUp suppresses the tail yank).
		var start int
		if overflow {
			if scrolledUp {
				start = scrollOffset
				// Defensive clamp: content can grow between renders, but it can
				// also shrink (buffer reset); never slice past the new tail.
				if maxOff := total - tb.maxLines; start > maxOff {
					start = maxOff
				}
			} else {
				start = total - tb.maxLines
			}
		}
		end := min(start+tb.maxLines, total)

		// Cache the total line count for cheap HasOverflow checks on keypress.
		tb.mu.Lock()
		tb.lastLineCount = total
		tb.mu.Unlock()

		linesOut := make([]string, 0, tb.maxLines+2)
		// NO header: the "Thinking…" status is owned by the parent indicator
		// (loading dock or collapsed line) — see the NO-DUPLICATE CONTRACT
		// above. The box starts directly with the reasoning window.
		// Explicit newline separation: headerStr + "\n" + thinkingBody — the
		// expanded thought text must render on its own physical line with
		// consistent indentation and dimmed styling, never concatenated onto
		// the status/policy header row.
		for _, line := range allLines[start:end] {
			if line == "" {
				linesOut = append(linesOut, thinkingStyle.Render("│"))
			} else {
				linesOut = append(linesOut, thinkingStyle.Render("│ "+line))
			}
		}
		if complete {
			linesOut = append(linesOut, thinkingStyle.Render("│ "+mutedStyle.Render(
				renderThoughtSummary(elapsedStr, reasoningTokens))))
		}
		if overflow {
			// In-box scroll affordance footer. It only appears when the box
			// actually overflows, so it never distracts on short reasoning.
			hint := "Ctrl+O collapse"
			if !complete {
				hint = "Ctrl+O collapse · j/k scroll"
			}
			linesOut = append(linesOut, thinkingStyle.Render("│ "+mutedStyle.Render(
				fmt.Sprintf("%s · %d/%d", hint, start+1, total))))
		}

		return strings.Join(linesOut, "\n")
	}

	// ── Collapsed (default) ────────────────────────────────────────────
	// While the reasoning block is still streaming show a compact spinner
	// plus a faint expand hint; once it finishes (terminal event or stream
	// over) collapse into a single-line summary.
	if streaming && !complete {
		sp := spinner
		if sp == "" {
			sp = SpinnerSnowflake()
		}
		return thinkingStyle.Render(fmt.Sprintf("%s Thinking.. (%s)  %s",
			sp, elapsedStr, mutedStyle.Render("[Ctrl+O to expand]")))
	}
	return thinkingStyle.Render(renderThoughtSummary(elapsedStr, reasoningTokens))
}

// thinkingStyle is the dimmed/italic reasoning style. Reasoning must read as a
// distinct, subordinate stream — never as part of the answer. The muted gray
// foreground guarantees the dim look even on terminals that ignore the Faint
// SGR attribute (where faint alone would render at full brightness).
var thinkingStyle = lipgloss.NewStyle().Faint(true).Italic(true).Foreground(lipgloss.Color(colorMuted))
