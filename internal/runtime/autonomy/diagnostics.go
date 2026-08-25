package autonomy

import "sync"

// ── Diagnostic telemetry sink (Boundary 2 / Boundary 5) ─────────────────────
//
// Boundary telemetry ([boundary2] …, [boundary5] …) must never touch
// os.Stdout or os.Stderr: while Bubble Tea owns the terminal in altscreen
// mode, any raw byte from a background goroutine races the renderer's own
// ANSI redraw sequences and corrupts the visible frame. Diagnostics are
// therefore routed through this injectable sink — wired at composition time
// to publish engine.activity events on the shared event bus so the UI
// projects them like every other domain event (DiagnosticSignal discipline:
// advisory lines cross a boundary; they are never printed raw).
//
// With no sink installed the line is DISCARDED. Silence is the safe default:
// headless callers that want observability wire SetDiagnosticLog themselves.

// DiagnosticSink receives one formatted boundary-telemetry line. It must be
// safe for concurrent use; implementations should never write to the process
// stdio streams.
type DiagnosticSink func(format string, args ...interface{})

var (
	diagMu   sync.RWMutex
	diagSink DiagnosticSink // nil = discard
)

// SetDiagnosticLog overrides the boundary-telemetry sink. Passing nil
// disables diagnostics entirely (discard). Safe for concurrent use.
func SetDiagnosticLog(fn DiagnosticSink) {
	diagMu.Lock()
	defer diagMu.Unlock()
	diagSink = fn
}

// diagnosticf emits one boundary-telemetry line through the configured sink.
// It is a no-op when no sink is installed.
func diagnosticf(format string, args ...interface{}) {
	diagMu.RLock()
	sink := diagSink
	diagMu.RUnlock()
	if sink == nil {
		return
	}
	sink(format, args...)
}
