package authority

import (
	"errors"
	"testing"
)

func TestRoleForIntent(t *testing.T) {
	tests := []struct {
		intent string
		want   IntentRole
	}{
		{"plan", RoleThinking},
		{"investigate", RoleThinking},
		{"build", RoleCoder},
		{"ask", RoleFast},
		{"review", RoleFast},
		{"commit", RoleFast},
		{"conversation", RoleFast},
		{"unknown", RoleFast},
	}
	for _, tc := range tests {
		t.Run(tc.intent, func(t *testing.T) {
			got := RoleForIntent(tc.intent)
			if got != tc.want {
				t.Errorf("RoleForIntent(%q) = %v, want %v", tc.intent, got, tc.want)
			}
		})
	}
}

func TestResolveModel_Rule1_PolicyOverride(t *testing.T) {
	policy := ModelPolicy{
		Thinking: &ModelBinding{
			ProviderID: "anthropic",
			ModelID:    "claude-sonnet-4-20250514",
		},
	}
	runtimeState := ModelState{
		ActiveProvider: "ollama",
		ActiveModel:    "qwen2.5-coder:7b",
	}

	binding, err := ResolveModel("plan", runtimeState, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.ProviderID != "anthropic" || binding.ModelID != "claude-sonnet-4-20250514" {
		t.Errorf("expected policy override binding, got %+v", binding)
	}
}

func TestResolveModel_Rule2_DefaultActive(t *testing.T) {
	policy := ModelPolicy{}
	runtimeState := ModelState{
		ActiveProvider: "openrouter",
		ActiveModel:    "anthropic/claude-3.5-sonnet",
	}

	binding, err := ResolveModel("ask", runtimeState, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.ProviderID != "openrouter" || binding.ModelID != "anthropic/claude-3.5-sonnet" {
		t.Errorf("expected runtime active binding, got %+v", binding)
	}
}

func TestResolveModel_Rule3_TupleIntegrity(t *testing.T) {
	// Even when policy defines Thinking and runtime has a different provider,
	// the result must be the complete policy tuple, never a mix.
	policy := ModelPolicy{
		Thinking: &ModelBinding{
			ProviderID: "anthropic",
			ModelID:    "claude-sonnet-4-20250514",
		},
	}
	runtimeState := ModelState{
		ActiveProvider: "ollama",
		ActiveModel:    "llama3.2",
	}

	binding, err := ResolveModel("investigate", runtimeState, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binding.ProviderID != "anthropic" || binding.ModelID != "claude-sonnet-4-20250514" {
		t.Errorf("tuple integrity violated: got provider=%q model=%q", binding.ProviderID, binding.ModelID)
	}
}

func TestResolveModel_Rule4_MissingBinding(t *testing.T) {
	policy := ModelPolicy{}
	runtimeState := ModelState{
		ActiveProvider: "ollama",
		ActiveModel:    "",
	}

	_, err := ResolveModel("build", runtimeState, policy)
	if !errors.Is(err, ErrUnassignedModel) {
		t.Errorf("expected ErrUnassignedModel, got %v", err)
	}
}

func TestResolveModel_Rule5_Mismatch(t *testing.T) {
	policy := ModelPolicy{}
	runtimeState := ModelState{
		ActiveProvider: "ollama",
		ActiveModel:    "anthropic/claude-3.5-sonnet",
	}

	_, err := ResolveModel("build", runtimeState, policy)
	if !errors.Is(err, ErrProviderModelMismatch) {
		t.Errorf("expected ErrProviderModelMismatch, got %v", err)
	}
}

func TestResolveModel_DirectResponseSamePath(t *testing.T) {
	// conversation (direct_response) should resolve through the exact same
	// ResolveModel path as execution, using RoleFast.
	policy := ModelPolicy{
		Fast: &ModelBinding{
			ProviderID: "openrouter",
			ModelID:    "anthropic/claude-3.5-sonnet",
		},
	}
	runtimeState := ModelState{
		ActiveProvider: "ollama",
		ActiveModel:    "qwen2.5-coder:7b",
	}

	binding, err := ResolveModel("conversation", runtimeState, policy)
	if err != nil {
		t.Fatalf("unexpected error for direct_response: %v", err)
	}
	if binding.ProviderID != "openrouter" {
		t.Errorf("expected policy override for conversation, got provider=%q", binding.ProviderID)
	}
}

func TestResolveModel_EmptyPolicyBinding(t *testing.T) {
	policy := ModelPolicy{
		Fast: &ModelBinding{
			ProviderID: "openrouter",
			ModelID:    "",
		},
	}
	runtimeState := ModelState{
		ActiveProvider: "ollama",
		ActiveModel:    "qwen2.5-coder:7b",
	}

	_, err := ResolveModel("ask", runtimeState, policy)
	if err == nil {
		t.Fatal("expected error for empty policy model, got nil")
	}
	if !errors.Is(err, ErrUnassignedModel) {
		t.Errorf("expected ErrUnassignedModel, got %v", err)
	}
}
