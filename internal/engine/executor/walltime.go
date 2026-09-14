// Package executor provides execution wall-timer metadata helpers for the
// agentic workflow. Every tool output carries live wall-clock metadata so
// execution blocks stay truthful about how long they ran and under which
// timeout bound they executed.
package executor

import (
	"fmt"
	"time"
)

// FormatWallMeta renders the wall-timer metadata suffix appended to every
// execution block: ⟨Wall: X.XXs | Timeout: XXXs⟩. Wall is the measured
// elapsed time; timeout is the bound the execution ran under (seconds).
// Negative durations clamp to zero; the output is pure text (styling is a
// TUI-layer concern) so headless/engine consumers stay ANSI-free.
func FormatWallMeta(wall time.Duration, timeout time.Duration) string {
	if wall < 0 {
		wall = 0
	}
	if timeout < 0 {
		timeout = 0
	}
	return fmt.Sprintf("⟨Wall: %.2fs | Timeout: %ds⟩", wall.Seconds(), int(timeout.Seconds()))
}

// ElapsedWall returns time.Since(start), clamped to >= 0. It returns 0 when
// start is zero (clock never armed) so callers never render a negative or
// nonsensical wall time.
func ElapsedWall(start time.Time) time.Duration {
	if start.IsZero() {
		return 0
	}
	d := time.Since(start)
	if d < 0 {
		return 0
	}
	return d
}

// FormatWallSince is the convenience wrapper used at execution-block render
// time: it measures the wall clock from start and formats it against the
// given timeout bound.
func FormatWallSince(start time.Time, timeout time.Duration) string {
	return FormatWallMeta(ElapsedWall(start), timeout)
}
