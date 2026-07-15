package execution

// ActivityLogFunc is a hook for piping file mutation events into the UI
// chat viewport in real time. The UI model sets this at startup so every
// structural patch write and rollback is fully transparent.
type ActivityLogFunc func(format string, args ...interface{})

var globalActivityLog ActivityLogFunc

func SetActivityLogger(fn ActivityLogFunc) {
	globalActivityLog = fn
}
