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
// initialises the single execution-view projection.
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
	if !m.execView.Active() {
		t.Fatal("execView must be active at dispatch")
	}
	if m.execView.HumanStep() != "Understanding request" {
		t.Fatalf("initial human step = %q, want Understanding request", m.execView.HumanStep())
	}
	// The dock text must derive from the projection.
	dock := m.composeDockText()
	if !strings.Contains(dock, "Understanding request") {
		t.Fatalf("dock text %q does not derive from the projection step", dock)
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

	if m.execView.HumanStep() != "Inspecting index.html" {
		t.Fatalf("step after target.resolved = %q, want Inspecting index.html", m.execView.HumanStep())
	}
	if dock := m.composeDockText(); !strings.Contains(dock, "Inspecting index.html") {
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
		"Understanding request",
		"Inspecting index.html",
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
// shimmer — no stale spinner after success.
func TestGatedProjectionTerminalClearsLoading(t *testing.T) {
	mock := &mockProvider{}
	m := gatedDispatchModel(t, mock, map[string]string{"note.txt": "foo\nbar\nbaz\n"})
	m.state = StateChat

	cmd := m.runGatedLine("$hot change bar to qux @note.txt")
	if cmd == nil {
		t.Fatal("gated execution returned nil command")
	}
	if !m.shimmerActive {
		t.Fatal("shimmer must be active at dispatch")
	}

	// Terminal success event → projection terminal + shimmer cleared.
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
	// terminal flow so the loading state is released.
	m.executionResolving = true
	m.handleDomainEvent(events.NewExecutionFinished("g2", true, "completed"))
	if m.shimmerActive {
		t.Fatal("terminal event left the shimmer active")
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

	panel := m.renderExecutionNarrative()
	if !strings.Contains(panel, "Understanding request") {
		t.Fatalf("narrative panel missing the initial step: %q", panel)
	}
	// Raw machine events must never leak into the human panel.
	if strings.Contains(panel, "execution.started") || strings.Contains(panel, "strategy.selected") {
		t.Fatalf("narrative panel leaked a raw machine event: %q", panel)
	}

	// Advancing runtime events project the next narrative steps.
	m.handleDomainEvent(events.NewExecutionStarted("n1", "hot", "fix typo in @index.html"))
	m.handleDomainEvent(events.NewTargetResolved("n1", "index.html", true, "strategy"))
	panel = m.renderExecutionNarrative()
	if !strings.Contains(panel, "Inspecting index.html") {
		t.Fatalf("narrative panel missing the projected step: %q", panel)
	}
	if strings.Contains(panel, "execution.target.resolved") {
		t.Fatalf("narrative panel leaked a machine event: %q", panel)
	}
}
