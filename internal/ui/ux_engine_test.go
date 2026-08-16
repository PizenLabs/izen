package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── UX_ENGINE — CONVERSATION FLOW & IMPLEMENTATION LEAKAGE ───────────────────
//
// UX_ENGINE #4: a casual greeting / simple question (DirectResponse strategy)
// is a single human action — "Izen → understands intent → answers". It must
// NOT create an execution narrative panel, workspace-context milestones, or
// planning states. The runtime still resolves and executes it (zero repository
// context), but the human surface is the answer only.
//
// UX_ENGINE #6: internal runtime concepts (strategy names, provider/model
// names, event names, artifact kinds) must never reach the default human
// surface. They are converted to human language at the projection boundary.

// TestConversationFlowDoesNotCreateExecutionNarrative pins UX_ENGINE #4: a
// direct-response request leaves the execution-view projection nil so the
// narrative panel never renders — no milestones, no fake pipeline.
func TestConversationFlowDoesNotCreateExecutionNarrative(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{
		responses: []*ai.Response{{Content: "Hello, how can I help?"}},
	}, nil)
	m.state = StateChat

	cmd := m.runGatedLine("hi")
	if cmd == nil {
		t.Fatal("conversation dispatch returned nil command")
	}
	// The conversation must not create the execution narrative projection.
	if m.execView != nil {
		t.Fatal("DirectResponse conversation created an execution narrative — UX_ENGINE #4 forbids a fake pipeline")
	}
	if !m.executionResolving {
		t.Fatal("conversation must still set the in-flight marker for terminal cleanup")
	}
	// No execution timeline: the dock TEXT has no execution-derived claim (and
	// never a static "Answering..." placeholder). Per PROMPT.md Test 1 a
	// conversation must not render the loading dock/spinner either — the
	// direct answer is the only surface.
	if dock := m.composeDockText(); dock != "" {
		t.Fatalf("conversation dock text = %q, want empty (no execution timeline)", dock)
	}
	if dock := m.renderLoadingDock(); dock != "" {
		t.Fatalf("conversation must render no loading dock (no spinner), got %q", dock)
	}
}

// TestConversationFlowRendersAnswerOnly pins that a DirectResponse result is
// pushed as the human answer without internal artifact leakage.
func TestConversationFlowRendersAnswerOnly(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{
		responses: []*ai.Response{{Content: "Go is a programming language."}},
	}, nil)
	m.state = StateChat

	cmd := m.runGatedLine("what is golang")
	if cmd == nil {
		t.Fatal("conversation dispatch returned nil command")
	}
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("got %T, want gatedExecutionMsg", msg)
	}
	if gem.err != nil {
		t.Fatalf("conversation execution failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.Strategy != string(strategy.DirectResponse) {
		t.Fatalf("result strategy = %v, want direct_response", gem.res)
	}
	if gem.res.Content != "Go is a programming language." {
		t.Fatalf("answer = %q, want the conversational answer", gem.res.Content)
	}

	// Project the result: the answer lands in the AI role, and no internal
	// "artifact produced" line ever reaches the human surface.
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	_ = res
	joined := recordsText(m)
	if !strings.Contains(joined, "Go is a programming language.") {
		t.Fatalf("answer missing from records: %q", joined)
	}
	for _, leak := range []string{"artifact produced", "artifact produced", "direct_response",
		"strategy.selected", "provider.response", "execution.finished", "[runtime]"} {
		if strings.Contains(joined, leak) {
			t.Errorf("human surface leaked internal concept %q: %q", leak, joined)
		}
	}
}

// TestGatedExecutionDoesNotLeakRuntimeConcepts pins UX_ENGINE #6 on the
// execution-bearing path: terminal projections humanize artifact activity lines
// instead of exposing the raw artifact kind.
func TestGatedExecutionDoesNotLeakRuntimeConcepts(t *testing.T) {
	// A plan-producing read-only execution must render "Prepared a plan", never
	// "[runtime] artifact produced: plan".
	m := gatedDispatchModel(t, &mockProvider{
		responses: []*ai.Response{{Content: `{"strategic_overview":{"impact_domain":"Execution"},"atomic_tasks":[{"file":"a.go","description":"fix a"}]}`}},
	}, nil)
	m.state = StateChat

	cmd := m.runGatedLine("$prompt plan a refactor")
	if cmd == nil {
		t.Fatal("dispatch returned nil command")
	}
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("got %T, want gatedExecutionMsg", msg)
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	_ = res
	joined := recordsText(m)
	if !strings.Contains(joined, "Prepared a plan") {
		t.Fatalf("plan activity line not humanized: %q", joined)
	}
	for _, leak := range []string{"[runtime] artifact produced", "artifact produced: plan", "multi_file_planning"} {
		if strings.Contains(joined, leak) {
			t.Errorf("human surface leaked internal artifact concept %q: %q", leak, joined)
		}
	}
}

// TestExpandedFrameShowsStepSources pins UX_ENGINE #1: every narrative step
// carries its ExecutionGraph derivation source (surfaced in the EXPANDED layer
// as the event-derivation proof).
func TestExpandedFrameShowsStepSources(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("ux")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityExpanded

	m.handleDomainEvent(events.NewExecutionStarted("ux", "build", "fix index.html"))
	m.handleDomainEvent(events.NewTargetResolved("ux", "index.html", true, "strategy"))

	panel := stripANSITest(m.renderExecutionLayered())
	if !strings.Contains(panel, "Reading index.html") {
		t.Fatalf("expanded view missing the narrative step: %q", panel)
	}
	if !strings.Contains(panel, "source: target.resolved") {
		t.Fatalf("expanded view missing the step derivation source: %q", panel)
	}
}

// TestNormalFrameHasNoStepSources pins that NORMAL (USER MODE) keeps the human
// milestones clean — no source sub-lines, no runtime metadata.
func TestNormalFrameHasNoStepSources(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("ux")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityNormal

	m.handleDomainEvent(events.NewExecutionStarted("ux", "build", "fix index.html"))
	m.handleDomainEvent(events.NewTargetResolved("ux", "index.html", true, "strategy"))

	panel := stripANSITest(m.renderExecutionLayered())
	if !strings.Contains(panel, "Reading index.html") {
		t.Fatalf("normal view missing the narrative step: %q", panel)
	}
	if strings.Contains(panel, "source:") {
		t.Fatalf("normal view leaked step derivation sources: %q", panel)
	}
	if strings.Contains(panel, "strategy:") || strings.Contains(panel, "tokens:") {
		t.Fatalf("normal view leaked runtime metadata: %q", panel)
	}
}

// TestHumanArtifactActivity pins the artifact-kind → human-language conversion.
func TestHumanArtifactActivity(t *testing.T) {
	cases := []struct {
		kind, target, want string
	}{
		{"plan", "", "Prepared a plan"},
		{"patch", "index.html", "Prepared a proposed change"},
		{"investigation", "", "Prepared findings"},
		{"verification", "", "Verified changes"},
		{"error", "", "Prepared an error report"},
		{"response", "", "Generated response"},
		{"explanation", "auth.go", "Generated response for auth.go"},
		{"", "", "Generated response"},
	}
	for _, tc := range cases {
		if got := humanArtifactActivity(tc.kind, tc.target); got != tc.want {
			t.Errorf("humanArtifactActivity(%q, %q) = %q, want %q", tc.kind, tc.target, got, tc.want)
		}
	}
}

// TestExecutionFailureIsHumanized pins that a runtime failure surfaces the
// human error message without classification/stage leakage.
func TestExecutionFailureIsHumanized(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.state = StateChat

	m.executionResolving = true
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("ux")
	m.execVisibility = presentation.VisibilityDebug
	m.handleDomainEvent(events.NewExecutionFailed(events.FailureRecoverable, errors.New("boom"), "executor"))

	joined := recordsText(m)
	if !strings.Contains(joined, "boom") {
		t.Fatalf("failure message missing from records: %q", joined)
	}
	for _, leak := range []string{"[recoverable]", "stage:", "executor"} {
		if strings.Contains(joined, leak) {
			t.Errorf("failure line leaked internal classification/stage %q: %q", leak, joined)
		}
	}
}
