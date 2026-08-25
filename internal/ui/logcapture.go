package ui

import (
	"log"
	"strings"
	"sync"
)

// ── Standard-logger silencing (altscreen guard) ─────────────────────────────
//
// While Bubble Tea owns the terminal in altscreen mode, ANY write to
// os.Stdout/os.Stderr — including the standard library logger reached from
// dependency engines — races the renderer's own ANSI redraw sequences on the
// same TTY and corrupts the visible frame (cursor jumps, dropped redraws, an
// apparently frozen screen). installStdLogCapture therefore redirects the
// standard logger into a bounded in-memory ring buffer for the whole program
// lifetime: zero raw bytes ever reach the terminal while the TUI is active,
// and the last diagnostics stay recoverable from memory.

const (
	// stdLogCaptureMaxEntries bounds the ring: the most recent N log lines.
	stdLogCaptureMaxEntries = 256
	// stdLogCaptureMaxLineLen bounds one captured line; longer lines are
	// truncated with an ellipsis marker.
	stdLogCaptureMaxLineLen = 512
)

// stdLogCapture is a thread-safe bounded in-memory sink for standard-logger
// output. It implements io.Writer.
type stdLogCapture struct {
	mu      sync.Mutex
	entries []string
}

// Write consumes one logger write, splitting it into lines and appending each
// to the ring. It never fails.
func (c *stdLogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := strings.TrimSuffix(string(p), "\n")
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		if len(line) > stdLogCaptureMaxLineLen {
			line = line[:stdLogCaptureMaxLineLen-3] + "…"
		}
		c.entries = append(c.entries, line)
		if len(c.entries) > stdLogCaptureMaxEntries {
			c.entries = c.entries[len(c.entries)-stdLogCaptureMaxEntries:]
		}
	}
	return len(p), nil
}

// Dump returns the captured entries oldest-first.
func (c *stdLogCapture) Dump() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.entries...)
}

// globalStdLogCapture holds the entries of the active capture window. It is
// process-wide because the standard logger is process-wide too.
var globalStdLogCapture = &stdLogCapture{}

// stdLogCaptureDump returns the currently retained standard-logger entries,
// oldest-first. Empty when no capture window is active or nothing was logged.
func stdLogCaptureDump() []string { return globalStdLogCapture.Dump() }

// installStdLogCapture redirects the standard library logger into the global
// ring buffer (flags cleared: the ring stores bare messages). The returned
// closure restores the previous writer and flags exactly.
func installStdLogCapture() (restore func()) {
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(globalStdLogCapture)
	log.SetFlags(0)
	return func() {
		log.SetOutput(prev)
		log.SetFlags(prevFlags)
	}
}
