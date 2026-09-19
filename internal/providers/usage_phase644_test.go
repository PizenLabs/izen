package providers

import (
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestStreamUsageTracker_OptimisticPrompt pins the Optimistic Prompt Token
// Invariant (Phase 6.4.4): a prompt estimate registered at dispatch is
// visible immediately, even with zero output bytes, so early cancellation
// never loses prompt cost.
func TestStreamUsageTracker_OptimisticPrompt(t *testing.T) {
	var tr streamUsageTracker
	tr.recordPromptEstimate(120)
	u := tr.Usage()
	if !u.Known {
		t.Fatal("Known = false, want true for optimistic prompt estimate")
	}
	if !u.Estimated {
		t.Fatal("Estimated = false, want true before authoritative usage")
	}
	if u.PromptTokens != 120 {
		t.Errorf("PromptTokens = %d, want 120", u.PromptTokens)
	}
	if u.CompletionTokens != 0 {
		t.Errorf("CompletionTokens = %d, want 0 (no output yet)", u.CompletionTokens)
	}
	if !tr.Estimated() {
		t.Error("Estimated() = false, want true")
	}
}

// TestStreamUsageTracker_OptimisticPlusPartial pins the Real-Time Live
// Streaming Usage Invariant at the tracker level: prompt estimate +
// per-chunk completion increments snapshot without any terminal usage frame.
func TestStreamUsageTracker_OptimisticPlusPartial(t *testing.T) {
	var tr streamUsageTracker
	tr.recordPromptEstimate(80)
	tr.recordOutput(40) // 10 tokens
	tr.recordOutput(40) // 20 tokens
	u := tr.Usage()
	if !u.Known || !u.Estimated {
		t.Fatalf("Known=%v Estimated=%v, want true/true", u.Known, u.Estimated)
	}
	if u.PromptTokens != 80 {
		t.Errorf("PromptTokens = %d, want 80", u.PromptTokens)
	}
	if u.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", u.CompletionTokens)
	}
}

// TestStreamUsageTracker_AuthoritativeReplacesOptimistic pins that a terminal
// usage chunk replaces the optimistic estimate verbatim (no double-count).
func TestStreamUsageTracker_AuthoritativeReplacesOptimistic(t *testing.T) {
	var tr streamUsageTracker
	tr.recordPromptEstimate(80)
	tr.recordOutput(400)
	tr.recordUsageFull(ai.ProviderUsage{PromptTokens: 64, CompletionTokens: 32, Known: true})
	u := tr.Usage()
	if u.PromptTokens != 64 || u.CompletionTokens != 32 {
		t.Errorf("Usage() = (%d,%d), want authoritative (64,32)", u.PromptTokens, u.CompletionTokens)
	}
	if u.Estimated {
		t.Error("Estimated = true after authoritative chunk")
	}
	if tr.PromptEstimate() != 0 {
		t.Errorf("PromptEstimate = %d, want 0 (discarded)", tr.PromptEstimate())
	}
}

// TestEstimatePromptTokensForRequest pins the shared request-size heuristic.
func TestEstimatePromptTokensForRequest(t *testing.T) {
	got := EstimatePromptTokensForRequest("system prompt", []ai.Message{{Role: "user", Content: "hello world"}})
	// len("system prompt")=13 + len("hello world")=11 → 24 chars → 6 tokens.
	if got != 6 {
		t.Errorf("EstimatePromptTokensForRequest = %d, want 6", got)
	}
	if got := EstimatePromptTokensForRequest("", nil); got != 0 {
		t.Errorf("empty request = %d, want 0", got)
	}
}
