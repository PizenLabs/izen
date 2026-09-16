// Package format provides shared human-readable TUI formatting helpers.
package format

import (
	"fmt"
	"time"
)

// FormatDuration renders a duration in human-readable TUI form:
//
//	<60s   → "0.0s", "45.7s" (one decimal)
//	>=60s  → "1m 00s", "9m 29s" (minutes + zero-padded seconds)
//	>=1h   → "1h 01m" (hours + zero-padded minutes)
//
// Human-Readable Duration Invariant: all TUI status bars and execution trace
// timers MUST format durations > 60 seconds into Xm Ys form.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	totalSecs := int(d.Round(time.Second).Seconds())
	if totalSecs < 3600 {
		m := totalSecs / 60
		s := totalSecs % 60
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	h := totalSecs / 3600
	m := (totalSecs % 3600) / 60
	return fmt.Sprintf("%dh %02dm", h, m)
}
