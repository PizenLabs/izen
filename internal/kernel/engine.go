package kernel

import (
	"context"
	"errors"
	"fmt"

	domaintask "github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
)

// ErrTaskTimeout is the canonical timeout cause (aliased from the domain so
// both kernel.ErrTaskTimeout and task.ErrTaskTimeout observe one value).
var ErrTaskTimeout = domaintask.ErrTaskTimeout

var errNilTask = errors.New("nil task")

// Engine executes Executable tasks and enforces their lifecycle contract. It
// wraps the event bus and emits task.started / task.completed / task.failed /
// task.canceled events around each execution.
//
// STEP 2 — thin pass-through adapter: when a durable TaskStore is wired (via
// WithTaskStore, or via WithRuntimeEngine which extracts the RuntimeEngine's
// bound store), every terminal result is appended to ledger.ndjson as task
// lifecycle lineage. The store is the single source of truth; the Engine
// holds no independent execution state (no scheduling loops, no task maps —
// only the bus and the store pointer). With no store wired the Engine runs
// the legacy in-memory lifecycle unchanged.
type Engine struct {
	bus *events.Bus
	// store is the durable lineage sink borrowed from the RuntimeEngine.
	// Nil disables ledger persistence (harness/legacy mode).
	store *durable.TaskStore
}

// RuntimeProvider is satisfied by *runtime.RuntimeEngine (via its Store
// method) without importing the master runtime package from the kernel,
// keeping the dependency direction kernel -> durable only.
type RuntimeProvider interface {
	Store() *durable.TaskStore
}

// NewEngine constructs an Engine. A nil bus disables event emission.
func NewEngine(bus *events.Bus) *Engine {
	return &Engine{bus: bus}
}

// WithTaskStore wires the durable lineage sink borrowed from the
// RuntimeEngine. The store remains owned by the RuntimeEngine; the adapter
// never replicates its state in memory. Nil detaches persistence. Returns
// the engine for chaining.
func (e *Engine) WithTaskStore(store *durable.TaskStore) *Engine {
	if e != nil {
		e.store = store
	}
	return e
}

// WithRuntimeEngine wires the RuntimeEngine as the execution authority by
// borrowing its bound durable store for ledger lineage. A nil provider (or
// one with a nil store) detaches persistence. Returns the engine for
// chaining.
func (e *Engine) WithRuntimeEngine(rt RuntimeProvider) *Engine {
	if e == nil {
		return nil
	}
	if rt == nil {
		e.store = nil
		return e
	}
	e.store = rt.Store()
	return e
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

	e.emit(events.NewTaskStarted(task.ID()))

	result := task.Execute(ctx, rt)

	status, err := classify(rt, ctx, result)
	result.Status = status
	result.Error = err
	// Adapter delegation: persist the terminal outcome to the
	// RuntimeEngine-owned ledger before emitting bus events. A persistence
	// failure is fail-closed — the result degrades to Failed so a terminal
	// state is never reported without durable lineage.
	if perr := e.persistTerminal(task.ID(), result); perr != nil {
		result.Status = StatusFailed
		result.Error = perr
		e.emitTerminal(StatusFailed, task.ID(), result)
		return result
	}
	e.emitTerminal(status, task.ID(), result)

	return result
}

// Execute is the adapter's execution entry: it runs task to completion
// through the RuntimeEngine-backed lifecycle (ExecuteTask) and returns the
// mapped domain/task.TaskResult. It exists so callers can program against
// the STEP 2 parity name while the legacy ExecuteTask signature stays
// untouched for graph/planner callers.
func (e *Engine) Execute(parentCtx context.Context, task Executable) TaskResult {
	if e == nil {
		return TaskResult{Status: StatusFailed, Error: errNilEngine}
	}
	return e.ExecuteTask(parentCtx, task)
}

var errNilEngine = errors.New("kernel: nil engine")

// AnalyzeFailure classifies a failed execution cause into a terminal
// domain/task.TaskResult through the runtime failure taxonomy (the same
// classification the RuntimeEngine applies inside its failure loop):
// timeout causes preserve ErrTaskTimeout for errors.Is, cancellations map
// to StatusCanceled, and everything else maps to StatusFailed carrying the
// ephemeral FailureReason as Data for planner routing.
func (e *Engine) AnalyzeFailure(execErr error) TaskResult {
	if execErr == nil {
		return TaskResult{Status: StatusCompleted}
	}
	if errors.Is(execErr, ErrTaskTimeout) || errors.Is(execErr, context.DeadlineExceeded) {
		return TaskResult{
			Status: StatusFailed,
			Error:  fmt.Errorf("%w: %w", ErrTaskTimeout, execErr),
			Data:   ephemeral.ClassifyError(execErr),
		}
	}
	if errors.Is(execErr, context.Canceled) {
		return TaskResult{Status: StatusCanceled, Error: execErr}
	}
	return TaskResult{
		Status: StatusFailed,
		Error:  execErr,
		Data:   ephemeral.ClassifyError(execErr),
	}
}

// persistTerminal appends one task-lifecycle lineage event to ledger.ndjson
// via the wired store. Nil store disables persistence (legacy mode).
// Unknown event types are audit-only in replay: they durably anchor the
// lineage without mutating materialized task state.
func (e *Engine) persistTerminal(taskID string, result TaskResult) error {
	if e == nil || e.store == nil {
		return nil
	}
	if taskID == "" {
		return fmt.Errorf("kernel: empty task id (fail-closed)")
	}
	var typ durable.EventType
	switch result.Status {
	case StatusCompleted:
		typ = durable.EventType("TASK_COMPLETED")
	case StatusCanceled:
		typ = durable.EventType("TASK_CANCELED")
	default:
		typ = durable.EventType("TASK_FAILED")
	}
	payload := map[string]any{"status": string(result.Status)}
	if result.Error != nil {
		payload["error"] = result.Error.Error()
	}
	if err := e.store.RecordCustomEvent(taskID, typ, payload); err != nil {
		return fmt.Errorf("kernel: record terminal lineage: %w", err)
	}
	return nil
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

func (e *Engine) emit(ev events.DomainEvent) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(ev)
}

func (e *Engine) emitTerminal(status ExecutionStatus, taskID string, result TaskResult) {
	var ev events.DomainEvent
	switch status {
	case StatusCompleted:
		ev = events.NewTaskCompleted(taskID, result)
	case StatusFailed:
		ev = events.NewTaskFailed(taskID, result)
	case StatusCanceled:
		ev = events.NewTaskCanceled(taskID)
	default:
		return
	}
	e.emit(ev)
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
