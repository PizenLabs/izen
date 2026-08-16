package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── PHASE 4 — EXECUTION VIEW PROJECTION (single renderer state) ─────────────
//
// The gated RuntimeExecutor path renders its execution status ONLY from the
// presentation.ExecutionProjection — a single ExecutionViewState reduced from
// the canonical runtime events. These tests pin:
//
//  1. A gated dispatch starts the projection (Running "Thinking...").
//  2. The renderer derives the loading-dock text from the projection's human
//     step — never from a UI-invented label.
//  3. A terminal event ALWAYS transitions the projection into a terminal phase
//     and clears the shimmer.
//  4. No impossible state renders: a running step can never follow a terminal
//     phase, and the dock never shows a step the runtime never emitted.

// TestGatedDispatchStartsExecutionProjection pins that a gated dispatch
// initialises the single execution-view projection WITHOUT fabricating any
// runtime state: the projection is Idle (nothing truthful to render) until the
// first canonical execution event arrives.
func TestGatedDispatchStartsExecutionProjection(t *testing.T) {
	mock := &mockProvider{}
	m := gatedDispatchModel(t, mock, map[string]string{"note.txt": "foo\nbar\nbaz\n"})
	m.state = StateChat

	cmd := m.runGatedLine("$hot change bar to qux @note.txt")
	if cmd == nil {
		t.Fatal("gated execution returned nil command")
	}
	if m.execView == nil {
		t.Fatal("execView not initialised at gated dispatch")
	}
	// No static dispatch-time step: the projection stays idle until a real
	// runtime event arrives (no "Understanding request", no fake thinking).
	if m.execView.Active() {
		t.Fatal("execView must NOT be active at dispatch — no event has occurred yet")
	}
	if m.execView.HumanStep() != "" {
		t.Fatalf("initial human step = %q, want \"\" (a step requires a real event)", m.execView.HumanStep())
	}
	// The dock TEXT derives from the projection — with no event there is no
	// text claim. The dock SURFACE (animated spinner + contextual tip) stays
	// alive so the execution is never a frozen pane.
	if dock := m.composeDockText(); dock != "" {
		t.Fatalf("dock text %q before any runtime event — no event-derived step exists", dock)
	}
	if dock := m.renderLoadingDock(); dock == "" {
		t.Fatal("loading dock (spinner + tip) must be active during the gated execution")
	}
}

// TestGatedProjectionFollowsRuntimeEvents pins that the projection advances
// purely from the canonical runtime events and the dock follows it.
func TestGatedProjectionFollowsRuntimeEvents(t *testing.T) {
	mock := &mockProvider{}
	m := gatedDispatchModel(t, mock, map[string]string{"index.html": "<p>hi</p>"})
	m.state = StateChat

	cmd := m.runGatedLine("$hot fix typo in @index.html")
	if cmd == nil {
		t.Fatal("gated execution returned nil command")
	}

	// The runtime events arrive through handleDomainEvent (the bus projection).
	m.handleDomainEvent(events.NewExecutionStarted("g1", "hot", "fix typo in @index.html"))
	m.handleDomainEvent(events.NewTargetResolved("g1", "index.html", true, "strategy"))

	if m.execView.HumanStep() != "Reading index.html" {
		t.Fatalf("step after target.resolved = %q, want Reading index.html", m.execView.HumanStep())
	}
	if dock := m.composeDockText(); !strings.Contains(dock, "Reading index.html") {
		t.Fatalf("dock %q missing the projected target step", dock)
	}

	m.handleDomainEvent(events.NewModelInvoked("g1", "mock", 0, 0))
	m.handleDomainEvent(events.NewProviderResponse("g1", "mock", 5, 5))
	m.handleDomainEvent(events.NewArtifactProduced("g1", "patch", "index.html"))

	if m.execView.HumanStep() != "Preparing result" {
		t.Fatalf("step after artifact.produced = %q, want Preparing result", m.execView.HumanStep())
	}

	m.handleDomainEvent(events.NewApprovalRequired("g1", "index.html", "<diff>"))
	if st := m.execView.State(); st.Phase != presentation.PhaseWaitingApproval {
		t.Fatalf("phase after approval.required = %s, want waiting-approval", st.Phase)
	}

	// Human narrative matches the runtime truth, nothing invented.
	joined := strings.Join(m.execView.HumanTimeline(), "|")
	for _, want := range []string{
		"Reading index.html",
		"Analyzing",
		"Preparing result",
		"Waiting for approval",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("human narrative missing %q: %v", want, m.execView.HumanTimeline())
		}
	}
}

// TestGatedProjectionTerminalClearsLoading pins that a terminal runtime event
// transitions the projection to a terminal phase AND clears the loading
// state — no stale spinner after success.
func TestGatedProjectionTerminalClearsLoading(t *testing.T) {
	mock := &mockProvider{}
	m := gatedDispatchModel(t, mock, map[string]string{"note.txt": "foo\nbar\nbaz\n"})
	m.state = StateChat

	cmd := m.runGatedLine("$hot change bar to qux @note.txt")
	if cmd == nil {
		t.Fatal("gated execution returned nil command")
	}
	// The gated path keeps the loading dock alive (spinner + tips) but its
	// text derives ONLY from real events — a dispatch-time progress template is
	// never claimed. The in-flight marker is the loading signal set at t=0.
	if !m.shimmerActive {
		t.Fatal("gated dispatch must keep the loading dock (spinner + tips) active")
	}
	if m.shimmerText != "" {
		t.Fatalf("gated dispatch must not seed a static shimmer text, got %q", m.shimmerText)
	}
	if !m.executionResolving {
		t.Fatal("execution in-flight marker not set at dispatch")
	}

	// Terminal success event → projection terminal + loading state released.
	m.handleDomainEvent(events.NewExecutionStarted("g2", "hot", "change bar to qux"))
	m.handleDomainEvent(events.NewArtifactProduced("g2", "patch", "note.txt"))
	m.handleDomainEvent(events.NewExecutionFinished("g2", true, "completed"))

	st := m.execView.State()
	if st.Phase != presentation.PhaseCompleted {
		t.Fatalf("phase after finished = %s, want completed", st.Phase)
	}
	if m.execView.HumanStep() != "" {
		t.Fatalf("running step %q survives the terminal event", m.execView.HumanStep())
	}
	// clearExecutionLoading is gated on executionResolving; simulate the full
	// terminal flow so the loading state is released. A terminal event is
	// authoritative execution truth — it must release the in-flight marker.
	m.executionResolving = true
	m.handleDomainEvent(events.NewExecutionFinished("g2", true, "completed"))
	if m.executionResolving {
		t.Fatal("terminal event left the execution in-flight marker set")
	}
}

// TestUIProjectionNeverRendersImpossibleStates pins that after a terminal
// phase the renderer can never resurrect a running step — even if a stray
// runtime event arrives out of order.
func TestUIProjectionNeverRendersImpossibleStates(t *testing.T) {
	mock := &mockProvider{}
	m := gatedDispatchModel(t, mock, map[string]string{"note.txt": "foo\nbar\nbaz\n"})
	m.state = StateChat

	cmd := m.runGatedLine("$hot change bar to qux @note.txt")
	if cmd == nil {
		t.Fatal("gated execution returned nil command")
	}

	m.handleDomainEvent(events.NewExecutionStarted("g3", "hot", "change bar to qux"))
	m.handleDomainEvent(events.NewExecutionFinished("g3", true, "completed"))
	// A stray running event for the SAME request after the terminal event.
	m.handleDomainEvent(events.NewArtifactProduced("g3", "patch", "note.txt"))

	st := m.execView.State()
	if !st.Phase.Terminal() {
		t.Fatalf("phase = %s after terminal + stray event, want terminal", st.Phase)
	}
	if m.execView.HumanStep() != "" {
		t.Fatalf("renderer resurrected a running step after terminal: %q", m.execView.HumanStep())
	}
	// The dock must not claim any step.
	if dock := m.composeDockText(); strings.Contains(dock, "proposed change") && m.shimmerActive {
		t.Fatalf("dock claims a running step after terminal: %q", dock)
	}
}

// TestNarrativePanelRendersProjectionPins the Phase 5 TUI contract: the gated
// execution renders its human narrative panel from the projection — never raw
// machine events.
func TestNarrativePanelRendersProjection(t *testing.T) {
	mock := &mockProvider{}
	m := gatedDispatchModel(t, mock, map[string]string{"index.html": "<p>hi</p>"})
	m.state = StateChat

	cmd := m.runGatedLine("$hot fix typo in @index.html")
	if cmd == nil {
		t.Fatal("gated execution returned nil command")
	}

	// No event has arrived yet: the panel must be empty — a dispatch-time
	// "Understanding request" seed would be a fabricated step.
	if panel := m.renderExecutionNarrative(); panel != "" {
		t.Fatalf("narrative panel renders a step before any runtime event: %q", panel)
	}

	// Advancing runtime events project the next narrative steps.
	m.handleDomainEvent(events.NewExecutionStarted("n1", "hot", "fix typo in @index.html"))
	m.handleDomainEvent(events.NewTargetResolved("n1", "index.html", true, "strategy"))
	panel := m.renderExecutionNarrative()
	if !strings.Contains(panel, "Reading index.html") {
		t.Fatalf("narrative panel missing the projected step: %q", panel)
	}
	if strings.Contains(panel, "execution.target.resolved") {
		t.Fatalf("narrative panel leaked a machine event: %q", panel)
	}
}
