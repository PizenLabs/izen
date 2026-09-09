package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

func TestEnvProvidersDeepseekAndGoogleAlias(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "dsk-test")
	t.Setenv("GOOGLE_API_KEY", "google-test")
	t.Setenv("GEMINI_API_KEY", "")

	got := EnvProviders()
	byName := map[string]detector.ProviderConfig{}
	for _, p := range got {
		byName[p.Name] = p
	}
	ds, ok := byName["deepseek"]
	if !ok {
		t.Fatalf("deepseek missing from %v", got)
	}
	if ds.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("deepseek BaseURL = %q", ds.BaseURL)
	}
	gm, ok := byName["gemini"]
	if !ok {
		t.Fatalf("gemini (GOOGLE_API_KEY alias) missing from %v", got)
	}
	if gm.APIKey != "google-test" {
		t.Errorf("gemini APIKey = %q, want google-test", gm.APIKey)
	}
}

func TestParseOllamaTags(t *testing.T) {
	got := ParseOllamaTags([]byte(`{"models":[{"name":"llama3:8b"},{"name":"mistral:7b"},{"name":""}]}`))
	if len(got) != 2 || got[0].ID != "llama3:8b" || got[1].ID != "mistral:7b" {
		t.Errorf("parsed = %+v, want llama3 + mistral", got)
	}
	if got := ParseOllamaTags([]byte(`not-json`)); len(got) != 0 {
		t.Errorf("malformed = %+v, want empty", got)
	}
}

func TestDiscoverProvidersDedupsOpenRouter(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "or-test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := DiscoverProviders(ctx)
	count := 0
	for _, p := range got {
		if p.Name == "openrouter" {
			count++
			if p.BaseURL != "https://openrouter.ai/api/v1" {
				t.Errorf("openrouter BaseURL = %q", p.BaseURL)
			}
		}
	}
	if count != 1 {
		t.Errorf("openrouter entries = %d, want 1 (detector + scanner dedup)", count)
	}
}
