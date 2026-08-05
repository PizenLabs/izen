package execution

import "time"

// ActivityLogFunc is a hook for piping file mutation events into the UI
// chat viewport in real time. The UI model sets this at startup so every
// structural patch write and rollback is fully transparent.
type ActivityLogFunc func(format string, args ...interface{})

var globalActivityLog ActivityLogFunc

func SetActivityLogger(fn ActivityLogFunc) {
	globalActivityLog = fn
}

// EventFunc is a typed event sink for EngineEvent payloads carrying real
// I/O metrics (bytes read, lines patched, exit codes, elapsed time).
// The UI model sets this at startup and dispatches directly to the
// ActivityTree — no string parsing involved.
type EventFunc func(event interface{})

var globalEventLog EventFunc

func SetEventLogger(fn EventFunc) {
	globalEventLog = fn
}

// FileMutateEvent carries real metrics for a file mutation operation.
type FileMutateEvent struct {
	File     string
	LinesAdd int
	LinesDel int
	Elapsed  time.Duration
}

// CommandExecEvent carries real metrics for a command execution operation.
// ExitCode < 0 marks a RUNNING command (the activity tree renders it with the
// animated snowflake spinner); the terminal event carries the real exit code,
// elapsed time, and the combined stdout/stderr output for Ctrl+O expansion.
type CommandExecEvent struct {
	Command  string
	ExitCode int
	Elapsed  time.Duration
	Output   string
}
