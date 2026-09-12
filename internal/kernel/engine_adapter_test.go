package kernel

// STEP 2 adapter tests: kernel.Engine persists terminal lineage to the
// RuntimeEngine-owned ledger and classifies failures through the parity
// taxonomy. The legacy in-memory lifecycle is preserved verbatim.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

func adapterStore(t *testing.T) (*durable.TaskStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		t.Fatal(err)
	}
	return store, dir
}

func ledgerBody(t *testing.T, store *durable.TaskStore) string {
	t.Helper()
	raw, err := os.ReadFile(store.LedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A successful execution through the adapter appends terminal lineage to
// ledger.ndjson while preserving the legacy result and event contract.
func TestAdapterExecutePersistsLedger(t *testing.T) {
	store, _ := adapterStore(t)
	bus := events.NewBus(64)
	engine := NewEngine(bus).WithTaskStore(store)

	task := &mockTask{
		id: "adapter-task-1",
		fn: func(context.Context, Runtime) TaskResult {
			return TaskResult{Status: StatusCompleted, Data: "done"}
		},
	}
	res := engine.ExecuteTask(context.Background(), task)
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", res.Status)
	}
	body := ledgerBody(t, store)
	if !strings.Contains(body, "TASK_COMPLETED") {
		t.Fatalf("ledger missing TASK_COMPLETED lineage:\n%s", body)
	}
	if !strings.Contains(body, "adapter-task-1") {
		t.Fatalf("ledger missing task identity:\n%s", body)
	}
}

// A failed execution appends TASK_FAILED lineage.
func TestAdapterExecuteFailurePersistsLedger(t *testing.T) {
	store, _ := adapterStore(t)
	engine := NewEngine(nil).WithTaskStore(store)

	sentinel := errors.New("adapter boom")
	res := engine.ExecuteTask(context.Background(), &mockTask{
		id: "adapter-task-2",
		fn: func(context.Context, Runtime) TaskResult {
			return TaskResult{Status: StatusFailed, Error: sentinel}
		},
	})
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if body := ledgerBody(t, store); !strings.Contains(body, "TASK_FAILED") {
		t.Fatalf("ledger missing TASK_FAILED lineage:\n%s", body)
	}
}

// WithRuntimeEngine borrows the RuntimeEngine's bound store: lineage lands
// in the engine-owned ledger.
func TestAdapterWithRuntimeEngine(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(nil)
	rt := newStubRuntimeProvider(t, dir)
	engine := eng.WithRuntimeEngine(rt)

	res := engine.ExecuteTask(context.Background(), &mockTask{id: "adapter-task-3"})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", res.Status)
	}
	if body := ledgerBody(t, rt.Store()); !strings.Contains(body, "TASK_COMPLETED") {
		t.Fatalf("ledger missing TASK_COMPLETED via RuntimeEngine store:\n%s", body)
	}
}

// Without a store the legacy path runs with no ledger file created.
func TestAdapterLegacyPathWithoutStore(t *testing.T) {
	dir := t.TempDir()
	engine := NewEngine(nil)

	res := engine.ExecuteTask(context.Background(), &mockTask{id: "legacy-task"})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", res.Status)
	}
	if _, err := os.Stat(dir + "/.izen/runtime/ledger.ndjson"); !os.IsNotExist(err) {
		t.Fatal("legacy path must not create ledger state")
	}
}

// Execute is the parity-named entry and observes the same lifecycle.
func TestAdapterExecuteAlias(t *testing.T) {
	engine := NewEngine(nil)
	res := engine.Execute(context.Background(), &mockTask{id: "alias-task"})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", res.Status)
	}
	var nilEng *Engine
	if res := nilEng.Execute(context.Background(), &mockTask{id: "x"}); res.Status != StatusFailed {
		t.Fatalf("nil engine status = %s, want failed", res.Status)
	}
}

// AnalyzeFailure preserves timeout/cancel causes and classifies the rest.
func TestAdapterAnalyzeFailure(t *testing.T) {
	engine := NewEngine(nil)

	if got := engine.AnalyzeFailure(ErrTaskTimeout); got.Status != StatusFailed ||
		!errors.Is(got.Error, ErrTaskTimeout) {
		t.Fatalf("timeout maps to %+v, want Failed+ErrTaskTimeout", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if got := engine.AnalyzeFailure(ctx.Err()); got.Status != StatusFailed &&
		got.Status != StatusCanceled {
		t.Fatalf("deadline maps to %+v, want Failed or Canceled", got)
	}
	if got := engine.AnalyzeFailure(context.Canceled); got.Status != StatusCanceled {
		t.Fatalf("cancel maps to %+v, want Canceled", got)
	}
	if got := engine.AnalyzeFailure(nil); got.Status != StatusCompleted {
		t.Fatalf("nil maps to %+v, want Completed", got)
	}
}

// stubRuntimeProvider adapts a real durable store as a RuntimeProvider
// (the shape *runtime.RuntimeEngine satisfies via Store()).
type stubRuntimeProvider struct{ store *durable.TaskStore }

func newStubRuntimeProvider(t *testing.T, dir string) *stubRuntimeProvider {
	t.Helper()
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		t.Fatal(err)
	}
	return &stubRuntimeProvider{store: store}
}

func (s *stubRuntimeProvider) Store() *durable.TaskStore { return s.store }
