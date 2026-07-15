package lynx

// ActivityLogFunc is a hook for piping Lynx daemon lifecycle events
// into the UI chat viewport in real time. The UI model sets this at
// startup so every binary spawn and search is fully transparent.
type ActivityLogFunc func(format string, args ...interface{})

var globalActivityLog ActivityLogFunc

func SetActivityLogger(fn ActivityLogFunc) {
	globalActivityLog = fn
}
