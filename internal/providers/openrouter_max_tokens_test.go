package providers

import (
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestOpenRouterBuildRequestDefaultMaxTokens pins the raised default output
// limit: a request without an explicit budget (e.g. /ask code generation)
// must carry max_tokens=4096 — never a small provider default — so long
// answers complete without finish_reason "length" truncation.
func TestOpenRouterBuildRequestDefaultMaxTokens(t *testing.T) {
	p := NewOpenRouterProvider("test-key", "cohere/north-mini-code", "https://openrouter.example.com/api/v1")
	req := p.buildRequest(
		"cohere/north-mini-code",
		[]openrouterMessage{{Role: "user", Content: "how to integrate k8s into golang backend"}},
		ai.Request{},
		true,
	)
	if req.MaxTokens != 4096 {
		t.Errorf("default MaxTokens = %d, want 4096", req.MaxTokens)
	}
}

// TestOpenRouterBuildRequestPreservesExplicitBudget pins that tighter caller
// budgets (bounded-patch mutation, read-only plans) pass through verbatim
// below the cap.
func TestOpenRouterBuildRequestPreservesExplicitBudget(t *testing.T) {
	p := NewOpenRouterProvider("test-key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	for _, want := range []int{800, 1200, 2048, 4096, 8192} {
		req := p.buildRequest(
			"openai/gpt-4o",
			[]openrouterMessage{{Role: "user", Content: "hi"}},
			ai.Request{MaxTokens: want},
			true,
		)
		if req.MaxTokens != want {
			t.Errorf("explicit MaxTokens=%d rewritten to %d, want preserved", want, req.MaxTokens)
		}
	}
}

// TestOpenRouterBuildRequestClampsLargeBudget pins the hard ceiling: budgets
// above 8192 are clamped, never sent unconstrained.
func TestOpenRouterBuildRequestClampsLargeBudget(t *testing.T) {
	p := NewOpenRouterProvider("test-key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	req := p.buildRequest(
		"openai/gpt-4o",
		[]openrouterMessage{{Role: "user", Content: "hi"}},
		ai.Request{MaxTokens: 32000},
		true,
	)
	if req.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want clamped 8192", req.MaxTokens)
	}
}

// TestReasoningForDefaultsCoTCap pins the A.2 reasoning control: enabled
// reasoning without an explicit CoT cap or thinking budget defaults to a
// 1024-token reasoning cap so the hidden channel cannot consume the whole
// output budget — the maximum token budget goes to the actual response.
func TestReasoningForDefaultsCoTCap(t *testing.T) {
	r := reasoningFor(ai.Request{Reasoning: &ai.ReasoningConfig{Level: "medium"}})
	if r == nil {
		t.Fatal("reasoningFor should not return nil for enabled reasoning")
	}
	if r.Effort != "medium" {
		t.Errorf("reasoning.effort = %q, want medium", r.Effort)
	}
	if r.MaxTokens != 1024 {
		t.Errorf("reasoning.max_tokens = %d, want default 1024", r.MaxTokens)
	}
}

// TestReasoningForExplicitCapBeatsDefault pins that an explicit CoT cap or
// budget still wins over the 1024 default.
func TestReasoningForExplicitCapBeatsDefault(t *testing.T) {
	r := reasoningFor(ai.Request{Reasoning: &ai.ReasoningConfig{Level: "medium", CoTLimit: 512}})
	if r == nil || r.MaxTokens != 512 {
		t.Errorf("explicit CoTLimit lost to default, got %+v", r)
	}
	r = reasoningFor(ai.Request{Reasoning: &ai.ReasoningConfig{Level: "high", BudgetTokens: 8192}})
	if r == nil || r.MaxTokens != 8192 {
		t.Errorf("explicit BudgetTokens lost to default, got %+v", r)
	}
}
