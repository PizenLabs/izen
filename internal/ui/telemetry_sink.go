package ui

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// TelemetrySinkBufferSize bounds the async TUI telemetry channel. Saturation
// drops and counts instead of blocking the UI thread.
const TelemetrySinkBufferSize = 256

type telemetryWrite struct {
	dir      string
	filename string
	data     []byte
}

var (
	telemetryCh     = make(chan telemetryWrite, TelemetrySinkBufferSize)
	telemetryOnce   sync.Once
	telemetryDropCt uint64
)

// TelemetryDropped returns the number of telemetry records dropped because the
// async channel was saturated. A non-zero value signals the consumer is slower
// than UI production.
func TelemetryDropped() uint64 {
	return atomic.LoadUint64(&telemetryDropCt)
}

func ensureTelemetrySink() {
	telemetryOnce.Do(func() {
		go telemetryWriteLoop()
	})
}

// enqueueTelemetryWrite appends one telemetry record without blocking the UI
// thread. When the channel is saturated the record is dropped and counted
// (drop-on-overflow semantics).
func enqueueTelemetryWrite(dir, filename string, data []byte) {
	ensureTelemetrySink()
	select {
	case telemetryCh <- telemetryWrite{dir: dir, filename: filename, data: data}:
	default:
		atomic.AddUint64(&telemetryDropCt, 1)
	}
}

// telemetryWriteLoop is the sole disk writer for TUI telemetry. It runs off
// the UI thread so debug logging never blocks rendering or input.
func telemetryWriteLoop() {
	for w := range telemetryCh {
		writeTelemetryRecord(w)
	}
}

// writeTelemetryRecord persists one telemetry record. It is the only function
// in the TUI telemetry path that touches the filesystem.
func writeTelemetryRecord(w telemetryWrite) {
	if w.dir == "" || w.filename == "" || len(w.data) == 0 {
		return
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(w.dir, w.filename), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(w.data)
	_ = f.Close()
}
