package presentation

import (
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// ── PHASE 6 — EXECUTION NARRATIVE DERIVED FROM GRAPH TRANSITIONS ───────────
//
// The human narrative is derived from ExecutionGraph transitions — never a
// static predefined step. A human step exists ONLY because a real transition
// occurred, and the sentence is the deterministic derivation of that
// transition. These tests pin:
//
//  1. Every canonical transition derives the expected sentence.
//  2. The narrative never contains a step for a transition that did not occur
//     (no fake/static steps).
//  3. The narrative changes according to the actual graph state (partial
//     graphs produce partial narratives).
//  4. Machine and human records are strictly separated.
//  5. Terminal transitions derive terminal sentences.

// TestNarrativeTransitionDerivation pins the canonical transition → sentence
// derivation table.
func TestNarrativeTransitionDerivation(t *testing.T) {
	cases := []struct {
		name string
		evs  []events.DomainEvent
		want string
	}{
		{
			name: "strategy.selected",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewStrategySelected("r", "targeted_mutation", true, "reason")},
			want: "Understanding request",
		},
		{
			name: "target.resolved",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewTargetResolved("r", "index.html", true, "strategy")},
			want: "Inspecting index.html",
		},
		{
			name: "context.prepared",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewContextPrepared("r", []string{"user_intent"}, 40)},
			want: "Gathering context",
		},
		{
			name: "provider.invoked",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewModelInvoked("r", "mock", 0, 0)},
			want: "Generating response",
		},
		{
			name: "artifact.produced",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewArtifactProduced("r", "patch", "index.html")},
			want: "Preparing result",
		},
		{
			name: "verification.completed",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewVerificationCompleted("r", true, []string{"build"})},
			want: "Verified changes",
		},
		{
			name: "approval.required",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewApprovalRequired("r", "index.html", "<d>")},
			want: "Waiting for approval",
		},
		{
			name: "mutation.started",
			evs:  []events.DomainEvent{events.NewExecutionStarted("r", "build", "x"), events.NewMutationStarted("r", []string{"index.html"})},
			want: "Applying changes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := NewExecutionNarrative()
			for _, ev := range tc.evs {
				n.Project(ev)
			}
			if got := n.CurrentHuman(); got != tc.want {
				t.Errorf("transition %s → %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestNarrativeNoFakeStaticSteps pins that the narrative NEVER contains a step
// for a transition that did not occur: a partial graph produces a partial
// narrative, never a canned full-lifecycle script.
func TestNarrativeNoFakeStaticSteps(t *testing.T) {
	// Only execution.started + strategy.selected occurred. The narrative must
	// NOT claim inspection, context, generation, results, or verification.
	n := NewExecutionNarrative()
	n.Project(events.NewExecutionStarted("r", "build", "fix index.html"))
	n.Project(events.NewStrategySelected("r", "targeted_mutation", true, "x"))

	got := n.Human()
	want := []string{"Understanding request"}
	if len(got) != len(want) {
		t.Fatalf("narrative for a partial graph = %v, want %v (no fake steps)", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("human[%d] = %q, want %q (a step that never happened leaked)", i, got[i], w)
		}
	}
	for _, forbidden := range []string{"Inspecting", "Gathering", "Generating", "Preparing", "Verified", "Applied"} {
		for _, line := range got {
			if contains(line, forbidden) {
				t.Errorf("fake step %q leaked into partial narrative %v", forbidden, got)
			}
		}
	}
}

// TestNarrativeChangesWithGraphState pins that the narrative reflects the
// ACTUAL graph state: the more transitions occur, the longer the narrative.
func TestNarrativeChangesWithGraphState(t *testing.T) {
	stream := []events.DomainEvent{
		events.NewExecutionStarted("r", "build", "fix index.html"),
		events.NewStrategySelected("r", "targeted_mutation", true, "x"),
		events.NewTargetResolved("r", "index.html", true, "strategy"),
		events.NewModelInvoked("r", "mock", 0, 0),
		events.NewArtifactProduced("r", "patch", "index.html"),
		events.NewVerificationCompleted("r", true, []string{"build"}),
	}

	n := NewExecutionNarrative()
	// A partial graph (only first two transitions) has a shorter narrative than
	// the full graph — the narrative is a function of the actual graph state.
	var partialLen int
	for i, ev := range stream {
		n.Project(ev)
		if i == 1 {
			partialLen = len(n.Human())
		}
	}
	fullLen := len(n.Human())
	if partialLen >= fullLen {
		t.Fatalf("narrative did not grow with graph state: partial=%d full=%d", partialLen, fullLen)
	}
}

// TestNarrativeDeterministic pins determinism: the same event stream always
// yields the same narrative.
func TestNarrativeDeterministic(t *testing.T) {
	stream := []events.DomainEvent{
		events.NewExecutionStarted("r", "build", "fix index.html"),
		events.NewStrategySelected("r", "targeted_mutation", true, "x"),
		events.NewTargetResolved("r", "index.html", true, "strategy"),
		events.NewModelInvoked("r", "mock", 0, 0),
		events.NewArtifactProduced("r", "patch", "index.html"),
		events.NewApprovalRequired("r", "index.html", "<d>"),
		events.NewMutationCompleted("r", "index.html", "changed"),
		events.NewVerificationCompleted("r", true, []string{"build"}),
		events.NewExecutionFinished("r", true, "completed"),
	}
	var first []string
	for run := 0; run < 20; run++ {
		n := NewExecutionNarrative()
		for _, ev := range stream {
			n.Project(ev)
		}
		got := n.Human()
		if run == 0 {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("narrative length changed across runs: %v vs %v", got, first)
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("narrative nondeterministic at %d: %q vs %q", i, got[i], first[i])
			}
		}
	}
}

// TestNarrativeMachineSeparated pins the machine/human separation: the machine
// records carry raw event types, the human records carry sentences only.
func TestNarrativeMachineSeparated(t *testing.T) {
	n := NewExecutionNarrative()
	n.Project(events.NewExecutionStarted("r", "build", "x"))
	n.Project(events.NewTargetResolved("r", "index.html", true, "strategy"))
	n.Project(events.NewExecutionFinished("r", true, "completed"))

	machine := n.Machine()
	if len(machine) != 3 {
		t.Fatalf("machine records = %d, want 3: %v", len(machine), machine)
	}
	if machine[1] != "execution.target.resolved: index.html" {
		t.Fatalf("machine[1] = %q, want the raw event record", machine[1])
	}
	human := n.Human()
	for _, h := range human {
		if h == "" {
			t.Fatal("human narrative contains an empty sentence")
		}
	}
	// Machine and human are strictly separated: no sentence text in machine.
	for _, m := range machine {
		if m == "Inspecting index.html" || m == "Understanding request" {
			t.Fatalf("machine record leaked a human sentence: %q", m)
		}
	}
}

// TestNarrativeTerminalSentences pins the deterministic terminal sentences.
func TestNarrativeTerminalSentences(t *testing.T) {
	cases := []struct {
		success bool
		outcome string
		want    string
	}{
		{true, "completed", "Completed"},
		{false, "cancelled", "Cancelled"},
		{false, "patch_failed", "Failed"},
	}
	for _, tc := range cases {
		n := NewExecutionNarrative()
		n.Project(events.NewExecutionStarted("r", "", "x"))
		n.Project(events.NewExecutionFinished("r", tc.success, tc.outcome))
		if got := n.CurrentHuman(); got != tc.want {
			t.Errorf("finished(success=%t, %s) → %q, want %q", tc.success, tc.outcome, got, tc.want)
		}
	}
}

// TestNarrativeStepsCarryTransitions pins that Steps exposes the canonical
// transition each step derives from — the derivation key the renderer/tests
// can inspect to prove the narrative is graph-derived.
func TestNarrativeStepsCarryTransitions(t *testing.T) {
	n := NewExecutionNarrative()
	n.Project(events.NewExecutionStarted("r", "build", "x"))
	n.Project(events.NewStrategySelected("r", "targeted_mutation", true, "reason"))
	n.Project(events.NewTargetResolved("r", "index.html", true, "strategy"))

	steps := n.Steps()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2 (execution.started + strategy.selected collapse to one Understanding request)", len(steps))
	}
	// execution.started and strategy.selected both derive "Understanding
	// request"; the identical sentence collapses into one step.
	if steps[0].Transition != "execution.started" && steps[0].Transition != "strategy.selected" {
		t.Errorf("steps[0].Transition = %q, want execution.started or strategy.selected", steps[0].Transition)
	}
	if steps[0].Sentence != "Understanding request" {
		t.Errorf("steps[0].Sentence = %q, want Understanding request", steps[0].Sentence)
	}
	if steps[1].Transition != "target.resolved" || steps[1].Sentence != "Inspecting index.html" {
		t.Errorf("steps[1] = %+v, want target.resolved / Inspecting index.html", steps[1])
	}
	// The Current flag is projection-owned (phase-aware); the narrative itself
	// does not flag it. Verify via the projection's running frame.
	p := NewExecutionProjection()
	for _, ev := range []events.DomainEvent{
		events.NewExecutionStarted("r", "build", "x"),
		events.NewStrategySelected("r", "targeted_mutation", true, "reason"),
		events.NewTargetResolved("r", "index.html", true, "strategy"),
	} {
		p.Project(ev)
	}
	f := p.Frame(VisibilityNormal)
	if len(f.Steps) != 2 || !f.Steps[1].Current {
		t.Errorf("running frame must flag the last step Current: %+v", f.Steps)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
