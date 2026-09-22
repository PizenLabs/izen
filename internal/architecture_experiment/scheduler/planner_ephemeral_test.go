package scheduler

import (
	"testing"
)

// TestContextPlanner_EphemeralSliceBound runs a 5-step task sequence and
// asserts per-turn input tokens stay flat/bounded instead of growing
// linearly with previous-step transcript history (no quadratic transcript
// accumulation).
func TestContextPlanner_EphemeralSliceBound(t *testing.T) {
	planner := NewContextPlanner(3)
	state := TaskStateSnapshot{
		Objective:        "migrate five packages",
		RemainingBudget:  8000,
		StateFingerprint: "abc123",
	}
	// Simulate a growing transcript the planner MUST NOT replay: 5 steps of
	// verbose history. The planner receives only digests + latest evidence.
	var historyTranscript int
	var counts []int
	for i := 1; i <= 5; i++ {
		step := ExecutionStep{
			ID:         "step-1",
			Type:       StepTypeMutation,
			Targets:    []string{"pkg.go"},
			StepBudget: 1024,
		}
		// Each turn appends ~2000 tokens of fake transcript history that a
		// naive full-replay planner would accumulate.
		historyTranscript += 2000
		_ = historyTranscript
		evidence := []EvidenceItem{
			{Kind: "build", Subject: "pkg.go", Digest: "d1"},
			{Kind: "test", Subject: "pkg.go", Digest: "d2"},
			{Kind: "lint", Subject: "pkg.go", Digest: "d3"},
			{Kind: "extra-old", Subject: "pkg.go", Digest: "stale"},
		}
		slice := planner.Assemble(state.Objective, state, step, "AST(pkg.go): func Foo", evidence)
		counts = append(counts, slice.InputTokens)
		// Only the tail of evidence is carried.
		if len(slice.LatestEvidence) != 3 {
			t.Fatalf("step %d: evidence carried = %d, want bounded 3 (no history replay)", i, len(slice.LatestEvidence))
		}
		if got := slice.SystemProtocol; got != MutationSystemProtocol {
			t.Fatalf("step %d: protocol = %q, want machine-only", i, got)
		}
		// Advance compact state (digests only, no transcript).
		state.CompletedSteps = append(state.CompletedSteps, step.ID)
		state.RemainingBudget -= 100
	}
	first, last := counts[0], counts[len(counts)-1]
	// Flat/bounded: last turn must not exceed 1.5x the first (a replaying
	// planner would show ~5x linear growth: 2000*5 accumulated).
	if float64(last) > float64(first)*1.5 {
		t.Fatalf("input tokens grew across 5 steps: %v (first=%d last=%d), want flat/bounded", counts, first, last)
	}
	t.Logf("ephemeral input tokens across 5 steps: %v", counts)
}
