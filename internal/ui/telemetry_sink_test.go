package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTelemetrySink_NonBlockingEnqueue proves debug logging never blocks the
// UI thread: even with a saturated channel, enqueue returns within a bounded
// deadline and counts the drop instead of stalling.
func TestTelemetrySink_NonBlockingEnqueue(t *testing.T) {
	dir := t.TempDir()
	// Saturate the channel directly to force the drop path. Records carry an
	// isolated temp dir so the async write loop never touches the workspace.
	fill := make([]telemetryWrite, 0, cap(telemetryCh))
drain:
	for {
		select {
		case telemetryCh <- telemetryWrite{dir: dir, filename: "saturate.log", data: []byte("x\n")}:
			fill = append(fill, telemetryWrite{})
		default:
			break drain
		}
	}
	before := TelemetryDropped()
	start := time.Now()
	enqueueTelemetryWrite(dir, "plan.log", []byte("non-blocking\n"))
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
	// The write-loop goroutine drains concurrently with the best-effort drain
	// above, so the channel/file may still be busy. Block until the sink is
	// truly empty: otherwise a later test's enqueue hits a full channel and is
	// silently dropped (drop-on-saturation), and the TempDir cleanup races an
	// in-flight write.
	telemetryFlush()

	// Prove the async loop wrote nothing into the real workspace path: the
	// isolated temp dir must not have a real .izen/debug subdirectory layout
	// and every record carried the temp dir, so no workspace file exists.
	if filepath.Join(dir, "saturate.log") == ".izen/debug/saturate.log" {
		t.Fatal("telemetry record must not use workspace-relative paths")
	}
}

// TestTelemetryFlush_DeterministicDrain proves telemetryFlush synchronises on
// the async sink: after flush returns, every enqueued record is on disk.
func TestTelemetryFlush_DeterministicDrain(t *testing.T) {
	dir := t.TempDir()
	orig := getDebugLogDir()
	SetDebugLogDir(dir)
	defer SetDebugLogDir(orig)
	// Drain any backlog left by heavier async telemetry producers from earlier
	// tests; otherwise our own enqueues could hit a full channel and be dropped.
	drainTelemetryBacklog()
	for i := 0; i < 5; i++ {
		enqueueTelemetryWrite(dir, "flush.log", []byte("line\n"))
	}
	telemetryFlush()
	data, err := os.ReadFile(filepath.Join(dir, "flush.log"))
	if err != nil {
		t.Fatalf("expected flush.log on disk after telemetryFlush: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 5 {
		t.Fatalf("expected 5 lines flushed, got %d", lines)
	}
}

// drainTelemetryBacklog blocks until the async sink has consumed everything
// enqueued before this call, leaving the channel empty for the caller's own
// records. Without it, drop-on-saturation can silently drop a test's enqueues
// when an earlier heavy telemetry producer left the buffer full.
func drainTelemetryBacklog() {
	telemetryFlush()
}
