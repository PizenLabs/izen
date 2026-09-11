package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// Cold-boot regression: a key explicitly saved in ~/.izen/config.yml (injected
// as the constructed client key) must win over a stale shell environment
// variable from ~/.zshrc. Env is only a fallback when no configured key exists.
func TestResolveAPIKeyConfigWinsOverEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-key-old")
	t.Setenv("OPENCODE_API_KEY", "env-key-old")
	t.Setenv("9ROUTER_API_KEY", "env-key-old")

	if got := NewOpenRouterProvider("config-key-new", "m", "https://example.com").resolveAPIKey(); got != "config-key-new" {
		t.Errorf("openrouter resolveAPIKey = %q, want config-key-new (config must override env)", got)
	}
	if got := NewOpenCodeProvider("config-key-new", "m", "https://example.com").resolveAPIKey(); got != "config-key-new" {
		t.Errorf("opencode resolveAPIKey = %q, want config-key-new (config must override env)", got)
	}
	if got := NewNineRouterProvider("config-key-new", "m", "https://example.com").resolveAPIKey(); got != "config-key-new" {
		t.Errorf("9router resolveAPIKey = %q, want config-key-new (config must override env)", got)
	}
}

// Env remains a fallback when the provider was constructed without a
// configured key (unconfigured provider, dotenv-only setup).
func TestResolveAPIKeyEnvFallbackWhenUnconfigured(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-key-old")
	t.Setenv("OPENCODE_API_KEY", "env-key-old")
	t.Setenv("9ROUTER_API_KEY", "env-key-old")

	if got := NewOpenRouterProvider("", "m", "https://example.com").resolveAPIKey(); got != "env-key-old" {
		t.Errorf("openrouter resolveAPIKey = %q, want env fallback env-key-old", got)
	}
	if got := NewOpenCodeProvider("", "m", "https://example.com").resolveAPIKey(); got != "env-key-old" {
		t.Errorf("opencode resolveAPIKey = %q, want env fallback env-key-old", got)
	}
	if got := NewNineRouterProvider("", "m", "https://example.com").resolveAPIKey(); got != "env-key-old" {
		t.Errorf("9router resolveAPIKey = %q, want env fallback env-key-old", got)
	}
}

// End-to-end: the Authorization header on the wire must carry the configured
// key even when a stale env var is present.
func TestColdBootOpenRouterWireUsesConfigKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-key-old")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenRouterProvider("config-key-new", "test/model", srv.URL)
	resp, err := p.Execute(context.Background(), ai.Request{
		Model:    "test/model",
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("Execute response = %+v, want content ok", resp)
	}
	if gotAuth != "Bearer config-key-new" {
		t.Errorf("Authorization header = %q, want %q (stale env key must not leak)", gotAuth, "Bearer config-key-new")
	}
}
