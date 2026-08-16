package presentation

import (
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// ── PHASE 5 — EXECUTION NARRATIVE LAYER ────────────────────────────────────
//
// Claude-style UX separates machine events from the human narrative. The
// ExecutionNarrative is deterministic: the same events always yield the same
// sentences, no LLM is ever consulted, and the UI reads — never authors —
// narration text.

// TestNarrativeStrategySentences pins the deterministic strategy sentences.
func TestNarrativeStrategySentences(t *testing.T) {
	cases := []struct {
		strategy string
		want     string
	}{
		{"targeted_mutation", "Preparing a targeted edit"},
		{"direct_response", "Answering directly"},
		{"repository_investigation", "Investigating the repository"},
		{"multi_file_planning", "Planning the change"},
		{"targeted_reasoning", "Preparing execution"},
	}
	for _, tc := range cases {
		n := NewExecutionNarrative()
		n.Project(events.NewExecutionStarted("r", "build", "x"))
		n.Project(events.NewStrategySelected("r", tc.strategy, true, "reason"))
		if got := n.CurrentHuman(); got != tc.want {
			t.Errorf("strategy %s → %q, want %q", tc.strategy, got, tc.want)
		}
	}
}

// TestNarrativeArtifactSentences pins the deterministic artifact sentences.
func TestNarrativeArtifactSentences(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{"patch", "Generated a proposed change"},
		{"plan", "Drafted a plan"},
		{"investigation", "Completed the investigation"},
		{"response", "Generated response"},
	}
	for _, tc := range cases {
		n := NewExecutionNarrative()
		n.Project(events.NewExecutionStarted("r", "build", "x"))
		n.Project(events.NewArtifactProduced("r", tc.kind, "index.html"))
		if got := n.CurrentHuman(); got != tc.want {
			t.Errorf("artifact %s → %q, want %q", tc.kind, got, tc.want)
		}
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
