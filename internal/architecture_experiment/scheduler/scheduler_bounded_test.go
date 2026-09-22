package scheduler

import (
	"strings"
	"testing"

	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
)

// TestScheduler_BoundedStepDecomposition proves a large multi-file mutation
// under a 1024-token constrained model decomposes into sequential bounded
// steps WITHOUT executor intervention: the union of step targets equals the
// durable task scope, every step fits the effective budget (unless a single
// target alone exceeds it), and AcceptStep rejects any executor-side target
// mutation.
func TestScheduler_BoundedStepDecomposition(t *testing.T) {
	targets := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}
	sizes := map[string]int{
		"a.go": 400, "b.go": 400, "c.go": 400,
		"d.go": 400, "e.go": 400, "f.go": 400,
	}
	spec := TaskSpec{
		Objective:           "refactor six files",
		Targets:             targets,
		EstimatedSizes:      sizes,
		TotalEstimatedSize:  2400,
		TaskRemainingBudget: 8192,
		RequestedStepBudget: 4096, // large request: provider cap must win
		ReasoningMargin:     0,
		Provider:            ptr(dprovider.DetectCapability("openrouter", "constrained-model", 1024, 32768, false, false)),
		Type:                StepTypeMutation,
	}
	sched := NewStepScheduler()
	steps := sched.Schedule(spec)
	if len(steps) < 2 {
		t.Fatalf("constrained model must decompose 2400-token task into multiple steps, got %d", len(steps))
	}
	// Effective budget = min(4096, 1024, 8192) = 1024.
	if got := sched.EffectiveBudget(spec); got != 1024 {
		t.Fatalf("EffectiveBudget = %d, want 1024 (provider cap wins)", got)
	}
	seen := map[string]int{}
	for _, st := range steps {
		if st.StepBudget != 1024 {
			t.Fatalf("step %q budget = %d, want ephemeral 1024", st.ID, st.StepBudget)
		}
		if st.Type != StepTypeMutation {
			t.Fatalf("step %q type = %q, want mutation", st.ID, st.Type)
		}
		if st.EstimatedMutationSize > 1024 {
			t.Fatalf("step %q size %d exceeds effective budget 1024", st.ID, st.EstimatedMutationSize)
		}
		for _, tg := range st.Targets {
			seen[tg]++
		}
		// Executor accepts the exact scheduled step without mutating it.
		if err := AcceptStep(st, st); err != nil {
			t.Fatalf("AcceptStep exact copy rejected: %v", err)
		}
	}
	// Durable scope preserved: every target appears exactly once.
	for _, tg := range targets {
		if seen[tg] != 1 {
			t.Fatalf("target %q appears %d times across steps, want exactly 1 (scope durable)", tg, seen[tg])
		}
	}
	// Executor-side truncation is rejected (Invariant 2).
	mutated := steps[0]
	mutated.Targets = mutated.Targets[:len(mutated.Targets)-1]
	if len(steps[0].Targets) > 0 {
		if err := AcceptStep(steps[0], mutated); err == nil {
			t.Fatal("AcceptStep must reject executor-truncated target slice")
		}
	} else if err := AcceptStep(steps[0], ExecutionStep{Targets: []string{"intruder.go"}}); err == nil {
		t.Fatal("AcceptStep must reject executor-mutated targets")
	}
	// Mutation steps carry the machine-only protocol.
	planner := NewContextPlanner(3)
	if got := planner.SystemPromptFor(StepTypeMutation); !strings.Contains(got, "ZERO PROSE") || !strings.Contains(got, "RAW TOOL CALL ONLY") {
		t.Fatalf("mutation protocol = %q, want ZERO PROSE / RAW TOOL CALL ONLY", got)
	}
}
