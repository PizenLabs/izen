package config

import (
	"testing"
)

// TestActiveBindingPersistsAtomically pins the round-trip invariant: saving
// openrouter + selected model and reloading must return the identical pair.
// Provider and model are persisted as one logical binding, never as
// independent fields that can recombine into a mixed state.
func TestActiveBindingPersistsAtomically(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := Default()
	cfg.Bindings.Active = ActiveBindingConfig{
		Provider: "openrouter",
		Model:    "anthropic/claude-3.5-sonnet",
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Bindings.Active.Provider != "openrouter" ||
		reloaded.Bindings.Active.Model != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("reloaded binding = %+v, want openrouter + anthropic/claude-3.5-sonnet",
			reloaded.Bindings.Active)
	}
	if prov := reloaded.ActiveProviderName(); prov != "openrouter" {
		t.Fatalf("ActiveProviderName() after reload = %q, want openrouter", prov)
	}
	if mdl := reloaded.ActiveModelName(); mdl != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("ActiveModelName() after reload = %q, want anthropic/claude-3.5-sonnet", mdl)
	}
}
