package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── PHASE 6 — HUMAN PRESENTATION SEPARATION (UI) ────────────────────────────
//
// The TUI consumes ONLY the presentation projection (ExecutionViewState /
// ExecutionNarrative / ExecutionFrame). These tests prove the strict
// visibility separation end-to-end through the real model renderer:
//
//  1. NORMAL (human view) does not contain provider names, strategies, tokens,
//     or event names.
//  2. EXPANDED contains runtime metadata (strategy, model, tokens, duration,
//     artifacts).
//  3. DEBUG contains lifecycle events.
//  4. JSON artifacts are rendered through the semantic renderer, never raw.
//  5. Narrative changes according to the actual graph state.
//  6. No fake/static steps.

// feedFullExecution drives a full canonical runtime event stream through the
// model's bus projection.
func feedFullExecution(m *model) {
	rid := "p6"
	m.handleDomainEvent(events.NewExecutionStarted(rid, "build", "fix index.html", ""))
	m.handleDomainEvent(events.NewStrategySelected(rid, "targeted_mutation", true, "explicit target"))
	m.handleDomainEvent(events.NewTargetResolved(rid, "index.html", true, "strategy"))
	m.handleDomainEvent(events.NewContextPrepared(rid, []string{"user_intent", "target_content"}, 40))
	m.handleDomainEvent(events.NewModelInvoked(rid, "mock-provider", 0, 0))
	m.handleDomainEvent(events.NewProviderResponse(rid, "mock-provider", 12, 6))
	m.handleDomainEvent(events.NewArtifactProduced(rid, "patch", "index.html"))
	m.handleDomainEvent(events.NewExecutionFinished(rid, true, "completed"))
	// The terminal event clears the loading state; the test restores the
	// gated-resolving marker so the layer renderer is exercised (the renderer
	// only renders inside the gated execution window).
	m.executionResolving = true
}

// TestHumanViewDoesNotContainProviderNames pins requirement 6a: the NORMAL
// human view renders only the human narrative — no provider/model names,
// strategies, token counts, or raw event names.
func TestHumanViewDoesNotContainProviderNames(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("p6")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityNormal
	feedFullExecution(m)

	panel := stripANSITest(m.renderExecutionLayered())
	if !strings.Contains(panel, "Reading index.html") {
		t.Fatalf("human view missing the narrative: %q", panel)
	}
	if strings.Contains(panel, "mock-provider") {
		t.Fatalf("NORMAL human view leaked a provider name: %q", panel)
	}
	if strings.Contains(panel, "targeted_mutation") {
		t.Fatalf("NORMAL human view leaked the strategy: %q", panel)
	}
	if strings.Contains(panel, "execution.started") || strings.Contains(panel, "strategy.selected") ||
		strings.Contains(panel, "artifact.produced") || strings.Contains(panel, "target.resolved") {
		t.Fatalf("NORMAL human view leaked a raw event name: %q", panel)
	}
	// Token counts must not appear in the human view.
	if strings.Contains(panel, "12 in") || strings.Contains(panel, "token") {
		t.Fatalf("NORMAL human view leaked token usage: %q", panel)
	}
}

// TestExpandedViewContainsRuntimeMetadata pins requirement 6b: EXPANDED (Ctrl+O)
// reveals strategy, context policy, model, token usage, duration, and artifacts.
func TestExpandedViewContainsRuntimeMetadata(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("p6")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityExpanded
	feedFullExecution(m)

	panel := stripANSITest(m.renderExecutionLayered())
	for _, want := range []string{
		"strategy:", "targeted_mutation",
		"context policy:", "user_intent",
		"model:", "mock-provider",
		"tokens:", "12 in",
		"duration:",
		"artifact:",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("EXPANDED view missing runtime metadata %q: %q", want, panel)
		}
	}
	// The artifact is rendered semantically (diff header), never as raw kind.
	if !strings.Contains(panel, "diff index.html") {
		t.Errorf("EXPANDED view did not render the artifact semantically: %q", panel)
	}
	// EXPANDED shows metadata but NOT the full raw event stream section.
	if strings.Contains(panel, "runtime events") {
		t.Fatalf("EXPANDED view leaked the debug event stream: %q", panel)
	}
}

// TestDebugViewContainsLifecycleEvents pins requirement 6c: DEBUG surfaces the
// full runtime lifecycle event stream.
func TestDebugViewContainsLifecycleEvents(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("p6")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityDebug
	feedFullExecution(m)

	panel := stripANSITest(m.renderExecutionLayered())
	for _, want := range []string{
		"execution.started",
		"strategy.selected",
		"target.resolved",
		"context.prepared",
		"model.invoked",
		"provider.response",
		"artifact.produced",
		"execution.finished",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("DEBUG view missing lifecycle event %q: %q", want, panel)
		}
	}
}

// TestJSONPlanArtifactUsesSemanticRenderer pins requirement 6d: a JSON plan
// artifact is rendered through the semantic renderer — the output is a task
// list, never raw JSON.
func TestJSONPlanArtifactUsesSemanticRenderer(t *testing.T) {
	m := newTestModel()
	rawJSON := `{"strategic_overview":{"impact_domain":"Execution Layer"},"atomic_tasks":[{"file":"internal/execution/a.go","description":"fix a"},{"file":"internal/execution/b.go","description":"fix b"}]}`
	m.pushArtifact("plan", "internal/execution", rawJSON)

	joined := ""
	for _, r := range m.records {
		joined += r.text + "\n"
	}
	if !strings.Contains(joined, "a.go") || !strings.Contains(joined, "b.go") {
		t.Fatalf("plan artifact not rendered semantically (missing tasks): %q", joined)
	}
	if strings.Contains(joined, "atomic_tasks") || strings.Contains(joined, "\"task_id\"") {
		t.Fatalf("plan artifact leaked raw JSON: %q", joined)
	}
}

// TestCtrlOCyclesVisibility pins the Ctrl+O EXPANDED contract: with an active
// gated execution, Ctrl+O cycles NORMAL → EXPANDED → DEBUG → NORMAL.
func TestCtrlOCyclesVisibility(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("p6")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityNormal
	// The projection becomes Active only when a real execution event arrives —
	// Ctrl+O must not cycle a projection with no runtime truth behind it.
	m.handleDomainEvent(events.NewExecutionStarted("p6", "build", "x", ""))

	if !m.cycleExecVisibility() || m.execVisibility != presentation.VisibilityExpanded {
		t.Fatalf("first Ctrl+O must go to EXPANDED, got %s", m.execVisibility)
	}
	if !m.cycleExecVisibility() || m.execVisibility != presentation.VisibilityDebug {
		t.Fatalf("second Ctrl+O must go to DEBUG, got %s", m.execVisibility)
	}
	if !m.cycleExecVisibility() || m.execVisibility != presentation.VisibilityNormal {
		t.Fatalf("third Ctrl+O must return to NORMAL, got %s", m.execVisibility)
	}
	// With no active execution, the key falls through (returns false).
	m.executionResolving = false
	if m.cycleExecVisibility() {
		t.Fatal("Ctrl+O must not cycle visibility without an active gated execution")
	}
}

// TestNarrativeChangesAccordingToGraphState pins requirement 6e: the rendered
// human view reflects the ACTUAL graph state — a partial execution shows only
// the transitions that occurred.
func TestNarrativeChangesAccordingToGraphState(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("p6")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityNormal

	// Partial graph: only started + strategy. Neither transition is human-
	// visible progress, so the rendered view must be EMPTY — never a canned
	// "Understanding request" step and never the later static steps.
	m.handleDomainEvent(events.NewExecutionStarted("p6", "build", "fix index.html", ""))
	m.handleDomainEvent(events.NewStrategySelected("p6", "targeted_mutation", true, "x"))
	panel := stripANSITest(m.renderExecutionLayered())
	if panel != "" {
		t.Fatalf("partial graph must render no steps (started+strategy are plumbing): %q", panel)
	}
	for _, forbidden := range []string{"Inspecting", "Gathering", "Generating", "Preparing", "Verified"} {
		if strings.Contains(panel, forbidden) {
			t.Errorf("partial graph rendered a step that never happened (%q): %q", forbidden, panel)
		}
	}

	// Advance the graph: the view changes with the actual state.
	m.handleDomainEvent(events.NewTargetResolved("p6", "index.html", true, "strategy"))
	panel2 := stripANSITest(m.renderExecutionLayered())
	if !strings.Contains(panel2, "Reading index.html") {
		t.Fatalf("advanced narrative missing Reading index.html: %q", panel2)
	}
	if panel == panel2 {
		t.Fatal("narrative did not change with the actual graph state")
	}
}

// TestNoFakeStaticSteps pins requirement 6f: a verification that never happened
// never appears as a narrative step, regardless of the dock/shimmer state.
func TestNoFakeStaticSteps(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("p6")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityNormal

	m.handleDomainEvent(events.NewExecutionStarted("p6", "build", "fix index.html", ""))
	m.handleDomainEvent(events.NewStrategySelected("p6", "targeted_mutation", true, "x"))
	m.handleDomainEvent(events.NewTargetResolved("p6", "index.html", true, "strategy"))
	m.handleDomainEvent(events.NewExecutionFinished("p6", true, "completed"))

	panel := stripANSITest(m.renderExecutionLayered())
	// The narrative must never contain a step the runtime never emitted —
	// verification never ran, so "Verified" must not appear.
	if strings.Contains(panel, "Verified") {
		t.Fatalf("fake verification step rendered without the transition: %q", panel)
	}
	if strings.Contains(panel, "Generating response") {
		t.Fatalf("fake generation step rendered without the provider invocation: %q", panel)
	}
}
