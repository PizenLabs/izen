package kernel

import (
	"context"
	"errors"

	"github.com/PizenLabs/izen/pkg/event"
)

// ErrTaskTimeout is the cause attached to a task context when a task's Timeout
// elapses. It is surfaced as the TaskResult error for timed-out executions.
var ErrTaskTimeout = errors.New("task timed out")

var errNilTask = errors.New("nil task")

// Engine executes Executable tasks and enforces their lifecycle contract. It
// wraps the event bus and emits task.started / task.completed / task.failed /
// task.canceled events around each execution.
type Engine struct {
	bus event.EventBus
}

// NewEngine constructs an Engine. A nil bus disables event emission.
func NewEngine(bus event.EventBus) *Engine {
	return &Engine{bus: bus}
}

// ExecuteTask runs a task to completion and returns its terminal result.
//
// It derives a task-scoped context from parentCtx, applying the task's Timeout
// when greater than zero. A task that ignores its context still terminates
// deterministically: a deadline produces StatusFailed with ErrTaskTimeout, an
// explicit runtime Cancel produces StatusCanceled, and a canceled parent
// produces StatusCanceled.
//
//nolint:contextcheck // a nil parentCtx defaults to a background root, never a derived context.
func (e *Engine) ExecuteTask(parentCtx context.Context, task Executable) TaskResult {
	if task == nil {
		return TaskResult{Status: StatusFailed, Error: errNilTask}
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	ctx, cancel := e.taskContext(parentCtx, task)
	rt := NewRuntime(ctx, cancel, e.bus)

	e.emit(event.TypeTaskStarted, task.ID(), nil)

	result := task.Execute(ctx, rt)

	status, err := classify(rt, ctx, result)
	result.Status = status
	result.Error = err
	e.emitTerminal(status, task.ID(), result)

	return result
}

// taskContext builds the task-scoped context, applying the task timeout when
// one is declared.
func (e *Engine) taskContext(parentCtx context.Context, task Executable) (context.Context, context.CancelCauseFunc) {
	if task.Timeout() > 0 {
		ctx, cancel := context.WithTimeoutCause(parentCtx, task.Timeout(), ErrTaskTimeout)
		return ctx, cancelCauseIgnoring(cancel)
	}
	return context.WithCancelCause(parentCtx)
}

// cancelCauseIgnoring adapts a plain CancelFunc to the CancelCauseFunc shape.
// It is used for timed contexts, whose cause is already fixed at construction.
func cancelCauseIgnoring(cancel context.CancelFunc) context.CancelCauseFunc {
	return func(error) { cancel() }
}

func (e *Engine) emit(typ event.EventType, taskID string, payload any) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(event.NewEvent(typ, taskID, payload))
}

func (e *Engine) emitTerminal(status ExecutionStatus, taskID string, result TaskResult) {
	var typ event.EventType
	switch status {
	case StatusCompleted:
		typ = event.TypeTaskCompleted
	case StatusFailed:
		typ = event.TypeTaskFailed
	case StatusCanceled:
		typ = event.TypeTaskCanceled
	default:
		return
	}
	e.emit(typ, taskID, result)
}

// classify determines the terminal status and error of a completed execution.
// Explicit runtime cancellation wins, followed by a deadline, then parent
// cancellation. Otherwise the task's own result is honored, with non-terminal
// statuses normalized from its error.
func classify(rt Runtime, ctx context.Context, result TaskResult) (ExecutionStatus, error) {
	switch {
	case rt.IsCanceled():
		return StatusCanceled, context.Cause(ctx)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return StatusFailed, context.Cause(ctx)
	case errors.Is(ctx.Err(), context.Canceled):
		return StatusCanceled, context.Cause(ctx)
	}

	if result.Status.IsTerminal() {
		return result.Status, result.Error
	}
	if result.Error != nil {
		return StatusFailed, result.Error
	}
	return StatusCompleted, nil
}
