package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// AnimationConfig controls the per-tick character release for progressive
// typewriter-style rendering. Tuned for 25ms ticks at 3 characters per frame
// yields ~120 chars/sec — readable without visual stutter.
type AnimationConfig struct {
	CharsPerFrame int
	TickInterval  time.Duration
}

func DefaultAnimationConfig() AnimationConfig {
	return AnimationConfig{
		CharsPerFrame: 3,
		TickInterval:  25 * time.Millisecond,
	}
}

// AnimBuffer holds a queue of pre-styled ANSI lines and releases their
// visible characters incrementally per Tick() call. Each line is a complete
// ANSI-styled string produced by IncrementalStreamParser. The buffer splits
// at visible-character boundaries using ansi.Truncate so ANSI escape sequences
// are never broken mid-sequence.
//
// Zero-value is not usable — use NewAnimBuffer.
type AnimBuffer struct {
	config   AnimationConfig
	pending  []string // fully styled lines waiting to be revealed
	lineIdx  int      // index of line currently being revealed
	charPos  int      // visible character position within the current line
	revealed []string // fully revealed lines

	cacheValid   bool   // cache recomputed after every state mutation
	cachedJoined string // pre-joined VisibleLines result, safe for direct use
}

func NewAnimBuffer(cfg AnimationConfig) *AnimBuffer {
	return &AnimBuffer{
		config:   cfg,
		pending:  make([]string, 0, 64),
		revealed: make([]string, 0, 64),
	}
}

// QueueLines adds pre-styled lines to the end of the animation queue.
func (b *AnimBuffer) QueueLines(lines []string) {
	b.pending = append(b.pending, lines...)
	b.cacheValid = false
}

// Tick advances the character release position by CharsPerFrame and returns
// whether any visible line state changed. Callers should trigger a re-render
// when true.
func (b *AnimBuffer) Tick() bool {
	if b.lineIdx >= len(b.pending) {
		return false
	}

	changed := false
	remaining := b.config.CharsPerFrame

	for remaining > 0 && b.lineIdx < len(b.pending) {
		line := b.pending[b.lineIdx]
		lineLen := ansi.StringWidth(line)

		if b.charPos >= lineLen {
			b.revealed = append(b.revealed, line)
			b.lineIdx++
			b.charPos = 0
			changed = true
			continue
		}

		avail := lineLen - b.charPos
		advance := remaining
		if advance > avail {
			advance = avail
		}
		b.charPos += advance
		remaining -= advance
		changed = true

		if b.charPos >= lineLen {
			b.revealed = append(b.revealed, line)
			b.lineIdx++
			b.charPos = 0
		}
	}

	if changed {
		b.cacheValid = false
	}

	return changed
}

// JoinedContent returns the current visible content as a single pre-joined
// string. The result is cached and recomputed only when the buffer state
// changes, avoiding repeated ansi.Truncate calls on the render path.
func (b *AnimBuffer) JoinedContent() string {
	if !b.cacheValid {
		visible := b.VisibleLines()
		b.cachedJoined = strings.Join(visible, "\n")
		b.cacheValid = true
	}
	return b.cachedJoined
}

// VisibleLines returns the current set of lines that should be displayed:
// fully revealed lines plus the partially revealed current line (if any).
func (b *AnimBuffer) VisibleLines() []string {
	if len(b.revealed) == 0 && b.lineIdx >= len(b.pending) {
		return nil
	}

	total := len(b.revealed)
	if b.lineIdx < len(b.pending) {
		total++
	}

	out := make([]string, 0, total)
	out = append(out, b.revealed...)

	if b.lineIdx < len(b.pending) {
		if b.charPos > 0 {
			partial := b.pending[b.lineIdx]
			visible := ansi.Truncate(partial, b.charPos, "")
			out = append(out, visible)
		}
	}

	return out
}

// IsAnimating returns true when there are pending lines yet to be revealed.
func (b *AnimBuffer) IsAnimating() bool {
	return b.lineIdx < len(b.pending)
}

// Flush immediately reveals all remaining pending content and returns the
// complete set of lines.
func (b *AnimBuffer) Flush() []string {
	for b.lineIdx < len(b.pending) {
		b.revealed = append(b.revealed, b.pending[b.lineIdx])
		b.lineIdx++
	}
	b.charPos = 0
	b.cacheValid = false
	out := make([]string, len(b.revealed))
	copy(out, b.revealed)
	return out
}

// Reset clears all state. Lines are not retained.
func (b *AnimBuffer) Reset() {
	b.pending = b.pending[:0]
	b.revealed = b.revealed[:0]
	b.lineIdx = 0
	b.charPos = 0
	b.cacheValid = false
}
