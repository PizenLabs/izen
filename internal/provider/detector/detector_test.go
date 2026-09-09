package detector

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProvidersFile writes a legacy-shape providers.json under home.
func writeProvidersFile(t *testing.T, home, payload string) {
	t.Helper()
	dir := filepath.Join(home, ".izen", "credentials")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(payload), 0600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
}

func findByName(list []ProviderConfig, name string) *ProviderConfig {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func TestEnvOverridesFileCredential(t *testing.T) {
	home := t.TempDir()
	writeProvidersFile(t, home, `{"encrypted_providers":[{"name":"openrouter","token":"file-token"}]}`)
	t.Setenv("OPENROUTER_API_KEY", "env-token")
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	got := DetectProvidersWithHome(home)
	or := findByName(got, "openrouter")
	if or == nil {
		t.Fatalf("openrouter missing from %v", got)
	}
	if or.APIKey != "env-token" {
		t.Errorf("APIKey = %q, want %q (env must override file)", or.APIKey, "env-token")
	}
	if or.Source != "env" {
		t.Errorf("Source = %q, want %q", or.Source, "env")
	}
	if or.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("BaseURL = %q, want openrouter endpoint", or.BaseURL)
	}
}

func TestGroqBaseURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROQ_API_KEY", "gsk-test")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	got := DetectProvidersWithHome(home)
	g := findByName(got, "groq")
	if g == nil {
		t.Fatalf("groq missing from %v", got)
	}
	if g.BaseURL != "https://api.groq.com/openai/v1" {
		t.Errorf("BaseURL = %q, want groq endpoint", g.BaseURL)
	}
}

func TestFileOnlyCredentialIncluded(t *testing.T) {
	home := t.TempDir()
	writeProvidersFile(t, home, `{"encrypted_providers":[{"name":"openai","token":"file-only"}]}`)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	got := DetectProvidersWithHome(home)
	o := findByName(got, "openai")
	if o == nil {
		t.Fatalf("openai file credential missing from %v", got)
	}
	if o.APIKey != "file-only" || o.Source != "file" {
		t.Errorf("got %+v, want file credential", o)
	}
	if o.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q, want openai endpoint", o.BaseURL)
	}
}

func TestNoCredentialsYieldsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	if got := DetectProvidersWithHome(home); len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestMalformedProvidersFileIgnored(t *testing.T) {
	home := t.TempDir()
	writeProvidersFile(t, home, `not-json{{{`)
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	if got := DetectProvidersWithHome(home); len(got) != 0 {
		t.Errorf("got %v, want empty on malformed file", got)
	}
}
