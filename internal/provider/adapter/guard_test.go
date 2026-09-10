package adapter

import (
	"testing"
)

func TestIsCompatibleProviderModel(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     bool
	}{
		{"ollama", "llama3", true},
		{"ollama", "llama3:8b", true},
		{"ollama", "openai/gpt-4o", false}, // vendor prefix not allowed for ollama
		{"openrouter", "openai/gpt-4o", true},
		{"openrouter", "anthropic/claude-3.5-sonnet", true},
		{"openrouter", "llama3", false}, // missing vendor prefix
		{"openai", "gpt-4o", true},
		{"openai", "", false},
		{"", "model", false},
		{"unknown", "any-model", true},
	}

	for _, c := range cases {
		got := IsCompatibleProviderModel(c.provider, c.model)
		if got != c.want {
			t.Errorf("IsCompatibleProviderModel(%q, %q) = %v, want %v", c.provider, c.model, got, c.want)
		}
	}
}

func TestProviderSwitchGuard(t *testing.T) {
	t.Run("compatible switch", func(t *testing.T) {
		if err := ProviderSwitchGuard("openrouter", "openai/gpt-4o"); err != nil {
			t.Errorf("unexpected error for compatible switch: %v", err)
		}
	})
	t.Run("incompatible switch", func(t *testing.T) {
		err := ProviderSwitchGuard("ollama", "openai/gpt-4o")
		if err == nil {
			t.Error("expected error for incompatible switch, got nil")
		}
		if err.Error() == "" {
			t.Error("expected non-empty error message")
		}
	})
}
