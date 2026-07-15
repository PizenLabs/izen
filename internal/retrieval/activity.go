package retrieval

// ActivityLogFunc is a hook for piping internal tool invocations into
// the UI chat viewport in real time. The UI model sets this at startup.
type ActivityLogFunc func(format string, args ...interface{})

var globalActivityLog ActivityLogFunc

func SetActivityLogger(fn ActivityLogFunc) {
	globalActivityLog = fn
}
