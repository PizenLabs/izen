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
