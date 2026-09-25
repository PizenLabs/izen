package registry

import (
	"testing"

	"github.com/PizenLabs/izen/internal/config"
)

// TestResolveRoleModel_NoCatalogFilter pins the adaptive-runtime catalog
// access contract: role resolution sees every discovered model, including
// agentic-harness models, because none are locally rejected anymore.
func TestResolveRoleModel_NoCatalogFilter(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed([]ModelDescriptor{
		{ID: "openai/gpt-4o", Provider: "openrouter", Name: "GPT-4o", Capabilities: []ModelCapability{CapTools}},
		{ID: "thinkingmachines/inkling:free", Provider: "openrouter", Name: "Inkling (free)"},
		{ID: "thinkingmachines/inkling-small:free", Provider: "openrouter", Name: "Inkling Small (free)"},
	})

	for _, agentic := range []string{"thinkingmachines/inkling:free", "thinkingmachines/inkling-small:free"} {
		cfg := &config.CascadeConfig{Roles: map[string]string{"plan": agentic}}
		got, err := ResolveRoleModel("plan", cfg, r)
		if err != nil {
			t.Fatalf("ResolveRoleModel(plan) for %q: %v", agentic, err)
		}
		if got.ID != agentic {
			t.Fatalf("resolved %q, want %q", got.ID, agentic)
		}
	}

	cfg := &config.CascadeConfig{Roles: map[string]string{"plan": "openai/gpt-4o"}}
	got, err := ResolveRoleModel("plan", cfg, r)
	if err != nil {
		t.Fatalf("ResolveRoleModel(plan) eligible binding: %v", err)
	}
	if got.ID != "openai/gpt-4o" {
		t.Fatalf("resolved %q, want openai/gpt-4o", got.ID)
	}
}
