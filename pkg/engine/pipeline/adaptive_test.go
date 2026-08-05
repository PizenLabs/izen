package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// TestEngineRunAdaptiveEndToEnd drives a full generative run through the
// adaptive control loop (Observe → Decide → Execute) and verifies the folded
// Result matches the classic pipeline output.
func TestEngineRunAdaptiveEndToEnd(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	eng = NewEngine(eng.Root(), eng.Sor().Engine(),
		WithRouter(NewRouter(
			WithModel(IntentExecution, "fast-coding-model"),
		)),
		WithClient(patchCompletion(t, "fast-coding-model")),
	)

	res, err := eng.RunAdaptive(context.Background(), Request{
		Mode:        "build",
		Intent:      layer3.IntentNewFeature,
		TargetFile:  "svc/service.go",
		Description: "add Helper function",
	})
	if err != nil {
		t.Fatalf("RunAdaptive: %v", err)
	}
	if res.Knowledge == nil {
		t.Fatal("Knowledge is nil")
	}
	if res.Capabilities == nil || res.Capabilities.Stack() != "go" {
		t.Errorf("Capabilities = %v, want go stack", res.Capabilities)
	}
	if res.Context == nil || !res.Context.Stats.BudgetMet {
		t.Errorf("Context not governed: %+v", res.Context)
	}
	if len(res.Patches) == 0 || res.Patches[0].Path != "svc/service.go" {
		t.Fatalf("patches = %+v, want svc/service.go", res.Patches)
	}
	if !strings.Contains(res.Patches[0].New, "func Helper() string") {
		t.Errorf("patch content missing Helper:\n%s", res.Patches[0].New)
	}
	if res.Validation == nil || !res.Validation.OK {
		t.Fatalf("validation not OK: %+v", res.Validation)
	}
	if res.Route.Intent != IntentExecution || res.Route.Model != "fast-coding-model" {
		t.Errorf("route = %+v, want execution/fast-coding-model", res.Route)
	}
}

// TestEngineRunAdaptiveEmitsControlFacts verifies the adaptive run publishes
// fact-only control telemetry: iterations, node observations and termination.
func TestEngineRunAdaptiveEmitsControlFacts(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	bus := telemetry.NewEventBus(64)
	c := &capture{}
	bus.SubscribeAll(c.handler)
	eng = NewEngine(eng.Root(), eng.Sor().Engine(),
		WithEventBus(bus),
		WithRouter(NewRouter(WithModel(IntentExecution, "fast-coding-model"))),
		WithClient(patchCompletion(t, "fast-coding-model")),
	)

	res, err := eng.RunAdaptive(context.Background(), Request{
		Mode:        "build",
		Intent:      layer3.IntentNewFeature,
		TargetFile:  "svc/service.go",
		Description: "add Helper function",
	})
	if err != nil {
		t.Fatalf("RunAdaptive: %v", err)
	}
	if !res.Validation.OK {
		t.Fatalf("validation failed: %+v", res.Validation.Err)
	}

	waitCount(t, c, telemetry.EventControlIteration, 1)
	waitCount(t, c, telemetry.EventControlNodeObserved, 1)
	waitCount(t, c, telemetry.EventControlTerminated, 1)
	// The classic layer facts are still emitted through the same bus.
	waitCount(t, c, telemetry.EventKnowledgeResolved, 1)
	waitCount(t, c, telemetry.EventValidationDAG, 1)
}

// TestEngineRunAdaptiveNoClientAborts verifies a critical execution failure
// (no worker client) terminates the adaptive run with a loud error instead of
// falling back to heuristic context extraction.
func TestEngineRunAdaptiveNoClientAborts(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	eng = NewEngine(eng.Root(), eng.Sor().Engine()) // no client

	_, err := eng.RunAdaptive(context.Background(), Request{
		Mode:        "build",
		Intent:      layer3.IntentNewFeature,
		TargetFile:  "svc/service.go",
		Description: "add a function",
	})
	if err == nil {
		t.Fatal("expected error running without a worker client, got nil")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("error = %q, want the adaptive loop to abort loudly", err)
	}
}

// TestEngineAdaptivePlanStatic verifies the Static IR for a request: five
// critical nodes in dependency order with no runtime state attached.
func TestEngineAdaptivePlanStatic(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	plan, err := eng.AdaptivePlan(Request{Mode: "build", Intent: layer3.IntentNewFeature})
	if err != nil {
		t.Fatalf("AdaptivePlan: %v", err)
	}
	if plan.ID == "" || plan.Description == "" {
		t.Error("plan missing identity")
	}
	order, err := plan.Graph.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	ids := make([]string, len(order))
	for i, n := range order {
		ids[i] = n.ID
		if !n.Critical {
			t.Errorf("pipeline node %q must be critical", n.ID)
		}
	}
	want := []string{"knowledge", "capabilities", "context", "execute", "validate"}
	if len(ids) != len(want) {
		t.Fatalf("plan nodes = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("plan order = %v, want %v", ids, want)
		}
	}
}
