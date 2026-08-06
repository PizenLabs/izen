package kernel

import (
	"context"
	"time"
)

// TaskResult is the outcome of executing a task. Status is always terminal on
// the result returned by ExecuteTask; Error carries the failure or cancellation
// reason when applicable; Data carries an optional, type-unspecified outcome.
type TaskResult struct {
	Status ExecutionStatus
	Error  error
	Data   any
}

// Executable is the contract a runnable task satisfies. Requires lists the IDs
// of tasks that must complete first and is reserved for the scheduler layer;
// the kernel does not resolve dependencies itself.
type Executable interface {
	ID() string
	Requires() []string
	Timeout() time.Duration
	Execute(ctx context.Context, rt Runtime) TaskResult
}
