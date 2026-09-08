package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
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
func collectEvents(bus *events.Bus, types []string) (<-chan events.DomainEvent, func()) {
	want := make(map[string]bool, len(types))
	for _, typ := range types {
		want[typ] = true
	}
	ch := make(chan events.DomainEvent, 64)
	sub := bus.SubscribeAll(func(ev events.DomainEvent) {
		if want[ev.Type()] {
			ch <- ev
		}
	})
	return ch, sub.Cancel
}

// mustNext reads the next event, failing the test if none arrives in time.
func mustNext(t *testing.T, ch <-chan events.DomainEvent) events.DomainEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

// taskIDOf extracts the task identity from a task lifecycle event payload.
func taskIDOf(t *testing.T, ev events.DomainEvent) string {
	t.Helper()
	switch p := ev.Payload().(type) {
	case events.TaskStartedPayload:
		return p.TaskID
	case events.TaskCompletedPayload:
		return p.TaskID
	case events.TaskFailedPayload:
		return p.TaskID
	case events.TaskCanceledPayload:
		return p.TaskID
	default:
		t.Fatalf("unexpected task event payload %T", ev.Payload())
		return ""
	}
}

func TestExecuteTaskSuccessEventOrder(t *testing.T) {
	bus := events.NewBus(64)
	engine := NewEngine(bus)
	evCh, unsub := collectEvents(bus, []string{
		events.EventTaskStarted,
		events.EventTaskCompleted,
		events.EventTaskFailed,
		events.EventTaskCanceled,
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

	if got := mustNext(t, evCh); got.Type() != events.EventTaskStarted {
		t.Fatalf("expected first event %s, got %s", events.EventTaskStarted, got.Type())
	} else if taskIDOf(t, got) != "task-1" {
		t.Fatalf("expected TaskID task-1, got %s", taskIDOf(t, got))
	}
	if got := mustNext(t, evCh); got.Type() != events.EventTaskCompleted {
		t.Fatalf("expected second event %s, got %s", events.EventTaskCompleted, got.Type())
	} else if taskIDOf(t, got) != "task-1" {
		t.Fatalf("expected TaskID task-1, got %s", taskIDOf(t, got))
	}
}

func TestExecuteTaskTimeout(t *testing.T) {
	bus := events.NewBus(64)
	engine := NewEngine(bus)
	evCh, unsub := collectEvents(bus, []string{
		events.EventTaskStarted,
		events.EventTaskFailed,
		events.EventTaskCanceled,
		events.EventTaskCompleted,
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

	if got := mustNext(t, evCh); got.Type() != events.EventTaskStarted {
		t.Fatalf("expected first event %s, got %s", events.EventTaskStarted, got.Type())
	}
	if got := mustNext(t, evCh); got.Type() != events.EventTaskFailed {
		t.Fatalf("expected second event %s, got %s", events.EventTaskFailed, got.Type())
	}
}

func TestExecuteTaskRuntimeCancel(t *testing.T) {
	bus := events.NewBus(64)
	engine := NewEngine(bus)
	evCh, unsub := collectEvents(bus, []string{
		events.EventTaskStarted,
		events.EventTaskCanceled,
		events.EventTaskCompleted,
		events.EventTaskFailed,
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

	if got := mustNext(t, evCh); got.Type() != events.EventTaskStarted {
		t.Fatalf("expected first event %s, got %s", events.EventTaskStarted, got.Type())
	}
	if got := mustNext(t, evCh); got.Type() != events.EventTaskCanceled {
		t.Fatalf("expected second event %s, got %s", events.EventTaskCanceled, got.Type())
	}
}

func TestExecuteTaskParentCancel(t *testing.T) {
	bus := events.NewBus(64)
	engine := NewEngine(bus)
	evCh, unsub := collectEvents(bus, []string{
		events.EventTaskStarted,
		events.EventTaskCanceled,
		events.EventTaskFailed,
		events.EventTaskCompleted,
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

	if got := mustNext(t, evCh); got.Type() != events.EventTaskStarted {
		t.Fatalf("expected first event %s, got %s", events.EventTaskStarted, got.Type())
	}
	if got := mustNext(t, evCh); got.Type() != events.EventTaskCanceled {
		t.Fatalf("expected second event %s, got %s", events.EventTaskCanceled, got.Type())
	}
}

func TestExecuteTaskRespectsTaskFailure(t *testing.T) {
	bus := events.NewBus(64)
	engine := NewEngine(bus)
	evCh, unsub := collectEvents(bus, []string{
		events.EventTaskStarted,
		events.EventTaskFailed,
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

	if got := mustNext(t, evCh); got.Type() != events.EventTaskStarted {
		t.Fatalf("expected first event %s, got %s", events.EventTaskStarted, got.Type())
	}
	if got := mustNext(t, evCh); got.Type() != events.EventTaskFailed {
		t.Fatalf("expected second event %s, got %s", events.EventTaskFailed, got.Type())
	}
}

func TestExecuteTaskNormalizesNonTerminalResult(t *testing.T) {
	bus := events.NewBus(64)
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
	bus := events.NewBus(64)
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
