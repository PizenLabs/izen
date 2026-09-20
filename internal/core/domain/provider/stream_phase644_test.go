package provider

import (
	"testing"
)

// TestStreamAccumulator_OptimisticPrompt pins the Optimistic Prompt Token
// Invariant (Phase 6.4.4): prompt tokens registered at dispatch are visible
// immediately, before any chunk streams.
func TestStreamAccumulator_OptimisticPrompt(t *testing.T) {
	var a StreamAccumulator
	if _, _, known, _ := a.Snapshot(); known {
		t.Fatal("empty accumulator must be unknown")
	}
	a.SetPromptEstimate(120)
	if got := a.PromptTokens(); got != 120 {
		t.Fatalf("PromptTokens = %d, want 120", got)
	}
	p, c, known, est := a.Snapshot()
	if !known || !est {
		t.Fatalf("snapshot known=%v estimated=%v, want true/true", known, est)
	}
	if p != 120 || c != 0 {
		t.Fatalf("snapshot = (%d,%d), want (120,0)", p, c)
	}
	// Authoritative replaces the estimate.
	a.SetAuthoritative(110, 5)
	p, c, known, est = a.Snapshot()
	if !known || est {
		t.Fatalf("authoritative known=%v estimated=%v, want true/false", known, est)
	}
	if p != 110 || c != 5 {
		t.Fatalf("authoritative snapshot = (%d,%d), want (110,5)", p, c)
	}
}

// TestStreamAccumulator_RealTimeCompletion pins the Real-Time Live Streaming
// Usage Invariant: completion counters advance per emitted chunk without any
// terminal usage frame.
func TestStreamAccumulator_RealTimeCompletion(t *testing.T) {
	var a StreamAccumulator
	a.SetPromptEstimate(40)
	a.AddContent(40) // 10 tokens
	a.AddContent(40) // 20 total
	if got := a.CompletionTokens(); got != 20 {
		t.Fatalf("CompletionTokens = %d, want 20", got)
	}
	// Reasoning never inflates completion.
	a.AddReasoning(8000)
	if got := a.CompletionTokens(); got != 20 {
		t.Fatalf("reasoning inflated completion: got %d, want 20", got)
	}
	p, c, known, est := a.Snapshot()
	if !known || !est {
		t.Fatalf("partial known=%v estimated=%v, want true/true", known, est)
	}
	if p != 40 || c != 20 {
		t.Fatalf("partial snapshot = (%d,%d), want (40,20)", p, c)
	}
}

// TestStreamAccumulator_ResetClearsStale pins that a cancelled request never
// leaks stale counts: Reset clears live mirrors.
func TestStreamAccumulator_ResetClearsStale(t *testing.T) {
	var a StreamAccumulator
	a.SetPromptEstimate(50)
	a.AddContent(100)
	a.Reset()
	if got := a.PromptTokens(); got != 0 {
		t.Fatalf("after Reset PromptTokens = %d, want 0", got)
	}
	if got := a.CompletionTokens(); got != 0 {
		t.Fatalf("after Reset CompletionTokens = %d, want 0", got)
	}
	if _, _, known, _ := a.Snapshot(); known {
		t.Fatal("after Reset snapshot must be unknown")
	}
}

// TestEstimatePromptTokens_CharsOverFour pins the shared chars/4 heuristic.
func TestEstimatePromptTokens_CharsOverFour(t *testing.T) {
	if got := EstimatePromptTokens(0); got != 0 {
		t.Fatalf("EstimatePromptTokens(0) = %d, want 0", got)
	}
	if got := EstimatePromptTokens(3); got != 1 {
		t.Fatalf("EstimatePromptTokens(3) = %d, want 1 (minimum)", got)
	}
	if got := EstimatePromptTokens(400); got != 100 {
		t.Fatalf("EstimatePromptTokens(400) = %d, want 100", got)
	}
	if got := EstimateCompletionTokens(40); got != 10 {
		t.Fatalf("EstimateCompletionTokens(40) = %d, want 10", got)
	}
}
