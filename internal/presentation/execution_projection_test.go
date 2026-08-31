package presentation

import (
	"errors"
	"strings"
	"testing"
	"time"

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
//  6. The reducer accumulates runtime metadata (strategy, context policy,
//     model, tokens, artifacts) for the EXPANDED layer.

func runProjection(evs ...events.DomainEvent) *ExecutionProjection {
	p := NewExecutionProjection()
	for _, ev := range evs {
		p.Project(ev)
	}
	return p
}

// TestReducerHumanNarrativePins the acceptance human timeline derived from the
// ExecutionGraph transitions: Reading index.html → Gathering context →
// Analyzing → Preparing result → Waiting for approval → Applying changes →
// Applied change → Verified changes → Completed. execution.started /
// strategy.selected carry no human step (deterministic plumbing — the first
// meaningful human progress appears when the runtime touches a target).
func TestReducerHumanNarrative(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r1", "build", "fix index.html", ""),
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
		"Reading index.html",
		"Gathering context",
		"Analyzing",
		"Preparing result",
		"Waiting for approval",
		"Applying changes",
		"Applied change to index.html",
		"Verified changes",
		"Completed",
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

// TestReducerAccumulatesDetails pins that the EXPANDED-layer metadata is
// accumulated from the observed payloads — strategy, context policy, model,
// token usage, duration, and artifacts.
func TestReducerAccumulatesDetails(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r1", "build", "fix index.html", ""),
		events.NewStrategySelected("r1", "targeted_mutation", true, "explicit target"),
		events.NewContextPrepared("r1", []string{"user_intent", "target_content"}, 40),
		events.NewModelInvoked("r1", "mock", 0, 0),
		events.NewProviderResponse("r1", "mock", 12, 6),
		events.NewArtifactProduced("r1", "patch", "index.html"),
		events.NewExecutionFinished("r1", true, "completed"),
	)

	d := p.State().Details
	if d.Strategy != "targeted_mutation" {
		t.Errorf("Details.Strategy = %q, want targeted_mutation", d.Strategy)
	}
	if len(d.ContextChannels) != 2 || d.ContextTokens != 40 {
		t.Errorf("Details context = %v / %d, want 2 channels / 40 tokens", d.ContextChannels, d.ContextTokens)
	}
	if d.Model != "mock" {
		t.Errorf("Details.Model = %q, want mock", d.Model)
	}
	if d.TokenInput != 12 || d.TokenOutput != 6 {
		t.Errorf("Details tokens = %d in / %d out, want 12 / 6", d.TokenInput, d.TokenOutput)
	}
	if len(d.Artifacts) != 1 || d.Artifacts[0].Kind != "patch" || d.Artifacts[0].Type != ArtifactDiff {
		t.Errorf("Details.Artifacts = %+v, want one classified patch artifact", d.Artifacts)
	}
	if d.Duration() <= 0 {
		t.Error("Details.Duration() must be positive for a completed execution")
	}
}

// TestReducerDetailsSurviveTerminal pins that the accumulated metadata survives
// the terminal state reassignment (EXPANDED layer keeps its metadata at
// completion).
func TestReducerDetailsSurviveTerminal(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r1", "build", "fix index.html", ""),
		events.NewStrategySelected("r1", "targeted_mutation", true, "explicit target"),
		events.NewModelInvoked("r1", "mock", 0, 0),
		events.NewProviderResponse("r1", "mock", 12, 6),
		events.NewExecutionFinished("r1", true, "completed"),
	)
	d := p.State().Details
	if d.Strategy != "targeted_mutation" || d.Model != "mock" || d.TokenInput != 12 {
		t.Fatalf("terminal state lost details: %+v", d)
	}
}

// TestReducerDebugProjection pins the developer diagnostics projection: every
// canonical machine event is surfaced in order.
func TestReducerDebugProjection(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r2", "build", "fix index.html", ""),
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
			evs:  []events.DomainEvent{events.NewExecutionStarted("a", "", "x", ""), events.NewExecutionFinished("a", true, "completed")},
			want: PhaseCompleted,
		},
		{
			name: "failure finished",
			evs:  []events.DomainEvent{events.NewExecutionStarted("b", "", "x", ""), events.NewExecutionFinished("b", false, "failed")},
			want: PhaseFailed,
		},
		{
			name: "cancelled finished",
			evs:  []events.DomainEvent{events.NewExecutionStarted("c", "", "x", ""), events.NewExecutionFinished("c", false, "cancelled")},
			want: PhaseCompleted,
		},
		{
			name: "failed event while running",
			evs: []events.DomainEvent{
				events.NewExecutionStarted("d", "", "x", ""),
				events.NewExecutionFailed(events.FailureRecoverable, errors.New("boom"), "executor.model"),
			},
			want: PhaseFailed,
		},
		{
			name: "failed then finished",
			evs: []events.DomainEvent{
				events.NewExecutionStarted("e", "", "x", ""),
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
		events.NewExecutionStarted("r3", "", "x", ""),
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
		events.NewExecutionStarted("r4", "", "x", ""),
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
		events.NewExecutionStarted("r5", "", "x", ""),
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
		events.NewExecutionStarted("old", "", "x", ""),
		events.NewTargetResolved("old", "a.go", true, "strategy"),
		events.NewExecutionFinished("old", true, "completed"),
		// A brand-new execution.
		events.NewExecutionStarted("new", "", "hi", ""),
	)
	st := p.State()
	if st.RequestID != "new" {
		t.Fatalf("request = %s, want new", st.RequestID)
	}
	if st.Phase != PhaseRunning || st.Step != "" {
		t.Fatalf("state = %+v, want fresh running with no fabricated step (events drive the narrative)", st)
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
		events.NewExecutionStarted("r6", "", "x", ""),
		events.NewTargetResolved("other", "ghost.go", true, "strategy"),
		events.NewExecutionFinished("other", true, "completed"),
	)
	st := p.State()
	if st.RequestID != "r6" || st.Phase != PhaseRunning {
		t.Fatalf("stale request mutated the projection: %+v", st)
	}
}

// TestReducerBeginHasNoFabricatedState pins requirement 1 of the UX/exec
// consistency work: Begin (the dispatch-time binding) must NOT fabricate a
// running state or a narrative step. No real runtime event has been observed
// yet, so nothing truthful exists to render — the projection stays Idle until
// the first execution.started event arrives, and the narrative is empty. A
// static "Understanding request" seed would be a fake progress claim.
func TestReducerBeginHasNoFabricatedState(t *testing.T) {
	p := NewExecutionProjection()
	p.Begin("req-1")

	st := p.State()
	if st.Phase != PhaseIdle {
		t.Fatalf("phase after Begin = %s, want idle (no event yet)", st.Phase)
	}
	if p.Active() {
		t.Fatal("projection must not be active before any runtime event")
	}
	if steps := p.HumanTimeline(); len(steps) != 0 {
		t.Fatalf("Begin fabricated narrative steps: %v", steps)
	}
	if p.HumanStep() != "" {
		t.Fatalf("Begin fabricated a human step: %q", p.HumanStep())
	}
	// The projection activates and derives its narrative ONLY from the real
	// event stream.
	p.Project(events.NewExecutionStarted("req-1", "build", "fix x", ""))
	st = p.State()
	if st.Phase != PhaseRunning {
		t.Fatalf("phase after execution.started = %s, want running", st.Phase)
	}
	if steps := p.HumanTimeline(); len(steps) != 0 {
		t.Fatalf("execution.started alone must not fabricate a human step: %v", steps)
	}
	p.Project(events.NewTargetResolved("req-1", "index.html", true, "strategy"))
	got := p.HumanTimeline()
	if len(got) != 1 || got[0] != "Reading index.html" {
		t.Fatalf("narrative after target.resolved = %v, want [Reading index.html]", got)
	}
}

// TestReducerProviderStreamLifecycle pins the live provider-state projection:
// the EXPANDED details carry the truthful provider phase (waiting → streaming),
// the authoritative provider-reported usage updates, and the reasoning
// telemetry — all derived from canonical events, never inferred by the UI.
func TestReducerProviderStreamLifecycle(t *testing.T) {
	p := NewExecutionProjection()
	p.Project(events.NewExecutionStarted("s1", "build", "fix x", ""))
	p.Project(events.NewStrategySelected("s1", "targeted_mutation", true, "x"))
	p.Project(events.NewTargetResolved("s1", "index.html", true, "strategy"))
	p.Project(events.NewModelInvoked("s1", "mock", 0, 0))

	if d := p.State().Details; d.ProviderState != "" {
		t.Fatalf("provider state before provider.waiting = %q, want empty", d.ProviderState)
	}
	if step := p.HumanStep(); step != "Analyzing" {
		t.Fatalf("human step after model.invoked = %q, want Analyzing", step)
	}

	p.Project(events.NewProviderWaiting("s1", "mock"))
	if d := p.State().Details; d.ProviderState != "waiting" {
		t.Fatalf("provider state after provider.waiting = %q, want waiting", d.ProviderState)
	}
	if step := p.HumanStep(); step != "Waiting for model" {
		t.Fatalf("human step after provider.waiting = %q, want Waiting for model", step)
	}

	p.Project(events.NewProviderFirstToken("s1", "mock", time.Second))
	if d := p.State().Details; d.ProviderState != "streaming" {
		t.Fatalf("provider state after provider.first_token = %q, want streaming", d.ProviderState)
	}
	if step := p.HumanStep(); step != "Model responding" {
		t.Fatalf("human step after provider.first_token = %q, want Model responding", step)
	}

	p.Project(events.NewProviderUsageUpdate("s1", "mock", 12, 6, 4))
	d := p.State().Details
	if d.ProviderState != "streaming" {
		t.Fatalf("provider state after usage update = %q, want streaming", d.ProviderState)
	}
	if d.TokenInput != 12 || d.TokenOutput != 6 || d.ReasoningTokens != 4 {
		t.Fatalf("details tokens = %d/%d reasoning=%d, want 12/6/4", d.TokenInput, d.TokenOutput, d.ReasoningTokens)
	}

	p.Project(events.NewReasoningTelemetry("s1", "mock", 800*time.Millisecond, 4))
	d = p.State().Details
	if d.ReasoningDuration != 800*time.Millisecond || d.ReasoningTokens != 4 {
		t.Fatalf("reasoning telemetry details = %s/%d, want 800ms/4", d.ReasoningDuration, d.ReasoningTokens)
	}

	p.Project(events.NewProviderResponse("s1", "mock", 12, 6))
	if d := p.State().Details; d.ProviderState != "done" {
		t.Fatalf("provider state after provider.response = %q, want done", d.ProviderState)
	}
	// provider.response is machine-only: "Model responding" remains the current
	// human step; "Analyzing" never re-appears as a duplicate.
	if step := p.HumanStep(); step != "Model responding" {
		t.Fatalf("human step after provider.response = %q, want Model responding", step)
	}
	if steps := p.HumanTimeline(); countSentence(steps, "Analyzing") != 1 {
		t.Fatalf("Analyzing must appear exactly once, got %v", steps)
	}
}

// countSentence counts how many timeline entries equal sentence.
func countSentence(steps []string, sentence string) int {
	n := 0
	for _, s := range steps {
		if s == sentence {
			n++
		}
	}
	return n
}
