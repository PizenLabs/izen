package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/event"
)

// mockTask is a scriptable Executable used to exercise the engine lifecycle.
type mockTask struct {
	id       string
	requires []string
	timeout  time.Duration
	fn       func(ctx context.Context, rt Runtime) TaskResult
}

func (m *mockTask) ID() string             { return m.id }
func (m *mockTask) Requires() []string     { return m.requires }
func (m *mockTask) Timeout() time.Duration { return m.timeout }
func (m *mockTask) Execute(ctx context.Context, rt Runtime) TaskResult {
	if m.fn != nil {
		return m.fn(ctx, rt)
	}
	return TaskResult{Status: StatusCompleted}
}

// collectEvents subscribes to the given types and returns the received events
// on a channel plus an unsubscribe function.
func collectEvents(bus *event.MemoryEventBus, types []event.EventType) (<-chan event.Event, func()) {
	ch := make(chan event.Event, 64)
	unsub := bus.Subscribe(types, func(e event.Event) { ch <- e })
	return ch, unsub
}

// mustNext reads the next event, failing the test if none arrives in time.
func mustNext(t *testing.T, ch <-chan event.Event) event.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return event.Event{}
	}
}

func TestExecuteTaskSuccessEventOrder(t *testing.T) {
	bus := event.NewMemoryEventBus(64)
	engine := NewEngine(bus)
	events, unsub := collectEvents(bus, []event.EventType{
		event.TypeTaskStarted,
		event.TypeTaskCompleted,
		event.TypeTaskFailed,
		event.TypeTaskCanceled,
	})
	defer unsub()

	task := &mockTask{
		id: "task-1",
		fn: func(context.Context, Runtime) TaskResult {
			return TaskResult{Status: StatusCompleted, Data: "done"}
		},
	}

	res := engine.ExecuteTask(context.Background(), task)

	if res.Status != StatusCompleted {
		t.Fatalf("expected %s, got %s", StatusCompleted, res.Status)
	}
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if res.Data != "done" {
		t.Fatalf("expected data %q, got %v", "done", res.Data)
	}

	if got := mustNext(t, events); got.Type != event.TypeTaskStarted {
		t.Fatalf("expected first event %s, got %s", event.TypeTaskStarted, got.Type)
	} else if got.TaskID != "task-1" {
		t.Fatalf("expected TaskID task-1, got %s", got.TaskID)
	}
	if got := mustNext(t, events); got.Type != event.TypeTaskCompleted {
		t.Fatalf("expected second event %s, got %s", event.TypeTaskCompleted, got.Type)
	} else if got.TaskID != "task-1" {
		t.Fatalf("expected TaskID task-1, got %s", got.TaskID)
	}
}

func TestExecuteTaskTimeout(t *testing.T) {
	bus := event.NewMemoryEventBus(64)
	engine := NewEngine(bus)
	events, unsub := collectEvents(bus, []event.EventType{
		event.TypeTaskStarted,
		event.TypeTaskFailed,
		event.TypeTaskCanceled,
		event.TypeTaskCompleted,
	})
	defer unsub()

	task := &mockTask{
		id:      "slow-task",
		timeout: 20 * time.Millisecond,
		fn: func(context.Context, Runtime) TaskResult {
			// Ignores the context and sleeps past the deadline to prove the
			// engine still terminates the task deterministically.
			time.Sleep(200 * time.Millisecond)
			return TaskResult{Status: StatusCompleted, Data: "too-late"}
		},
	}

	res := engine.ExecuteTask(context.Background(), task)

	if res.Status != StatusFailed {
		t.Fatalf("expected %s, got %s", StatusFailed, res.Status)
	}
	if !errors.Is(res.Error, ErrTaskTimeout) {
		t.Fatalf("expected ErrTaskTimeout, got %v", res.Error)
	}

	if got := mustNext(t, events); got.Type != event.TypeTaskStarted {
		t.Fatalf("expected first event %s, got %s", event.TypeTaskStarted, got.Type)
	}
	if got := mustNext(t, events); got.Type != event.TypeTaskFailed {
		t.Fatalf("expected second event %s, got %s", event.TypeTaskFailed, got.Type)
	}
}

func TestExecuteTaskRuntimeCancel(t *testing.T) {
	bus := event.NewMemoryEventBus(64)
	engine := NewEngine(bus)
	events, unsub := collectEvents(bus, []event.EventType{
		event.TypeTaskStarted,
		event.TypeTaskCanceled,
		event.TypeTaskCompleted,
		event.TypeTaskFailed,
	})
	defer unsub()

	sentinel := errors.New("canceled by task")
	task := &mockTask{
		id: "cancel-task",
		fn: func(_ context.Context, rt Runtime) TaskResult {
			rt.Cancel(sentinel)
			return TaskResult{Status: StatusCanceled}
		},
	}

	res := engine.ExecuteTask(context.Background(), task)

	if res.Status != StatusCanceled {
		t.Fatalf("expected %s, got %s", StatusCanceled, res.Status)
	}
	if !errors.Is(res.Error, sentinel) {
		t.Fatalf("expected sentinel error, got %v", res.Error)
	}

	if got := mustNext(t, events); got.Type != event.TypeTaskStarted {
		t.Fatalf("expected first event %s, got %s", event.TypeTaskStarted, got.Type)
	}
	if got := mustNext(t, events); got.Type != event.TypeTaskCanceled {
		t.Fatalf("expected second event %s, got %s", event.TypeTaskCanceled, got.Type)
	}
}

func TestExecuteTaskParentCancel(t *testing.T) {
	bus := event.NewMemoryEventBus(64)
	engine := NewEngine(bus)
	events, unsub := collectEvents(bus, []event.EventType{
		event.TypeTaskStarted,
		event.TypeTaskCanceled,
		event.TypeTaskFailed,
		event.TypeTaskCompleted,
	})
	defer unsub()

	parentCtx, cancel := context.WithCancel(context.Background())
	task := &mockTask{
		id: "parent-cancel",
		fn: func(ctx context.Context, _ Runtime) TaskResult {
			<-ctx.Done()
			return TaskResult{Status: StatusCanceled}
		},
	}

	resCh := make(chan TaskResult, 1)
	go func() { resCh <- engine.ExecuteTask(parentCtx, task) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	res := <-resCh
	if res.Status != StatusCanceled {
		t.Fatalf("expected %s, got %s", StatusCanceled, res.Status)
	}
	if !errors.Is(res.Error, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", res.Error)
	}

	if got := mustNext(t, events); got.Type != event.TypeTaskStarted {
		t.Fatalf("expected first event %s, got %s", event.TypeTaskStarted, got.Type)
	}
	if got := mustNext(t, events); got.Type != event.TypeTaskCanceled {
		t.Fatalf("expected second event %s, got %s", event.TypeTaskCanceled, got.Type)
	}
}

func TestExecuteTaskRespectsTaskFailure(t *testing.T) {
	bus := event.NewMemoryEventBus(64)
	engine := NewEngine(bus)
	events, unsub := collectEvents(bus, []event.EventType{
		event.TypeTaskStarted,
		event.TypeTaskFailed,
	})
	defer unsub()

	sentinel := errors.New("task reported failure")
	task := &mockTask{
		id: "fail-task",
		fn: func(context.Context, Runtime) TaskResult {
			return TaskResult{Status: StatusFailed, Error: sentinel}
		},
	}

	res := engine.ExecuteTask(context.Background(), task)

	if res.Status != StatusFailed {
		t.Fatalf("expected %s, got %s", StatusFailed, res.Status)
	}
	if !errors.Is(res.Error, sentinel) {
		t.Fatalf("expected sentinel error, got %v", res.Error)
	}

	if got := mustNext(t, events); got.Type != event.TypeTaskStarted {
		t.Fatalf("expected first event %s, got %s", event.TypeTaskStarted, got.Type)
	}
	if got := mustNext(t, events); got.Type != event.TypeTaskFailed {
		t.Fatalf("expected second event %s, got %s", event.TypeTaskFailed, got.Type)
	}
}

func TestExecuteTaskNormalizesNonTerminalResult(t *testing.T) {
	bus := event.NewMemoryEventBus(64)
	engine := NewEngine(bus)

	task := &mockTask{
		id: "raw-task",
		fn: func(context.Context, Runtime) TaskResult {
			return TaskResult{Status: StatusRunning}
		},
	}

	res := engine.ExecuteTask(context.Background(), task)
	if res.Status != StatusCompleted {
		t.Fatalf("expected non-terminal result normalized to %s, got %s", StatusCompleted, res.Status)
	}
}

func TestExecuteTaskNilTask(t *testing.T) {
	bus := event.NewMemoryEventBus(64)
	engine := NewEngine(bus)

	res := engine.ExecuteTask(context.Background(), nil)
	if res.Status != StatusFailed {
		t.Fatalf("expected %s, got %s", StatusFailed, res.Status)
	}
}

func TestExecutionStatusTerminal(t *testing.T) {
	terminal := map[ExecutionStatus]bool{
		StatusPending:   false,
		StatusRunning:   false,
		StatusCompleted: true,
		StatusFailed:    true,
		StatusCanceled:  true,
	}
	for status, want := range terminal {
		if got := status.IsTerminal(); got != want {
			t.Errorf("IsTerminal(%s) = %v, want %v", status, got, want)
		}
	}
}
