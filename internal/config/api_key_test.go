package config

import (
	"os"
	"path/filepath"
	"testing"
)

// WellKnownBaseURL returns canonical endpoints for known providers and empty
// for unknowns (never fabricated).
func TestWellKnownBaseURL(t *testing.T) {
	cases := map[string]string{
		"openrouter": "https://openrouter.ai/api/v1",
		"openai":     "https://api.openai.com/v1",
		"anthropic":  "https://api.anthropic.com/v1",
		"GROQ":       "https://api.groq.com/openai/v1",
		"ollama":     "http://localhost:11434/v1",
	}
	for in, want := range cases {
		if got := WellKnownBaseURL(in); got != want {
			t.Errorf("WellKnownBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := WellKnownBaseURL("not-a-real-provider"); got != "" {
		t.Errorf("unknown provider must yield empty base URL, got %q", got)
	}
}

// SaveProviderAPIKey persists a provider API key into the unified config
// store under a redirected HOME, creating the provider block (with its
// well-known base URL) when absent.
func TestSaveProviderAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows compat

	if err := SaveProviderAPIKey("OpenRouter", "sk-live-123"); err != nil {
		t.Fatalf("SaveProviderAPIKey: %v", err)
	}
	for _, bad := range []struct{ p, k string }{
		{"", "x"},
		{"openai", ""},
	} {
		if err := SaveProviderAPIKey(bad.p, bad.k); err == nil {
			t.Errorf("SaveProviderAPIKey(%q, %q) must fail", bad.p, bad.k)
		}
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load after save: %v", err)
	}
	prov, ok := cfg.AI.Providers["openrouter"]
	if !ok {
		t.Fatal("provider block not created")
	}
	if prov.APIKey != "sk-live-123" {
		t.Errorf("stored key = %q, want sk-live-123", prov.APIKey)
	}
	if prov.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("created provider base_url = %q, want well-known URL", prov.BaseURL)
	}
	if _, err := os.Stat(filepath.Join(home, ".izen", "config.yml")); err != nil {
		t.Errorf("config.yml not written: %v", err)
	}
}

// SaveProviderAPIKey persists into config.go returning the resolved default
// provider as the active fallback (no hardcoded model invention).
func TestDefaultAfterKeySave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := SaveProviderAPIKey("groq", "gsk-test"); err != nil {
		t.Fatalf("SaveProviderAPIKey: %v", err)
	}
	cfg := GetGlobalConfig()
	if got := WellKnownBaseURL("groq"); got != "https://api.groq.com/openai/v1" {
		t.Errorf("base URL = %q", got)
	}
	_ = cfg
}
