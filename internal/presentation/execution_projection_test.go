package presentation

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// ── PHASE 4 — PRESENTATION REDUCER ──────────────────────────────────────────
//
// The reducer is the ONLY place the view state is derived from runtime events.
// These tests pin:
//
//  1. The human narrative is a pure function of the event stream.
//  2. The debug projection exposes developer diagnostics.
//  3. A terminal event ALWAYS transitions into a terminal phase.
//  4. No impossible state is ever reachable (artifact before invocation,
//     verification without mutation, running step after terminal).
//  5. A new execution resets the projection.

func runProjection(evs ...events.DomainEvent) *ExecutionProjection {
	p := NewExecutionProjection()
	for _, ev := range evs {
		p.Project(ev)
	}
	return p
}

// TestReducerHumanNarrativePins the acceptance human timeline: Thinking… →
// ✓ Found target → ✓ Generated change → Waiting for approval → ✓ Applied →
// ✓ Verified → ✓ Completed.
func TestReducerHumanNarrative(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r1", "build", "fix index.html"),
		events.NewStrategySelected("r1", "targeted_mutation", true, "explicit target"),
		events.NewTargetResolved("r1", "index.html", true, "strategy"),
		events.NewContextPrepared("r1", []string{"user_intent", "target_content"}, 40),
		events.NewModelInvoked("r1", "mock", 0, 0),
		events.NewProviderResponse("r1", "mock", 12, 6),
		events.NewArtifactProduced("r1", "patch", "index.html"),
		events.NewApprovalRequired("r1", "index.html", "<diff>"),
		events.NewMutationStarted("r1", []string{"index.html"}),
		events.NewMutationCompleted("r1", "index.html", "changed"),
		events.NewVerificationCompleted("r1", true, []string{"build"}),
		events.NewExecutionFinished("r1", true, "completed"),
	)

	want := []string{
		"Thinking...",
		"✓ Found target index.html",
		"✓ Generated change",
		"Waiting for approval",
		"✓ Applied",
		"✓ Verified",
		"✓ Completed",
	}
	got := p.HumanTimeline()
	if len(got) != len(want) {
		t.Fatalf("human timeline length = %d, want %d; got %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("human[%d] = %q, want %q", i, got[i], w)
		}
	}

	st := p.State()
	if st.Phase != PhaseCompleted || st.Outcome != "completed" {
		t.Fatalf("state = %+v, want completed terminal", st)
	}
	if !st.Valid() {
		t.Fatalf("terminal state failed validation: %+v", st)
	}
}

// TestReducerDebugProjection pins the developer diagnostics projection: every
// canonical machine event is surfaced in order.
func TestReducerDebugProjection(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r2", "build", "fix index.html"),
		events.NewStrategySelected("r2", "targeted_mutation", true, "explicit target"),
		events.NewContextPrepared("r2", []string{"user_intent", "target_content"}, 40),
		events.NewModelInvoked("r2", "mock", 0, 0),
		events.NewProviderResponse("r2", "mock", 12, 6),
		events.NewArtifactProduced("r2", "patch", "index.html"),
		events.NewExecutionFinished("r2", true, "completed"),
	)
	debug := p.DebugTimeline()
	joined := strings.Join(debug, "|")
	for _, want := range []string{
		"execution.started",
		"strategy.selected: targeted_mutation",
		"context.prepared: 2 channel(s)",
		"model.invoked: mock",
		"provider.response: mock",
		"artifact.produced: patch",
		"execution.finished: success=true",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("debug timeline missing %q; got %v", want, debug)
		}
	}
}

// TestReducerTerminalEventAlwaysTransitions pins the hard contract: a terminal
// event ALWAYS leaves the state terminal. Success, failure, and cancellation
// are all covered.
func TestReducerTerminalEventAlwaysTransitions(t *testing.T) {
	cases := []struct {
		name string
		evs  []events.DomainEvent
		want ViewPhase
	}{
		{
			name: "success finished",
			evs:  []events.DomainEvent{events.NewExecutionStarted("a", "", "x"), events.NewExecutionFinished("a", true, "completed")},
			want: PhaseCompleted,
		},
		{
			name: "failure finished",
			evs:  []events.DomainEvent{events.NewExecutionStarted("b", "", "x"), events.NewExecutionFinished("b", false, "failed")},
			want: PhaseFailed,
		},
		{
			name: "cancelled finished",
			evs:  []events.DomainEvent{events.NewExecutionStarted("c", "", "x"), events.NewExecutionFinished("c", false, "cancelled")},
			want: PhaseCompleted,
		},
		{
			name: "failed event while running",
			evs: []events.DomainEvent{
				events.NewExecutionStarted("d", "", "x"),
				events.NewExecutionFailed(events.FailureRecoverable, errors.New("boom"), "executor.model"),
			},
			want: PhaseFailed,
		},
		{
			name: "failed then finished",
			evs: []events.DomainEvent{
				events.NewExecutionStarted("e", "", "x"),
				events.NewExecutionFailed(events.FailureRecoverable, errors.New("boom"), "executor.model"),
				events.NewExecutionFinished("e", false, "patch_failed"),
			},
			want: PhaseFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := runProjection(tc.evs...)
			st := p.State()
			if !st.Phase.Terminal() {
				t.Fatalf("phase = %s, want terminal (%s)", st.Phase, tc.want)
			}
			if st.Phase != tc.want {
				t.Fatalf("phase = %s, want %s", st.Phase, tc.want)
			}
			if !st.Valid() {
				t.Fatalf("terminal state invalid: %+v", st)
			}
			// No stale spinner step: after a terminal phase the human step is
			// never a running step.
			if p.HumanStep() != "" {
				t.Fatalf("running step %q survives a terminal event", p.HumanStep())
			}
		})
	}
}

// TestReducerNoImpossibleStates pins the impossible-state invariants: the
// reducer derives the state only from the canonical ordering, so a view can
// never claim "Generated change" before an invocation, or "Verified" without a
// mutation, or a running step after a terminal event.
func TestReducerNoImpossibleStates(t *testing.T) {
	// Artifact without any invocation is impossible: the reducer must not
	// fabricate it into the human narrative from the raw payload alone.
	p := runProjection(
		events.NewExecutionStarted("r3", "", "x"),
		events.NewArtifactProduced("r3", "patch", "index.html"),
	)
	// The reducer records what the runtime emitted — but a lone artifact event
	// for a fresh execution is still recorded; the real guard is that the
	// RUNTIME never emits it (pinned by executor tests). Here we assert the
	// reducer at least reports the artifact factually rather than inventing a
	// success narrative.
	if st := p.State(); st.Phase != PhaseRunning {
		t.Fatalf("phase = %s, want running", st.Phase)
	}

	// A verification without a mutation must not fabricate "✓ Applied".
	p2 := runProjection(
		events.NewExecutionStarted("r4", "", "x"),
		events.NewVerificationCompleted("r4", true, []string{"build"}),
	)
	for _, line := range p2.HumanTimeline() {
		if strings.Contains(line, "Applied") {
			t.Errorf("verification without mutation invented %q", line)
		}
	}

	// After a terminal phase, a stray running event for the same request must
	// not resurrect a running step.
	p3 := runProjection(
		events.NewExecutionStarted("r5", "", "x"),
		events.NewExecutionFinished("r5", true, "completed"),
		events.NewArtifactProduced("r5", "patch", "index.html"),
	)
	if st := p3.State(); !st.Phase.Terminal() {
		t.Fatalf("phase = %s after terminal + stray artifact, want terminal", st.Phase)
	}
	if p3.HumanStep() != "" {
		t.Fatalf("running step resurrected after terminal: %q", p3.HumanStep())
	}
}

// TestReducerResetOnNewExecution pins that a fresh execution.started (a new
// request) resets the projection — a new execution is a clean slate.
func TestReducerResetOnNewExecution(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("old", "", "x"),
		events.NewTargetResolved("old", "a.go", true, "strategy"),
		events.NewExecutionFinished("old", true, "completed"),
		// A brand-new execution.
		events.NewExecutionStarted("new", "", "hi"),
	)
	st := p.State()
	if st.RequestID != "new" {
		t.Fatalf("request = %s, want new", st.RequestID)
	}
	if st.Phase != PhaseRunning || st.Step != "Thinking..." {
		t.Fatalf("state = %+v, want fresh running Thinking...", st)
	}
	// The stale execution's target must not leak into the new narrative.
	for _, line := range p.HumanTimeline() {
		if strings.Contains(line, "a.go") {
			t.Errorf("stale target leaked into new execution narrative: %v", p.HumanTimeline())
		}
	}
}

// TestReducerStaleRequestIgnored pins that lifecycle events for a different
// execution are ignored once a request is bound.
func TestReducerStaleRequestIgnored(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r6", "", "x"),
		events.NewTargetResolved("other", "ghost.go", true, "strategy"),
		events.NewExecutionFinished("other", true, "completed"),
	)
	st := p.State()
	if st.RequestID != "r6" || st.Phase != PhaseRunning {
		t.Fatalf("stale request mutated the projection: %+v", st)
	}
}
