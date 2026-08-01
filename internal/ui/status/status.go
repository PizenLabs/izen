package status

import (
	"fmt"
	"math"
	"sync"
)

// ── Cloud Usage Tracker ───────────────────────────────────────────────────
//
// The Tracker is the single source of truth for the LAST reported cloud
// provider token usage. It is written exactly once per stream (at EOF, from
// the provider's real usage metadata) and read by the footer/status renderers
// so the token figures on screen strictly reflect what the provider reported —
// never a local character-count estimate.
type Tracker struct {
	mu       sync.RWMutex
	hasUsage bool
	input    int
	output   int
	total    int
}

// New returns an empty tracker with no recorded usage.
func New() *Tracker { return &Tracker{} }

// Default is the package-level global tracker shared by the stream consumer
// (writer) and the footer/status renderers (readers). It is the "global
// context state" that carries the provider-reported usage across package
// boundaries without an explicit dependency on the model struct.
var Default = New()

// Record stores the provider-reported usage. A zero input/output (provider
// returned no usage metadata) is accepted so callers can intentionally clear
// stale values; hasUsage is set regardless of magnitude.
func (t *Tracker) Record(input, output int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hasUsage = true
	t.input = input
	t.output = output
	t.total = input + output
}

// Reset clears all recorded usage back to the empty state.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hasUsage = false
	t.input = 0
	t.output = 0
	t.total = 0
}

// Has reports whether any usage has been recorded since the last Reset.
func (t *Tracker) Has() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hasUsage
}

// Input returns the recorded prompt/input token count.
func (t *Tracker) Input() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.input
}

// Output returns the recorded completion/output token count.
func (t *Tracker) Output() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.output
}

// Total returns the recorded total token count (input + output).
func (t *Tracker) Total() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.total
}

// Snapshot is an immutable copy of the tracker's current state.
type Snapshot struct {
	Has    bool
	Input  int
	Output int
	Total  int
}

// Snapshot returns a consistent, race-free copy of the usage state.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return Snapshot{
		Has:    t.hasUsage,
		Input:  t.input,
		Output: t.output,
		Total:  t.total,
	}
}

// FormatTokens renders a raw token count in compact human units:
//
//	0..999        → "840"
//	1,000..9,999  → "8.4k"
//	10,000+       → "25k"
func FormatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%dk", n/1000)
}

// FormatUsage renders the input/output token pair in the footer format:
//
//	"↓8.4k + ↑1.2k tok"
//
// The glyphs label the split: ↓ = input (prompt) tokens, ↑ = output
// (completion) tokens. When only a total is meaningful (no split available)
// it falls back to "9.6k tok". Returns "" when no usage has been recorded.
func FormatUsage(s Snapshot) string {
	if !s.Has {
		return ""
	}
	if s.Input == 0 && s.Output == 0 {
		return fmt.Sprintf("%s tok", FormatTokens(s.Total))
	}
	return fmt.Sprintf("↓%s + ↑%s tok", FormatTokens(s.Input), FormatTokens(s.Output))
}

// FormatUsageValues is the stateless variant used by renderers that already
// hold the raw input/output values (e.g. the model's accumulated counters).
// The ↓/↑ glyphs label input (prompt) and output (completion) tokens.
func FormatUsageValues(input, output int) string {
	return fmt.Sprintf("↓%s + ↑%s tok", FormatTokens(input), FormatTokens(output))
}

// FormatUsageContext renders token usage against the model's context window
// as a compact percentage line, matching modern TUI status-bar conventions:
//
//	"↓2.3k + ↑1.5k tok (3%)" — provider-reported input/output split
//	"3.8k tok (3%)"           — total-only fallback (no split available)
//	"0 tok (0%)"              — zero / no usage recorded
//
// The ↓/↑ glyphs label input (prompt) and output (completion) tokens.
// When the context window is unknown (contextLimit <= 0) the percentage
// suffix is omitted so the line never shows a meaningless "0%".
func FormatUsageContext(input, output, total, contextLimit int) string {
	var base string
	if input > 0 || output > 0 {
		base = fmt.Sprintf("↓%s + ↑%s tok", FormatTokens(input), FormatTokens(output))
	} else {
		base = fmt.Sprintf("%s tok", FormatTokens(total))
	}
	if contextLimit <= 0 {
		return base
	}
	used := total
	if used <= 0 {
		used = input + output
	}
	pct := int(math.Round(float64(used) / float64(contextLimit) * 100))
	return fmt.Sprintf("%s (%d%%)", base, pct)
}
