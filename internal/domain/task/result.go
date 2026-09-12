package task

import (
	"context"
	"errors"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// ExecutionStatus describes the lifecycle state of a task at execution
// time. It is distinct from TaskStatus (the planner lifecycle in task.go):
// the ExecStatus-prefixed constant names disambiguate the two lifecycles
// that share this package. Wire values preserve the legacy kernel labels
// ("pending", "running", "completed", "failed", "canceled") so the
// transitional kernel aliases observe zero behavior change.
type ExecutionStatus string

const (
	ExecStatusPending   ExecutionStatus = "pending"
	ExecStatusRunning   ExecutionStatus = "running"
	ExecStatusCompleted ExecutionStatus = "completed"
	ExecStatusFailed    ExecutionStatus = "failed"
	ExecStatusCanceled  ExecutionStatus = "canceled"
)

// IsTerminal reports whether the status represents a finished task. Only
// ExecStatusCompleted, ExecStatusFailed, and ExecStatusCanceled are terminal.
func (s ExecutionStatus) IsTerminal() bool {
	switch s {
	case ExecStatusCompleted, ExecStatusFailed, ExecStatusCanceled:
		return true
	default:
		return false
	}
}

// ErrTaskTimeout is the cause attached to a task context when a task's
// Timeout elapses. It is surfaced as the TaskResult error for timed-out
// executions.
var ErrTaskTimeout = errors.New("task timed out")

// TaskResult is the outcome of executing a task. Status is always terminal
// on the result returned by execution; Error carries the failure or
// cancellation reason when applicable; Data carries an optional,
// type-unspecified outcome.
type TaskResult struct {
	Status ExecutionStatus
	Error  error
	Data   any
}

// Runtime is the execution environment handed to a task. It exposes the
// task-scoped context, the shared event bus, and explicit cancellation.
//
// It mirrors the legacy kernel.Runtime contract exactly so the kernel can
// alias it during migration without changing execution behaviour.
type Runtime interface {
	Context() context.Context
	Emit(ev events.DomainEvent)
	IsCanceled() bool
	Cancel(reason error)
}

// Executable is the contract a runnable task satisfies. Requires lists the
// IDs of tasks that must complete first and is reserved for the scheduler
// layer; the execution core does not resolve dependencies itself.
type Executable interface {
	ID() string
	Requires() []string
	Timeout() time.Duration
	Execute(ctx context.Context, rt Runtime) TaskResult
}
