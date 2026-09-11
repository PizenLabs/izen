package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
)

// buildProviderInstance must yield a live client for every known provider and
// never fabricate one for unknown names.
func TestBuildProviderInstance(t *testing.T) {
	for _, name := range []string{"ollama", "openrouter", "openai", "anthropic", "gemini", "groq", "opencode", "9router"} {
		if p := buildProviderInstance(name, "sk-test", "https://example.com/v1", "m"); p == nil {
			t.Errorf("buildProviderInstance(%q) = nil, want live client", name)
		} else if p.Name() != name {
			t.Errorf("buildProviderInstance(%q).Name() = %q", name, p.Name())
		}
	}
	if p := buildProviderInstance("not-a-provider", "sk-test", "", ""); p != nil {
		t.Errorf("buildProviderInstance(unknown) = %v, want nil", p)
	}
}

// An explicit config.yml key counts as configured even when the shell env is
// unset (config file takes precedence over environment variables).
func TestIsProviderAvailableConfigFirst(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	cfg := config.Default()
	prov := cfg.AI.Providers["openrouter"]
	prov.APIKey = "sk-config-explicit"
	cfg.AI.Providers["openrouter"] = prov

	m := &model{cfg: cfg}
	if !m.isProviderAvailable("openrouter", "OPENROUTER_API_KEY") {
		t.Errorf("isProviderAvailable = false, want true with explicit config key and empty env")
	}

	empty := config.Default()
	empty.AI.Providers["openrouter"] = config.AIProviderConfig{BaseURL: "https://openrouter.ai/api/v1"}
	m2 := &model{cfg: empty}
	if m2.isProviderAvailable("openrouter", "OPENROUTER_API_KEY") {
		t.Errorf("isProviderAvailable = true, want false with empty config and empty env")
	}
}

// Hot-reload must re-register the rebuilt client on the manager and, when
// the provider is session-active, rebind m.provider so the next prompt uses
// the newly saved key.
func TestHotReloadProviderKey(t *testing.T) {
	cfg := config.Default()
	m := &model{cfg: cfg, mgr: ai.NewManager()}

	// Seed the active provider with a stale client, then reload its key.
	m.provider = buildProviderInstance("openrouter", "sk-stale", "https://openrouter.ai/api/v1", "m")
	m.mgr.Register("openrouter", m.provider)

	m.hotReloadProviderKey("openrouter", "sk-live-123")

	got, ok := m.mgr.Get("openrouter")
	if !ok || got == nil {
		t.Fatalf("manager missing re-registered openrouter client")
	}
	if m.provider == nil || m.provider.Name() != "openrouter" {
		t.Fatalf("active provider not bound to reloaded client")
	}

	// A second save must replace the client (new bearer token), not reuse it.
	first := m.provider
	m.hotReloadProviderKey("openrouter", "sk-live-456")
	if m.provider == first {
		t.Errorf("hot reload kept stale client instance; want rebuilt client with new key")
	}
	if _, ok := m.mgr.Get("openrouter"); !ok {
		t.Errorf("manager lost openrouter registration after second reload")
	}

	// Saving a key for an inactive provider must not hijack the active one.
	m.hotReloadProviderKey("groq", "gsk-test")
	if m.provider.Name() != "openrouter" {
		t.Errorf("active provider = %q, want openrouter (inactive save must not switch)", m.provider.Name())
	}
	if _, ok := m.mgr.Get("groq"); !ok {
		t.Errorf("manager missing freshly registered groq client")
	}
}
