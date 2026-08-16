package presentation

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// ── PHASE 6 — VISIBILITY LAYERS ─────────────────────────────────────────────
//
// The presentation layer computes an ExecutionFrame per Visibility. The
// renderer consumes the frame and only formats it. These tests pin the strict
// separation:
//
//  1. NORMAL: human narrative only — no provider names, strategies, tokens, or
//     event names.
//  2. EXPANDED: NORMAL + runtime metadata (strategy, context, model, tokens,
//     duration, artifacts).
//  3. DEBUG: EXPANDED + the full machine event stream.

func fullProjection() *ExecutionProjection {
	return runProjection(
		events.NewExecutionStarted("r1", "build", "fix index.html"),
		events.NewStrategySelected("r1", "targeted_mutation", true, "explicit target"),
		events.NewTargetResolved("r1", "index.html", true, "strategy"),
		events.NewContextPrepared("r1", []string{"user_intent", "target_content"}, 40),
		events.NewModelInvoked("r1", "mock", 0, 0),
		events.NewProviderResponse("r1", "mock", 12, 6),
		events.NewArtifactProduced("r1", "patch", "index.html"),
		events.NewExecutionFinished("r1", true, "completed"),
	)
}

// TestFrameNormalHidesInternals pins the NORMAL layer contract: only human
// narrative, never providers/strategies/tokens/event names.
func TestFrameNormalHidesInternals(t *testing.T) {
	f := fullProjection().Frame(VisibilityNormal)

	if f.Visibility != VisibilityNormal {
		t.Fatalf("frame visibility = %v, want normal", f.Visibility)
	}
	if len(f.Steps) == 0 {
		t.Fatal("normal frame must carry the human narrative steps")
	}
	joined := strings.Join(f.StepsSentence(), "|")
	// Provider names must never appear.
	if strings.Contains(joined, "mock") {
		t.Errorf("normal frame leaked a provider/model name: %q", joined)
	}
	// Event names must never appear.
	if strings.Contains(joined, "execution.") || strings.Contains(joined, "strategy.selected") ||
		strings.Contains(joined, "target.resolved") {
		t.Errorf("normal frame leaked a raw event name: %q", joined)
	}
	// Token counts must never appear.
	if strings.Contains(joined, "12") || strings.Contains(joined, "token") {
		t.Errorf("normal frame leaked token usage: %q", joined)
	}
	// Details must be empty in NORMAL.
	if !f.Details.Empty() {
		t.Errorf("normal frame leaked details: %+v", f.Details)
	}
	if len(f.Events) != 0 {
		t.Errorf("normal frame leaked the event stream: %v", f.Events)
	}
}

// TestFrameExpandedShowsDetails pins the EXPANDED layer: NORMAL + runtime
// metadata.
func TestFrameExpandedShowsDetails(t *testing.T) {
	f := fullProjection().Frame(VisibilityExpanded)

	if len(f.Steps) == 0 {
		t.Fatal("expanded frame must carry the human narrative steps")
	}
	d := f.Details
	if d.Strategy != "targeted_mutation" {
		t.Errorf("expanded frame missing strategy: %q", d.Strategy)
	}
	if len(d.ContextChannels) != 2 {
		t.Errorf("expanded frame missing context policy: %v", d.ContextChannels)
	}
	if d.Model != "mock" {
		t.Errorf("expanded frame missing model: %q", d.Model)
	}
	if d.TokenInput != 12 || d.TokenOutput != 6 {
		t.Errorf("expanded frame missing token usage: %d/%d", d.TokenInput, d.TokenOutput)
	}
	if d.Duration() <= 0 {
		t.Error("expanded frame missing duration")
	}
	if len(d.Artifacts) != 1 || d.Artifacts[0].Kind != "patch" {
		t.Errorf("expanded frame missing artifacts: %+v", d.Artifacts)
	}
	// EXPANDED does NOT include the raw event stream.
	if len(f.Events) != 0 {
		t.Errorf("expanded frame leaked the event stream: %v", f.Events)
	}
}

// TestFrameDebugShowsEvents pins the DEBUG layer: the full machine event
// stream is present.
func TestFrameDebugShowsEvents(t *testing.T) {
	f := fullProjection().Frame(VisibilityDebug)

	if len(f.Events) == 0 {
		t.Fatal("debug frame must carry the machine event stream")
	}
	joined := strings.Join(f.Events, "|")
	for _, want := range []string{"execution.started", "strategy.selected", "artifact.produced", "execution.finished"} {
		if !strings.Contains(joined, want) {
			t.Errorf("debug frame missing lifecycle event %q: %v", want, f.Events)
		}
	}
	// DEBUG also carries the metadata.
	if f.Details.Strategy != "targeted_mutation" {
		t.Errorf("debug frame missing strategy metadata: %q", f.Details.Strategy)
	}
}

// TestFrameTerminalPins that a terminal execution frame carries the terminal
// phase and the derived terminal steps — and that its steps are never flagged
// Current (no live step after completion).
func TestFrameTerminal(t *testing.T) {
	p := runProjection(
		events.NewExecutionStarted("r1", "build", "x"),
		events.NewExecutionFinished("r1", true, "completed"),
	)
	f := p.Frame(VisibilityNormal)
	if !f.Terminal() {
		t.Fatal("terminal execution frame must report Terminal")
	}
	if f.State.Phase != PhaseCompleted {
		t.Fatalf("frame phase = %s, want completed", f.State.Phase)
	}
	for _, s := range f.Steps {
		if s.Current {
			t.Errorf("terminal frame flagged a live step: %+v", s)
		}
	}
}

// StepsSentence flattens the frame steps to their sentences.
func (f ExecutionFrame) StepsSentence() []string {
	out := make([]string, 0, len(f.Steps))
	for _, s := range f.Steps {
		out = append(out, s.Sentence)
	}
	return out
}

// TestVisibilityString pins the canonical visibility names.
func TestVisibilityString(t *testing.T) {
	cases := []struct {
		v    Visibility
		want string
	}{
		{VisibilityNormal, "normal"},
		{VisibilityExpanded, "expanded"},
		{VisibilityDebug, "debug"},
	}
	for _, tc := range cases {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("Visibility(%d).String() = %q, want %q", tc.v, got, tc.want)
		}
		if !tc.v.Valid() {
			t.Errorf("Visibility(%d) must be Valid", tc.v)
		}
	}
	if (Visibility(99)).Valid() {
		t.Error("out-of-range visibility must not be Valid")
	}
}
