package ui

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TraceStep records one demuxed internal execution loop telemetry event.
type TraceStep struct {
	Timestamp time.Time
	Category  string        // e.g. "[loop]", "[grant]", "[runtime]", "[preflight]"
	Message   string        // raw detail message
	Duration  time.Duration // step latency if detected
}

// TelemetryDemuxer routes internal execution loop events away from the
// primary conversation viewport into an isolated Trace Buffer, rendering
// a single-line collapsible status anchor in the primary stream.
type TelemetryDemuxer struct {
	mu        sync.RWMutex
	steps     []TraceStep
	startTime time.Time
	lastTime  time.Time
	turnID    uint64
	active    bool
}

// NewTelemetryDemuxer creates a new thread-safe TelemetryDemuxer.
func NewTelemetryDemuxer() *TelemetryDemuxer {
	return &TelemetryDemuxer{
		startTime: time.Now(),
		lastTime:  time.Now(),
	}
}

// Reset clears the buffer for a fresh turn.
func (d *TelemetryDemuxer) Reset(turnID uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.steps = nil
	d.startTime = time.Now()
	d.lastTime = time.Now()
	d.turnID = turnID
	d.active = false
}

var (
	traceMicroSecRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(µs|us|ms|s)`)
	ansiRegex       = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)
)

var _ = ansiRegex

// Ingest intercepts an incoming record text. If it is internal execution
// telemetry, it parses and buffers the step in the Trace Buffer and returns
// isTelemetry = true along with the single-line summary anchor.
func (d *TelemetryDemuxer) Ingest(text string) (bool, string) {
	if !isEngineTraceLine(text) && !isExecutionLoopTrace(text) {
		return false, ""
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if len(d.steps) == 0 {
		d.startTime = now
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		cat := extractCategory(trimmed)
		dur := extractDuration(trimmed)
		if dur == 0 && !d.lastTime.IsZero() {
			dur = now.Sub(d.lastTime)
		}
		d.steps = append(d.steps, TraceStep{
			Timestamp: now,
			Category:  cat,
			Message:   trimmed,
			Duration:  dur,
		})
	}
	d.lastTime = now
	d.active = true

	anchor := d.formatAnchorLocked()
	return true, anchor
}

func isExecutionLoopTrace(s string) bool {
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "[loop]"):
		return true
	case strings.HasPrefix(lower, "[grant]"):
		return true
	case strings.HasPrefix(lower, "[runtime]"):
		return true
	case strings.Contains(lower, "observing -> deciding"):
		return true
	case strings.Contains(lower, "grant-"):
		return true
	}
	return false
}

func extractCategory(s string) string {
	if strings.HasPrefix(s, "[") {
		idx := strings.Index(s, "]")
		if idx > 0 {
			return s[:idx+1]
		}
	}
	if strings.HasPrefix(s, "▸") {
		return "[trace]"
	}
	return "[engine]"
}

func extractDuration(s string) time.Duration {
	m := traceMicroSecRe.FindStringSubmatch(s)
	if len(m) == 3 {
		val := m[1]
		unit := m[2]
		switch unit {
		case "µs", "us":
			var d float64
			if _, err := fmt.Sscanf(val, "%f", &d); err != nil {
				return 0
			}
			return time.Duration(d * float64(time.Microsecond))
		case "ms":
			var d float64
			if _, err := fmt.Sscanf(val, "%f", &d); err != nil {
				return 0
			}
			return time.Duration(d * float64(time.Millisecond))
		case "s":
			var d float64
			if _, err := fmt.Sscanf(val, "%f", &d); err != nil {
				return 0
			}
			return time.Duration(d * float64(time.Second))
		}
	}
	return 0
}

func (d *TelemetryDemuxer) formatAnchorLocked() string {
	count := len(d.steps)
	if count == 0 {
		return "▸ Trace (0 steps) · Press Alt+T to expand"
	}
	totalDur := time.Since(d.startTime)
	durStr := formatTraceDuration(totalDur)
	return fmt.Sprintf("▸ Trace (%d steps · %s) · Press Alt+T to expand", count, durStr)
}

// AnchorText returns the current formatted anchor text.
func (d *TelemetryDemuxer) AnchorText() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.formatAnchorLocked()
}

// StepCount returns number of buffered steps.
func (d *TelemetryDemuxer) StepCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.steps)
}

// TotalDuration returns duration across all buffered steps.
func (d *TelemetryDemuxer) TotalDuration() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.steps) == 0 {
		return 0
	}
	return time.Since(d.startTime)
}

// Steps returns a copy of all buffered steps.
func (d *TelemetryDemuxer) Steps() []TraceStep {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]TraceStep, len(d.steps))
	copy(out, d.steps)
	return out
}

func formatTraceDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// RenderOverlay renders the expanded modal/drawer overlay for debugging telemetry.
func (d *TelemetryDemuxer) RenderOverlay(width, height int) string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	modalW := int(float64(width) * 0.85)
	if modalW > 90 {
		modalW = 90
	}
	if modalW < 50 {
		modalW = width - 4
		if modalW < 40 {
			modalW = 40
		}
	}

	modalH := int(float64(height) * 0.75)
	if modalH > 24 {
		modalH = 24
	}
	if modalH < 10 {
		modalH = 10
	}

	var b strings.Builder
	title := fmt.Sprintf("┌─ Telemetry Trace Buffer (%d steps · %s) ─", len(d.steps), formatTraceDuration(time.Since(d.startTime)))
	dashLen := modalW - lipgloss.Width(title) - 1
	if dashLen < 0 {
		dashLen = 0
	}
	b.WriteString(title + strings.Repeat("─", dashLen) + "┐\n")

	contentH := modalH - 4
	startIdx := 0
	if len(d.steps) > contentH {
		startIdx = len(d.steps) - contentH
	}

	for i := startIdx; i < len(d.steps); i++ {
		step := d.steps[i]
		ts := step.Timestamp.Format("15:04:05.000")
		stepDur := ""
		if step.Duration > 0 {
			stepDur = fmt.Sprintf(" (%s)", formatTraceDuration(step.Duration))
		}
		rawMsg := step.Message
		maxMsgW := modalW - 24 - len(stepDur)
		if maxMsgW < 10 {
			maxMsgW = 10
		}
		if lipgloss.Width(rawMsg) > maxMsgW {
			rawMsg = ansi.Truncate(rawMsg, maxMsgW, "…")
		}

		line := fmt.Sprintf("│ %s %-8s %s%s", ts, step.Category, rawMsg, stepDur)
		pad := modalW - lipgloss.Width(line) - 1
		if pad < 0 {
			pad = 0
		}
		b.WriteString(line + strings.Repeat(" ", pad) + "│\n")
	}

	// Pad remaining rows
	renderedRows := len(d.steps) - startIdx
	for i := renderedRows; i < contentH; i++ {
		b.WriteString("│" + strings.Repeat(" ", modalW-2) + "│\n")
	}

	footer := "│ [Esc / Alt+T / Ctrl+O] Close Trace Overlay"
	padFoot := modalW - lipgloss.Width(footer) - 1
	if padFoot < 0 {
		padFoot = 0
	}
	b.WriteString(footer + strings.Repeat(" ", padFoot) + "│\n")
	b.WriteString("└" + strings.Repeat("─", modalW-2) + "┘")

	box := lipgloss.NewStyle().
		BorderForeground(lipgloss.Color(colorBlue)).
		Foreground(lipgloss.Color(colorText)).
		Render(b.String())

	return box
}
