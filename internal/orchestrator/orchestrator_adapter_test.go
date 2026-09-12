package orchestrator

// STEP 2 adapter tests: the legacy Orchestrator delegates phase hops to
// internal/runtime/orchestrator and persists PHASE_TRANSITION (+
// WORKSPACE_SWITCHED) lineage to the RuntimeEngine-owned ledger. The legacy
// in-memory lifecycle (SM driving, history, bus events) is preserved.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

func adapterRuntime(t *testing.T) *runtime.RuntimeContext {
	t.Helper()
	return runtime.New(
		artifact.NewStore(filepath.Join(t.TempDir(), ".izen")),
		capability.NewCapabilitySet(),
		budget.NewBudget(10, 1000, 100000, 3, 30*time.Second, 10),
	)
}

func adapterStore(t *testing.T) *durable.TaskStore {
	t.Helper()
	store := durable.NewTaskStore(t.TempDir())
	if err := store.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask("orch-task-1", "adapter", []string{"pkg/auth"}); err != nil {
		t.Fatal(err)
	}
	return store
}

func adapterLedger(t *testing.T, store *durable.TaskStore) string {
	t.Helper()
	raw, err := os.ReadFile(store.LedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A Transition through the adapter persists PHASE_TRANSITION lineage while
// preserving the legacy projection (current/history/workflow state).
func TestAdapterTransitionPersistsLedger(t *testing.T) {
	store := adapterStore(t)
	o := New(workflow.NewWorkflowStateMachine(), adapterRuntime(t)).
		WithTaskStore(store, "orch-task-1")

	if err := o.Transition(PhaseInvestigate, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Investigate): %v", err)
	}
	if o.Current() != PhaseInvestigate {
		t.Fatalf("Current() = %s, want investigate", o.Current())
	}
	body := adapterLedger(t, store)
	if !strings.Contains(body, "PHASE_TRANSITION") {
		t.Fatalf("ledger missing PHASE_TRANSITION:\n%s", body)
	}
	if !strings.Contains(body, "WORKSPACE_SWITCHED") {
		t.Fatalf("ledger missing WORKSPACE_SWITCHED for mapped phase:\n%s", body)
	}
	if got, want := len(o.History()), 2; got != want {
		t.Fatalf("History() length = %d, want %d", got, want)
	}
}

// A forbidden edge returns *TransitionError and persists nothing.
func TestAdapterInvalidTransitionPersistsNothing(t *testing.T) {
	store := adapterStore(t)
	o := New(workflow.NewWorkflowStateMachine(), adapterRuntime(t)).
		WithTaskStore(store, "orch-task-1")

	before := adapterLedger(t, store)
	if err := o.Transition(PhaseReview, workflow.TransitionContext{}); err == nil {
		t.Fatal("expected error for Idle -> Review, got nil")
	} else {
		var te *TransitionError
		if !errors.As(err, &te) {
			t.Fatalf("error type = %T, want *TransitionError", err)
		}
	}
	if after := adapterLedger(t, store); after != before {
		t.Fatalf("forbidden hop persisted lineage:\n%s", after)
	}
	if o.Current() != PhaseIdle {
		t.Fatalf("Current() = %s, want idle after rejected hop", o.Current())
	}
}

// Force bypasses the edge graph but never skips lineage: the forced hop
// persists PHASE_TRANSITION with forced=true.
func TestAdapterForcePersistsLedger(t *testing.T) {
	store := adapterStore(t)
	o := New(workflow.NewWorkflowStateMachine(), adapterRuntime(t)).
		WithTaskStore(store, "orch-task-1")

	for _, ph := range []Phase{PhasePlan, PhaseBuild, PhaseReview} {
		ctx := workflow.TransitionContext{}
		if ph == PhaseBuild {
			ctx = workflow.TransitionContext{HasPlan: true, HasCapabilities: true}
		}
		if err := o.Transition(ph, ctx); err != nil {
			t.Fatalf("Transition(%s): %v", ph, err)
		}
	}
	if err := o.Force(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Force(Plan): %v", err)
	}
	if o.Current() != PhasePlan {
		t.Fatalf("Current() = %s, want plan after Force", o.Current())
	}
	body := adapterLedger(t, store)
	if !strings.Contains(body, "PHASE_TRANSITION") {
		t.Fatalf("ledger missing PHASE_TRANSITION after Force:\n%s", body)
	}
	if !strings.Contains(body, "forced") {
		t.Fatalf("ledger missing forced marker after Force:\n%s", body)
	}
}

// Without a store the legacy path runs with no ledger file created.
func TestAdapterLegacyPathWithoutStore(t *testing.T) {
	dir := t.TempDir()
	_ = dir
	o := New(workflow.NewWorkflowStateMachine(), adapterRuntime(t))
	if err := o.Transition(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Plan): %v", err)
	}
	if o.Current() != PhasePlan {
		t.Fatalf("Current() = %s, want plan", o.Current())
	}
}

// WithRuntimeEngine borrows the RuntimeEngine's bound store for lineage.
func TestAdapterWithRuntimeEngine(t *testing.T) {
	store := adapterStore(t)
	o := New(workflow.NewWorkflowStateMachine(), adapterRuntime(t)).
		WithRuntimeEngine(stubProvider{store: store}, "orch-task-1")

	if err := o.Transition(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Plan): %v", err)
	}
	if body := adapterLedger(t, store); !strings.Contains(body, "PHASE_TRANSITION") {
		t.Fatalf("ledger missing PHASE_TRANSITION via RuntimeEngine store:\n%s", body)
	}
}

// stubProvider satisfies RuntimeProvider (the shape *runtime.RuntimeEngine
// satisfies via Store()).
type stubProvider struct{ store *durable.TaskStore }

func (s stubProvider) Store() *durable.TaskStore { return s.store }
