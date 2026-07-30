package retrieval

// ActivityLogFunc is a hook for piping internal tool invocations into
// the UI chat viewport in real time. The UI model sets this at startup.
type ActivityLogFunc func(format string, args ...interface{})

var globalActivityLog ActivityLogFunc

func SetActivityLogger(fn ActivityLogFunc) {
	globalActivityLog = fn
}

// EventFunc is a typed event sink for EngineEvent payloads carrying real
// I/O metrics (bytes read, search hits, elapsed time).
// The UI model sets this at startup and dispatches directly to the
// ActivityTree — no string parsing involved.
type EventFunc func(event interface{})

var globalEventLog EventFunc

func SetEventLogger(fn EventFunc) {
	globalEventLog = fn
}
