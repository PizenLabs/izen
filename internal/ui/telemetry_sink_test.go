package ui

import (
	"testing"
	"time"
)

// TestTelemetrySink_NonBlockingEnqueue proves debug logging never blocks the
// UI thread: even with a saturated channel, enqueue returns within a bounded
// deadline and counts the drop instead of stalling.
func TestTelemetrySink_NonBlockingEnqueue(t *testing.T) {
	// Saturate the channel directly to force the drop path.
	fill := make([]telemetryWrite, 0, cap(telemetryCh))
drain:
	for {
		select {
		case telemetryCh <- telemetryWrite{dir: ".izen/debug", filename: "saturate.log", data: []byte("x\n")}:
			fill = append(fill, telemetryWrite{})
		default:
			break drain
		}
	}
	before := TelemetryDropped()
	start := time.Now()
	enqueueTelemetryWrite(".izen/debug", "plan.log", []byte("non-blocking\n"))
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("enqueueTelemetryWrite blocked UI thread for %s", elapsed)
	}
	if got := TelemetryDropped(); got <= before {
		t.Fatalf("saturated enqueue must drop-and-count (before=%d after=%d)", before, got)
	}
	// Drain what we filled so later tests start clean.
	for range fill {
		select {
		case <-telemetryCh:
		default:
		}
	}
}
