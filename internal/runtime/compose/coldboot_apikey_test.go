package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
)

// Cold-boot regression: when ~/.izen/config.yml carries an explicit provider
// key and the shell environment carries a stale one (e.g. from ~/.zshrc), the
// composition-root bootstrap (registerProviders, the same path Wire uses) must
// instantiate the provider with the config key — never the env key.
func TestColdBootRegisterProvidersUsesConfigKeyOverEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-key-old")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	// Mock ~/.izen/config.yml content: explicit key saved on disk.
	cfg := &config.Config{
		AI: config.AIConfig{
			Providers: map[string]config.AIProviderConfig{
				"openrouter": {
					APIKey:       "config-key-new",
					BaseURL:      srv.URL,
					DefaultModel: "test/model",
				},
			},
		},
	}

	// Factory input contract: resolution itself must prefer the config key.
	if got := cfg.ResolveAPIKey("openrouter"); got != "config-key-new" {
		t.Fatalf("cfg.ResolveAPIKey = %q, want config-key-new", got)
	}

	// Cold-boot bootstrap sequence (same call Wire makes).
	mgr := ai.NewManager()
	registerProviders(cfg, mgr)

	prov, ok := mgr.Get("openrouter")
	if !ok {
		t.Fatal("openrouter not registered after cold-boot bootstrap")
	}
	resp, err := prov.Execute(context.Background(), ai.Request{
		Model:    "test/model",
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp == nil {
		t.Fatal("Execute returned nil response")
	}
	if gotAuth != "Bearer config-key-new" {
		t.Errorf("Authorization header = %q, want %q (stale env key must not leak)", gotAuth, "Bearer config-key-new")
	}
}
