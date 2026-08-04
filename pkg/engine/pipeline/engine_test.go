package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// capture collects telemetry events published during a run.
type capture struct {
	mu    sync.Mutex
	types map[telemetry.EventType]int
}

func (c *capture) handler(ev telemetry.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.types == nil {
		c.types = make(map[telemetry.EventType]int)
	}
	c.types[ev.Type()]++
}

func (c *capture) count(t telemetry.EventType) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.types[t]
}

func waitCount(t *testing.T, c *capture, typ telemetry.EventType, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.count(typ) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected >=%d events of type %s, got %d", want, typ, c.count(typ))
}

// patchCompletion returns a FuncClient that emits a valid Go patch for
// svc/service.go (adding a Helper function) whenever the routed model matches.
func patchCompletion(t *testing.T, wantModel string) *FuncClient {
	t.Helper()
	return NewFuncClient(func(_ context.Context, provider, model, _ string) (string, layer3.TokenUsage, error) {
		if model != wantModel {
			t.Errorf("worker called with model %q, want %q", model, wantModel)
		}
		return "=== FILE: svc/service.go\n" + serviceWithHelper + "\n=== END",
			layer3.TokenUsage{Input: 100, Output: 20}, nil
	})
}

const serviceWithHelper = `package svc

// Compute doubles the input.
func Compute(n int) int {
	return n * 2
}

// Helper reports a stable suffix.
func Helper() string {
	return "ok"
}

// helper is an internal implementation detail.
func helper(s string) string { return "[" + s + "]" }
`

// TestEngineRunEndToEnd drives a full generative run through Layers 0-5.
func TestEngineRunEndToEnd(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())

	bus := telemetry.NewEventBus(64)
	c := &capture{}
	bus.SubscribeAll(c.handler)

	eng = NewEngine(eng.Root(), eng.Sor().Engine(),
		WithEventBus(bus),
		WithRouter(NewRouter(
			WithModel(IntentExecution, "fast-coding-model"),
			WithModel(IntentReasoning, "heavy-reasoning-model"),
		)),
		WithClient(patchCompletion(t, "fast-coding-model")),
	)

	res, err := eng.Run(context.Background(), Request{
		Mode:        "build",
		Intent:      layer3.IntentNewFeature,
		TargetFile:  "svc/service.go",
		Description: "add Helper function",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Knowledge == nil {
		t.Fatal("Knowledge is nil")
	}
	if res.Knowledge.PrimaryManager != "go" {
		t.Errorf("PrimaryManager = %q, want go", res.Knowledge.PrimaryManager)
	}
	if res.Capabilities == nil || res.Capabilities.Stack() != "go" {
		t.Errorf("Capabilities stack = %v, want go", res.Capabilities)
	}
	if res.Context == nil {
		t.Fatal("Context is nil")
	}
	if !res.Context.Stats.BudgetMet {
		t.Errorf("context budget not met: %+v", res.Context.Stats)
	}
	if res.Route.Intent != IntentExecution {
		t.Errorf("route intent = %s, want execution", res.Route.Intent)
	}
	if res.Route.Model != "fast-coding-model" {
		t.Errorf("route model = %q, want fast-coding-model", res.Route.Model)
	}
	if len(res.Patches) == 0 {
		t.Fatal("no patches produced")
	}
	if res.Patches[0].Path != "svc/service.go" {
		t.Errorf("patch path = %q, want svc/service.go", res.Patches[0].Path)
	}
	if !strings.Contains(res.Patches[0].New, "func Helper() string") {
		t.Errorf("patch content missing Helper:\n%s", res.Patches[0].New)
	}
	if res.Validation == nil {
		t.Fatal("Validation is nil")
	}
	if !res.Validation.OK {
		t.Errorf("validation failed: %+v", res.Validation.Err)
	}

	waitCount(t, c, telemetry.EventKnowledgeResolved, 1)
	waitCount(t, c, telemetry.EventCapabilityDetected, 1)
	waitCount(t, c, telemetry.EventContextGoverned, 1)
	waitCount(t, c, telemetry.EventPipelineStep, 1)
	waitCount(t, c, telemetry.EventValidationDAG, 1)
}

// TestEngineRunReasoningRoute selects the reasoning model and budget for a
// /plan-mode request.
func TestEngineRunReasoningRoute(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	eng = NewEngine(eng.Root(), eng.Sor().Engine(),
		WithRouter(NewRouter(
			WithModel(IntentReasoning, "heavy-reasoning-model"),
		)),
		WithClient(patchCompletion(t, "heavy-reasoning-model")),
	)

	res, err := eng.Run(context.Background(), Request{
		Mode:        "plan",
		Intent:      layer3.IntentRefactor,
		TargetFile:  "svc/service.go",
		Description: "refactor Compute",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Route.Intent != IntentReasoning {
		t.Errorf("route intent = %s, want reasoning", res.Route.Intent)
	}
	if res.Route.Model != "heavy-reasoning-model" {
		t.Errorf("route model = %q, want heavy-reasoning-model", res.Route.Model)
	}
}

// TestEngineRunNoClientErrors is the no-legacy-fallback contract: a generative
// request without a configured worker client fails loudly rather than falling
// back to heuristic context extraction.
func TestEngineRunNoClientErrors(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	_, err := eng.Run(context.Background(), Request{
		Mode:        "build",
		Intent:      layer3.IntentNewFeature,
		TargetFile:  "svc/service.go",
		Description: "add a function",
	})
	if err == nil {
		t.Fatal("expected error running without a worker client, got nil")
	}
}

// TestEngineDeterministicRewrite runs a rename through the AST rewriter and
// validates the result — no LLM involved.
func TestEngineDeterministicRewrite(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	res, err := eng.Run(context.Background(), Request{
		Mode:         "build",
		Intent:       layer3.IntentRename,
		TargetSymbol: "Compute",
		NewName:      "Double",
	})
	if err != nil {
		t.Fatalf("Run(rename): %v", err)
	}
	if len(res.Patches) == 0 {
		t.Fatal("no rename patches produced")
	}
	if res.Validation == nil || !res.Validation.OK {
		t.Errorf("validation not OK: %+v", res.Validation)
	}
}

// TestEngineStaticNoToolchain verifies a static HTML/JS workspace produces an
// empty capability surface (never a fabricated Go toolchain) and still runs.
func TestEngineStaticNoToolchain(t *testing.T) {
	eng, _, _ := indexedEngine(t, staticFixture())
	g, err := eng.Capabilities()
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if g.Stack() != "static" {
		t.Errorf("stack = %s, want static", g.Stack())
	}
	if g.Supports("build") || g.Supports("test") {
		t.Error("static project fabricated build/test capabilities")
	}
	h, err := eng.SystemPromptHeader()
	if err != nil {
		t.Fatalf("SystemPromptHeader: %v", err)
	}
	if strings.Contains(h, "go build") || strings.Contains(h, "go test") {
		t.Errorf("static header claims a Go toolchain:\n%s", h)
	}
}

// TestEngineValidateRAMOnly ensures the RAM-only validation DAG always runs the
// structural stage first and never shells out.
func TestEngineValidateRAMOnly(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	res, err := eng.Validate(context.Background(), []layer3.FilePatch{{
		Path:    "svc/service.go",
		New:     serviceWithHelper,
		Old:     goPipelineFixture()["svc/service.go"],
		Changed: true,
	}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Errorf("validation failed: %+v", res)
	}
	for _, id := range res.Order {
		if res.Nodes[id].Stage != "structural" && res.Nodes[id].Stage != "syntax" {
			t.Errorf("RAM-only validation scheduled non-RAM stage %q", res.Nodes[id].Stage)
		}
	}
}

// TestEngineKnowledgeAndCapabilities cover the standalone Step 1 and Step 2
// accessors.
func TestEngineKnowledgeAndCapabilities(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())

	k, err := eng.Knowledge(context.Background())
	if err != nil {
		t.Fatalf("Knowledge: %v", err)
	}
	if k.PrimaryManager != "go" {
		t.Errorf("primary manager = %q, want go", k.PrimaryManager)
	}

	g, err := eng.Capabilities()
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if g.Stack() != "go" {
		t.Errorf("stack = %s, want go", g.Stack())
	}

	h, err := eng.SystemPromptHeader()
	if err != nil {
		t.Fatalf("SystemPromptHeader: %v", err)
	}
	if !strings.Contains(h, "STACK: go") {
		t.Errorf("header missing stack:\n%s", h)
	}
}

// TestEngineContextEmptyWithoutTarget verifies that a request without a target
// yields an empty governed context — never a fabricated one.
func TestEngineContextEmptyWithoutTarget(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	exec, err := eng.Context(context.Background(), Request{Mode: "ask"}, eng.RouteForMode("ask").Policy)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if exec == nil {
		t.Fatal("Context returned nil")
	}
	if len(exec.Files) != 0 {
		t.Errorf("empty-context policy produced files: %d", len(exec.Files))
	}
}
