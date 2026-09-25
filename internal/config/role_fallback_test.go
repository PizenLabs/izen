package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSplitProviderModel pins the slug parser used by the role fallback chain:
// exactly one slash means provider/model; a bare OpenRouter-style ID
// ("thinkingmachines/inkling:free") is a model, not a provider slug.
func TestSplitProviderModel(t *testing.T) {
	cases := []struct {
		in       string
		provider string
		model    string
	}{
		{"openrouter/thinkingmachines/inkling-small:free", "openrouter", "thinkingmachines/inkling-small:free"},
		{"openrouter/anthropic/claude-3.5-sonnet", "openrouter", "anthropic/claude-3.5-sonnet"},
		{"OpenRouter/anthropic/claude-3.5-sonnet", "openrouter", "anthropic/claude-3.5-sonnet"},
		{"anthropic/claude-3.5-sonnet", "anthropic", "claude-3.5-sonnet"},
		{"thinkingmachines/inkling-small:free", "", "thinkingmachines/inkling-small:free"},
		{"qwen2.5-coder:7b", "", "qwen2.5-coder:7b"},
		{"  openrouter/anthropic/claude-3.5-sonnet  ", "openrouter", "anthropic/claude-3.5-sonnet"},
		{"", "", ""},
		{"/leading", "", "/leading"},
		{"notaprovider/vendor/model", "", "notaprovider/vendor/model"},
	}
	for _, tc := range cases {
		p, m := SplitProviderModel(tc.in)
		if p != tc.provider || m != tc.model {
			t.Errorf("SplitProviderModel(%q) = (%q, %q), want (%q, %q)", tc.in, p, m, tc.provider, tc.model)
		}
	}
}

// TestRoleChainForMatchesDeclaredPrimary: the chain fires when the active model
// IS the role's declared primary.
func TestRoleChainForMatchesDeclaredPrimary(t *testing.T) {
	cfg := &Config{Roles: map[string]RoleFallbackConfig{
		"plan": {
			Model:    "openrouter/thinkingmachines/inkling-small:free",
			Fallback: "openrouter/anthropic/claude-3.5-sonnet",
		},
	}}
	chain, ok := cfg.RoleChainFor("plan", "thinkingmachines/inkling-small:free", "openrouter")
	if !ok {
		t.Fatal("RoleChainFor must resolve the configured plan chain for its declared primary")
	}
	if chain.Model != "anthropic/claude-3.5-sonnet" || chain.Provider != "openrouter" {
		t.Fatalf("chain = %+v, want openrouter/anthropic/claude-3.5-sonnet", chain)
	}
	if chain.Label != "openrouter/anthropic/claude-3.5-sonnet" {
		t.Fatalf("chain label = %q", chain.Label)
	}
	if chain.Primary != "thinkingmachines/inkling-small:free" || chain.Role != "plan" {
		t.Fatalf("chain provenance = %+v, want primary+role recorded", chain)
	}
}

// TestRoleChainForIgnoresForeignPrimary: a role whose declared primary is a
// DIFFERENT model never claims the turn — the user's explicit selection wins.
func TestRoleChainForIgnoresForeignPrimary(t *testing.T) {
	cfg := &Config{Roles: map[string]RoleFallbackConfig{
		"plan": {Model: "openrouter/thinkingmachines/inkling-small:free", Fallback: "openrouter/anthropic/claude-3.5-sonnet"},
	}}
	if _, ok := cfg.RoleChainFor("plan", "openai/gpt-4o", "openrouter"); ok {
		t.Fatal("a chain must not fire for a model the role did not declare as its primary")
	}
}

// TestRoleChainForFallbackOnlyRole: an entry that declares only a fallback
// applies to every turn of that role regardless of the active model.
func TestRoleChainForFallbackOnlyRole(t *testing.T) {
	cfg := &Config{Roles: map[string]RoleFallbackConfig{
		"default": {Fallback: "openrouter/anthropic/claude-3.5-sonnet"},
	}}
	chain, ok := cfg.RoleChainFor("default", "openai/gpt-4o", "openai")
	if !ok {
		t.Fatal("a fallback-only role entry must apply to its own role")
	}
	if chain.Provider != "openrouter" || chain.Model != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("chain = %+v, want the configured fallback with its own provider", chain)
	}
}

// TestRoleChainForFindsChainByFailingModel: the chain belongs to the model that
// failed, not only to the surface that invoked it (mode default, plan-declared
// primary still resolves).
func TestRoleChainForFindsChainByFailingModel(t *testing.T) {
	cfg := &Config{Roles: map[string]RoleFallbackConfig{
		"plan": {Model: "openrouter/thinkingmachines/inkling-small:free", Fallback: "openrouter/anthropic/claude-3.5-sonnet"},
	}}
	chain, ok := cfg.RoleChainFor("default", "thinkingmachines/inkling-small:free", "openrouter")
	if !ok {
		t.Fatal("the chain declared for the failing model must resolve from any role")
	}
	if chain.Role != "plan" {
		t.Fatalf("chain role = %q, want plan", chain.Role)
	}
}

// TestRoleChainForBareFallbackUsesActiveProvider: a bare fallback model ID
// inherits the provider of the model that failed.
func TestRoleChainForBareFallbackUsesActiveProvider(t *testing.T) {
	cfg := &Config{Roles: map[string]RoleFallbackConfig{
		"default": {Model: "qwen2.5-coder:7b", Fallback: "llama-3.3-70b-versatile"},
	}}
	chain, ok := cfg.RoleChainFor("default", "qwen2.5-coder:7b", "ollama")
	if !ok || chain.Provider != "ollama" || chain.Model != "llama-3.3-70b-versatile" {
		t.Fatalf("chain = %+v (ok=%v), want the ollama fallback", chain, ok)
	}
}

// TestRoleChainForRefusesSelfFallback: a fallback identical to the failed
// primary is a no-op loop and is refused rather than retried forever.
func TestRoleChainForRefusesSelfFallback(t *testing.T) {
	cfg := &Config{Roles: map[string]RoleFallbackConfig{
		"default": {Model: "openai/gpt-4o", Fallback: "openai/gpt-4o"},
	}}
	if _, ok := cfg.RoleChainFor("default", "gpt-4o", "openai"); ok {
		t.Fatal("a self-referential fallback must be refused")
	}
}

// TestRoleChainForNoConfig / empty entries never fabricate a chain.
func TestRoleChainForNoConfig(t *testing.T) {
	if _, ok := (&Config{}).RoleChainFor("plan", "gpt-4o", "openai"); ok {
		t.Fatal("no roles configured must mean no chain")
	}
	cfg := &Config{Roles: map[string]RoleFallbackConfig{"plan": {Model: "openai/gpt-4o"}}}
	if _, ok := cfg.RoleChainFor("plan", "gpt-4o", "openai"); ok {
		t.Fatal("a role without a fallback must mean no chain")
	}
}

// TestRoleFallbackConfigYAML pins the documented config schema, so a
// hand-written ~/.izen/config.yml with the spec's role blocks loads exactly.
func TestRoleFallbackConfigYAML(t *testing.T) {
	raw := `
roles:
  plan:
    model: "openrouter/thinkingmachines/inkling-small:free"
    fallback: "openrouter/anthropic/claude-3.5-sonnet"
  default:
    fallback: "openrouter/anthropic/claude-3.5-sonnet"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plan, ok := cfg.Roles["plan"]
	if !ok {
		t.Fatalf("roles.plan missing: %+v", cfg.Roles)
	}
	if plan.Model != "openrouter/thinkingmachines/inkling-small:free" || plan.Fallback != "openrouter/anthropic/claude-3.5-sonnet" {
		t.Fatalf("roles.plan = %+v", plan)
	}
	if cfg.Roles["default"].Fallback != "openrouter/anthropic/claude-3.5-sonnet" {
		t.Fatalf("roles.default = %+v", cfg.Roles["default"])
	}
	// The chain resolves for the plan primary.
	chain, ok := cfg.RoleChainFor("plan", "thinkingmachines/inkling-small:free", "openrouter")
	if !ok || chain.Model != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("chain = %+v (ok=%v)", chain, ok)
	}
}
