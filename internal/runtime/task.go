package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	domaintask "github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ErrTaskTimeout is the canonical timeout cause (aliased from the domain so
// both runtime.ErrTaskTimeout and task.ErrTaskTimeout observe one value).
// STEP 3A parity with internal/kernel.
var ErrTaskTimeout = domaintask.ErrTaskTimeout

var errNilTask = errors.New("nil task")

var errNilEngine = errors.New("runtime: nil engine")

// TaskRuntime is the concrete domaintask.Runtime implementation for
// RuntimeEngine.ExecuteTask. It encapsulates the task-scoped context, its
// cancel cause function, and the shared event bus.
type TaskRuntime struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	bus      *events.Bus
	canceled atomic.Bool
}

// NewTaskRuntime builds a TaskRuntime from an existing task-scoped context
// and its cancel cause function. The bus may be nil to disable emission.
func NewTaskRuntime(ctx context.Context, cancel context.CancelCauseFunc, bus *events.Bus) *TaskRuntime {
	return &TaskRuntime{ctx: ctx, cancel: cancel, bus: bus}
}

// Context returns the task-scoped context.
func (r *TaskRuntime) Context() context.Context { return r.ctx }

// Emit publishes an event to the bus. It is a no-op when the runtime has no
// bus.
func (r *TaskRuntime) Emit(ev events.DomainEvent) {
	if r == nil || r.bus == nil {
		return
	}
	r.bus.Publish(ev)
}

// IsCanceled reports whether Cancel was invoked on this runtime.
func (r *TaskRuntime) IsCanceled() bool {
	if r == nil {
		return false
	}
	return r.canceled.Load()
}

// Cancel marks the runtime canceled and cancels the task-scoped context with
// the given reason. A nil reason is replaced with context.Canceled.
func (r *TaskRuntime) Cancel(reason error) {
	if r == nil {
		return
	}
	if reason == nil {
		reason = context.Canceled
	}
	r.canceled.Store(true)
	r.cancel(reason)
}

// NewTaskEngine constructs a lightweight RuntimeEngine for Executable task
// execution (STEP 3A parity with kernel.NewEngine). A nil bus disables event
// emission. The durable store is nil (harness/legacy mode: no ledger
// persistence); use WithTaskStore or the full NewRuntimeEngine to wire
// ledger lineage.
func NewTaskEngine(bus *events.Bus) *RuntimeEngine {
	return &RuntimeEngine{bus: bus}
}

// WithEventBus wires the event bus for ExecuteTask emission. Nil disables
// emission. Returns the engine for chaining.
func (e *RuntimeEngine) WithEventBus(bus *events.Bus) *RuntimeEngine {
	if e != nil {
		e.bus = bus
	}
	return e
}

// WithTaskStore wires the durable lineage sink. Nil detaches persistence.
// Returns the engine for chaining.
func (e *RuntimeEngine) WithTaskStore(store *durable.TaskStore) *RuntimeEngine {
	if e != nil {
		e.store = store
	}
	return e
}

// ExecuteTask runs a task to completion and returns its terminal result.
//
// It derives a task-scoped context from parentCtx, applying the task's
// Timeout when greater than zero. A task that ignores its context still
// terminates deterministically: a deadline produces Failed with
// ErrTaskTimeout, an explicit runtime Cancel produces Canceled, and a
// canceled parent produces Canceled.
//
// Terminal outcomes are appended to ledger.ndjson via the wired store before
// bus events emit. A persistence failure is fail-closed — the result degrades
// to Failed so a terminal state is never reported without durable lineage.
// With no store wired the in-memory lifecycle runs unchanged.
//
//nolint:contextcheck // a nil parentCtx defaults to a background root, never a derived context.
func (e *RuntimeEngine) ExecuteTask(parentCtx context.Context, task domaintask.Executable) domaintask.TaskResult {
	if task == nil {
		return domaintask.TaskResult{Status: domaintask.ExecStatusFailed, Error: errNilTask}
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	ctx, cancel := e.taskContext(parentCtx, task)
	rt := NewTaskRuntime(ctx, cancel, e.busOf())

	e.emit(events.NewTaskStarted(task.ID()))

	result := task.Execute(ctx, rt)

	status, err := classifyTask(rt, ctx, result)
	result.Status = status
	result.Error = err
	if perr := e.persistTerminal(task.ID(), result); perr != nil {
		result.Status = domaintask.ExecStatusFailed
		result.Error = perr
		e.emitTerminal(domaintask.ExecStatusFailed, task.ID(), result)
		return result
	}
	e.emitTerminal(status, task.ID(), result)

	return result
}

// Execute runs task to completion through the ledger-backed lifecycle and
// returns the domain task result. It exists as the STEP 3A parity name so
// callers can program against one entry while ExecuteTask stays stable for
// graph/planner callers.
func (e *RuntimeEngine) Execute(parentCtx context.Context, task domaintask.Executable) domaintask.TaskResult {
	if e == nil {
		return domaintask.TaskResult{Status: domaintask.ExecStatusFailed, Error: errNilEngine}
	}
	return e.ExecuteTask(parentCtx, task)
}

func (e *RuntimeEngine) busOf() *events.Bus {
	if e == nil {
		return nil
	}
	return e.bus
}

func (e *RuntimeEngine) persistTerminal(taskID string, result domaintask.TaskResult) error {
	if e == nil || e.store == nil {
		return nil
	}
	if taskID == "" {
		return fmt.Errorf("runtime: empty task id (fail-closed)")
	}
	var typ durable.EventType
	switch result.Status {
	case domaintask.ExecStatusCompleted:
		typ = durable.EventType("TASK_COMPLETED")
	case domaintask.ExecStatusCanceled:
		typ = durable.EventType("TASK_CANCELED")
	default:
		typ = durable.EventType("TASK_FAILED")
	}
	payload := map[string]any{"status": string(result.Status)}
	if result.Error != nil {
		payload["error"] = result.Error.Error()
	}
	if err := e.store.RecordCustomEvent(taskID, typ, payload); err != nil {
		return fmt.Errorf("runtime: record terminal lineage: %w", err)
	}
	return nil
}

func (e *RuntimeEngine) taskContext(parentCtx context.Context, task domaintask.Executable) (context.Context, context.CancelCauseFunc) {
	if task.Timeout() > 0 {
		ctx, cancel := context.WithTimeoutCause(parentCtx, task.Timeout(), ErrTaskTimeout)
		return ctx, func(error) { cancel() }
	}
	return context.WithCancelCause(parentCtx)
}

func (e *RuntimeEngine) emit(ev events.DomainEvent) {
	if e == nil || e.bus == nil {
		return
	}
	e.bus.Publish(ev)
}

func (e *RuntimeEngine) emitTerminal(status domaintask.ExecutionStatus, taskID string, result domaintask.TaskResult) {
	var ev events.DomainEvent
	switch status {
	case domaintask.ExecStatusCompleted:
		ev = events.NewTaskCompleted(taskID, result)
	case domaintask.ExecStatusFailed:
		ev = events.NewTaskFailed(taskID, result)
	case domaintask.ExecStatusCanceled:
		ev = events.NewTaskCanceled(taskID)
	default:
		return
	}
	e.emit(ev)
}

func classifyTask(rt domaintask.Runtime, ctx context.Context, result domaintask.TaskResult) (domaintask.ExecutionStatus, error) {
	switch {
	case rt.IsCanceled():
		return domaintask.ExecStatusCanceled, context.Cause(ctx)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return domaintask.ExecStatusFailed, context.Cause(ctx)
	case errors.Is(ctx.Err(), context.Canceled):
		return domaintask.ExecStatusCanceled, context.Cause(ctx)
	}

	if result.Status.IsTerminal() {
		return result.Status, result.Error
	}
	if result.Error != nil {
		return domaintask.ExecStatusFailed, result.Error
	}
	return domaintask.ExecStatusCompleted, nil
}
