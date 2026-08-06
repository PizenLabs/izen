// Package kernel defines the pure execution core of the Izen Agent Runtime V3:
// task lifecycle, context handling, timeout enforcement, and terminal status
// classification.
//
// The package is deliberately coupled to nothing: it carries no AI/LLM
// concepts, never touches a file system, and depends only on the standard
// library and pkg/event.
package kernel

// ExecutionStatus describes the lifecycle state of a task.
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCanceled  ExecutionStatus = "canceled"
)

// IsTerminal reports whether the status represents a finished task. Only
// StatusCompleted, StatusFailed, and StatusCanceled are terminal.
func (s ExecutionStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}
