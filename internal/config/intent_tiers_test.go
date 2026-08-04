package config

import "testing"

// TestResolveIntentTierModels verifies the intent-based routing tiers
// (reasoning / execution / informational) resolve through ResolveTierModel.
func TestResolveIntentTierModels(t *testing.T) {
	cfg := Default()

	for _, tier := range []string{"reasoning", "execution", "informational"} {
		got := cfg.ResolveTierModel(tier)
		if got == "" {
			t.Fatalf("ResolveTierModel(%q) returned empty", tier)
		}
	}

	// A configured tier overrides the active model.
	cfg.Models.Tiers["reasoning"] = IntentTierConfig{Model: "reasoning-model"}
	if got := cfg.ResolveTierModel("reasoning"); got != "reasoning-model" {
		t.Errorf("ResolveTierModel(reasoning) = %q, want reasoning-model", got)
	}

	// Active override wins over the tier model.
	cfg.Models.Tiers["execution"] = IntentTierConfig{Model: "exec-model", ActiveOverride: "override-model"}
	if got := cfg.ResolveTierModel("execution"); got != "override-model" {
		t.Errorf("ResolveTierModel(execution) = %q, want override-model", got)
	}

	// Unknown tiers fall back to the active model.
	cfg.Models.SessionModel = ""
	if got := cfg.ResolveTierModel("bogus"); got != cfg.ActiveModelName() {
		t.Errorf("ResolveTierModel(bogus) = %q, want active %q", got, cfg.ActiveModelName())
	}
}

// TestResolveTierModel_ProviderScoped verifies that intent-tier resolution is
// scoped to the active provider: a tier pinned to a different provider (e.g.
// an Ollama local model pinned while OpenRouter is active) must be skipped so
// the request never carries an invalid model ID.
func TestResolveTierModel_ProviderScoped(t *testing.T) {
	cfg := Default()

	// Default tiers pin the ollama local model to every intent tier.
	if got := cfg.ResolveTierModel("informational"); got != "qwen2.5-coder:7b" {
		t.Fatalf("ResolveTierModel(informational) with ollama active = %q, want %q", got, "qwen2.5-coder:7b")
	}

	// Switching the active provider to OpenRouter must skip the ollama-pinned
	// tier and fall back to the OpenRouter default model instead.
	cfg.AI.DefaultProvider = "openrouter"
	if got := cfg.ResolveTierModel("informational"); got != "anthropic/claude-3.5-sonnet" {
		t.Errorf("ResolveTierModel(informational) with openrouter active = %q, want %q", got, "anthropic/claude-3.5-sonnet")
	}

	// A tier model whose provider matches the active provider is honored.
	cfg.Models.Tiers["execution"] = IntentTierConfig{Provider: "openrouter", Model: "openrouter/fast-model"}
	if got := cfg.ResolveTierModel("execution"); got != "openrouter/fast-model" {
		t.Errorf("ResolveTierModel(execution) = %q, want openrouter/fast-model", got)
	}

	// A tier model with an empty provider is treated as active-provider owned.
	cfg.Models.Tiers["reasoning"] = IntentTierConfig{Model: "openrouter/heavy-model"}
	if got := cfg.ResolveTierModel("reasoning"); got != "openrouter/heavy-model" {
		t.Errorf("ResolveTierModel(reasoning) = %q, want openrouter/heavy-model", got)
	}

	// An explicit active_override still wins regardless of the tier provider.
	cfg.Models.Tiers["informational"] = IntentTierConfig{
		Provider:       "ollama",
		Model:          "qwen2.5-coder:7b",
		ActiveOverride: "openrouter/custom-mini",
	}
	if got := cfg.ResolveTierModel("informational"); got != "openrouter/custom-mini" {
		t.Errorf("ResolveTierModel(informational) with override = %q, want openrouter/custom-mini", got)
	}
}

// TestResolveTierProvider verifies the provider owning a resolved tier model
// is derived from the active provider, never from a stale tier pin.
func TestResolveTierProvider(t *testing.T) {
	cfg := Default()
	if got := cfg.ResolveTierProvider("informational"); got != "ollama" {
		t.Fatalf("ResolveTierProvider(informational) = %q, want ollama", got)
	}

	cfg.AI.DefaultProvider = "openrouter"
	// Default tier pin (provider ollama) does not match the active provider,
	// so the active provider owns the route.
	if got := cfg.ResolveTierProvider("informational"); got != "openrouter" {
		t.Errorf("ResolveTierProvider(informational) = %q, want openrouter", got)
	}
	// A tier pinned to the active provider is honored.
	if got := cfg.ResolveTierProvider("reasoning"); got != "openrouter" {
		t.Errorf("ResolveTierProvider(reasoning) = %q, want openrouter", got)
	}

	cfg.AI.DefaultProvider = "ollama"
	cfg.Models.Tiers["reasoning"] = IntentTierConfig{Provider: "openrouter", Model: "openrouter/heavy"}
	if got := cfg.ResolveTierProvider("reasoning"); got != "ollama" {
		t.Errorf("ResolveTierProvider(reasoning) with mismatched pin = %q, want ollama", got)
	}
}
