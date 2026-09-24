package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// TestSubmitPrompt_AtomicBindingSurvivesStaleSessionConfig reproduces the
// /models split-brain: after switching ollama -> openrouter the authority
// holds openrouter + thinkingmachines/inkling-small:free while the live
// session config still carries the pre-switch ollama binding. Admission must
// derive provider AND model from the same authority binding, so the stale
// config provider must never produce a provider/model mismatch.
func TestSubmitPrompt_AtomicBindingSurvivesStaleSessionConfig(t *testing.T) {
	deps, _ := newDeps()
	deps.Authority = runtime.NewRuntimeAuthority()
	deps.Authority.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("thinkingmachines/inkling-small:free"),
	})
	cfg := config.Default()
	cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "ollama",
		Model:    "qwen2.5-coder:7b",
	}
	deps.Config = cfg

	h := New(deps).Submit()
	if err := h.Handle(context.Background(), runtime.SubmitPromptCmd{Prompt: "hi"}); err != nil {
		t.Fatalf("Submit with atomic openrouter binding must not fail, got: %v", err)
	}
}

// TestSubmitPrompt_MixedBindingRejected pins that genuinely mixed bindings
// still fail closed at admission: neither ollama+OpenRouter-model nor
// openrouter+Ollama-model may execute.
func TestSubmitPrompt_MixedBindingRejected(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"ollamaProviderOpenRouterModel", "ollama", "thinkingmachines/inkling-small:free"},
		{"openRouterProviderOllamaModel", "openrouter", "qwen2.5-coder:7b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := newDeps()
			deps.Authority = runtime.NewRuntimeAuthority()
			deps.Authority.Activate(authority.ModelBinding{
				ProviderID: authority.ProviderID(tc.provider),
				ModelID:    authority.ModelID(tc.model),
			})
			h := New(deps).Submit()
			err := h.Handle(context.Background(), runtime.SubmitPromptCmd{Prompt: "hi"})
			if !errors.Is(err, execution.ErrProviderModelMismatch) {
				t.Fatalf("mixed binding %s + %s: err = %v, want ErrProviderModelMismatch", tc.provider, tc.model, err)
			}
		})
	}
}

// TestSubmitPrompt_ProviderlessBindingFallsBackToConfig pins the legacy seam:
// a binding that carries no provider resolves the provider from config.
func TestSubmitPrompt_ProviderlessBindingFallsBackToConfig(t *testing.T) {
	deps, _ := newDeps()
	deps.Authority = runtime.NewRuntimeAuthority()
	deps.Authority.Activate(authority.ModelBinding{
		ModelID: authority.ModelID("anthropic/claude-3.5-sonnet"),
	})
	cfg := config.Default()
	cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "openrouter",
		Model:    "anthropic/claude-3.5-sonnet",
	}
	deps.Config = cfg

	h := New(deps).Submit()
	if err := h.Handle(context.Background(), runtime.SubmitPromptCmd{Prompt: "hi"}); err != nil {
		t.Fatalf("providerless binding with config fallback must not fail, got: %v", err)
	}
}
