package config

import (
	"os"
	"testing"
)

func setEnv(t *testing.T, key, val string) {
	t.Helper()
	_ = os.Setenv(key, val)
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	_ = os.Unsetenv(key)
}

func TestExpandEnvVar(t *testing.T) {
	setEnv(t, "TEST_ANTHROPIC_KEY", "sk-ant-test123")
	defer unsetEnv(t, "TEST_ANTHROPIC_KEY")

	result := ExpandEnvVar("${TEST_ANTHROPIC_KEY}")
	if result != "sk-ant-test123" {
		t.Errorf("ExpandEnvVar = %q, want %q", result, "sk-ant-test123")
	}
}

func TestExpandEnvVarNoMatch(t *testing.T) {
	result := ExpandEnvVar("plain-string")
	if result != "plain-string" {
		t.Errorf("ExpandEnvVar(plain) = %q, want %q", result, "plain-string")
	}
}

func TestExpandEnvVarMissing(t *testing.T) {
	result := ExpandEnvVar("${DOES_NOT_EXIST_ANYWHERE}")
	if result != "" {
		t.Errorf("ExpandEnvVar(missing) = %q, want empty", result)
	}
}

func TestExpandEnvVarDefault(t *testing.T) {
	result := ExpandEnvVar("${UNSET_VAR:-default-val}")
	if result != "default-val" {
		t.Errorf("ExpandEnvVar with default = %q, want %q", result, "default-val")
	}
}

func TestExpandEnvVarWithDefaultButEnvSet(t *testing.T) {
	setEnv(t, "TEST_WITH_DEFAULT", "actual-value")
	defer unsetEnv(t, "TEST_WITH_DEFAULT")

	result := ExpandEnvVar("${TEST_WITH_DEFAULT:-fallback}")
	if result != "actual-value" {
		t.Errorf("ExpandEnvVar with env set = %q, want %q", result, "actual-value")
	}
}

func TestExpandEnvVarMultipleVars(t *testing.T) {
	setEnv(t, "TEST_KEY1", "val1")
	setEnv(t, "TEST_KEY2", "val2")
	defer unsetEnv(t, "TEST_KEY1")
	defer unsetEnv(t, "TEST_KEY2")

	result := ExpandEnvVar("prefix-${TEST_KEY1}-middle-${TEST_KEY2}-suffix")
	expected := "prefix-val1-middle-val2-suffix"
	if result != expected {
		t.Errorf("ExpandEnvVar(multi) = %q, want %q", result, expected)
	}
}

func TestExpandEnvVarsOnAIConfig(t *testing.T) {
	setEnv(t, "TEST_AI_API_KEY", "sk-ai-test")
	defer unsetEnv(t, "TEST_AI_API_KEY")

	cfg := AIConfig{
		Providers: map[string]AIProviderConfig{
			"test-provider": {
				APIKey: "${TEST_AI_API_KEY}",
			},
		},
	}

	cfg.ExpandEnvVars()

	if cfg.Providers["test-provider"].APIKey != "sk-ai-test" {
		t.Errorf("APIKey = %q, want %q", cfg.Providers["test-provider"].APIKey, "sk-ai-test")
	}
}

func TestExpandEnvVarsSkipsNonEnvVars(t *testing.T) {
	cfg := AIConfig{
		Providers: map[string]AIProviderConfig{
			"ollama": {
				APIKey: "ollama",
			},
		},
	}

	cfg.ExpandEnvVars()

	if cfg.Providers["ollama"].APIKey != "ollama" {
		t.Errorf("APIKey should remain unchanged, got %q", cfg.Providers["ollama"].APIKey)
	}
}

func TestDefaultConfigHasProviders(t *testing.T) {
	cfg := Default()

	if cfg.AI.DefaultProvider != "ollama" {
		t.Errorf("DefaultProvider = %q, want %q", cfg.AI.DefaultProvider, "ollama")
	}

	providers := []string{"ollama", "anthropic", "openai", "openrouter", "groq"}
	for _, name := range providers {
		if _, ok := cfg.AI.Providers[name]; !ok {
			t.Errorf("Default config missing provider %q", name)
		}
	}

	if cfg.AI.Providers["ollama"].DefaultModel != "qwen2.5-coder:7b" {
		t.Errorf("ollama model = %q, want %q", cfg.AI.Providers["ollama"].DefaultModel, "qwen2.5-coder:7b")
	}

	if cfg.AI.Providers["anthropic"].DefaultModel != "claude-sonnet-4-20250514" {
		t.Errorf("anthropic model = %q", cfg.AI.Providers["anthropic"].DefaultModel)
	}

	if cfg.AI.Providers["openrouter"].DefaultModel != "anthropic/claude-3.5-sonnet" {
		t.Errorf("openrouter model = %q", cfg.AI.Providers["openrouter"].DefaultModel)
	}

	if cfg.AI.Providers["groq"].DefaultModel != "llama-3.3-70b-versatile" {
		t.Errorf("groq model = %q", cfg.AI.Providers["groq"].DefaultModel)
	}

	if cfg.AI.Providers["groq"].BaseURL != "https://api.groq.com/openai/v1" {
		t.Errorf("groq base_url = %q", cfg.AI.Providers["groq"].BaseURL)
	}
}

func TestValidateWithAnthropicConfig(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			DefaultProvider: "anthropic",
			Providers: map[string]AIProviderConfig{
				"anthropic": {
					BaseURL:      "https://api.anthropic.com/v1",
					APIKey:       "sk-ant-test",
					DefaultModel: "claude-sonnet-4-20250514",
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestActiveProviderNameFallback(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			Providers: map[string]AIProviderConfig{
				"ollama": {BaseURL: "http://localhost:11434/v1"},
			},
		},
	}

	name := cfg.ActiveProviderName()
	if name != "unknown" {
		t.Errorf("ActiveProviderName = %q, want %q", name, "unknown")
	}
}

func TestConfigWithEnvExpansionInProvider(t *testing.T) {
	setEnv(t, "TEST_CFG_KEY", "sk-test-cfg")
	defer unsetEnv(t, "TEST_CFG_KEY")

	cfg := &Config{
		AI: AIConfig{
			DefaultProvider: "anthropic",
			Providers: map[string]AIProviderConfig{
				"anthropic": {
					BaseURL:      "https://api.anthropic.com/v1",
					APIKey:       "${TEST_CFG_KEY}",
					DefaultModel: "claude-sonnet-4-20250514",
				},
			},
		},
	}

	cfg.AI.ExpandEnvVars()

	if cfg.AI.Providers["anthropic"].APIKey != "sk-test-cfg" {
		t.Errorf("After ExpandEnvVars, APIKey = %q, want %q", cfg.AI.Providers["anthropic"].APIKey, "sk-test-cfg")
	}
}

func TestActiveModelName(t *testing.T) {
	cfg := &Config{
		Models: ModelConfig{
			Default: "custom-model",
		},
	}

	model := cfg.ActiveModelName()
	if model != "custom-model" {
		t.Errorf("ActiveModelName = %q, want %q", model, "custom-model")
	}
}

func TestSanitizeForSession(t *testing.T) {
	input := "Hello @user, how are you?"
	result := SanitizeForSession(input)
	if result != "Hello [redacted], how are you?" {
		t.Errorf("SanitizeForSession = %q", result)
	}
}

func TestSanitizeUsername(t *testing.T) {
	if got := SanitizeUsername("@Jaky"); got != "Jaky" {
		t.Errorf("SanitizeUsername(@Jaky) = %q", got)
	}
	if got := SanitizeUsername(""); got != "Developer" {
		t.Errorf("SanitizeUsername(empty) = %q", got)
	}
}

func TestFallbackConfigDefault(t *testing.T) {
	cfg := Default()
	if !cfg.Fallback.Enabled {
		t.Error("Fallback should be enabled by default")
	}
}

func TestConfigLoadWithEnvVars(t *testing.T) {
	setEnv(t, "TEST_AI_BASE_URL", "http://custom:8080/v1")
	defer unsetEnv(t, "TEST_AI_BASE_URL")

	cfg := &Config{
		AI: AIConfig{
			DefaultProvider: "test-provider",
			Providers: map[string]AIProviderConfig{
				"test-provider": {
					BaseURL: "${TEST_AI_BASE_URL}",
					APIKey:  "test-key",
				},
			},
		},
	}

	cfg.AI.ExpandEnvVars()

	if cfg.AI.Providers["test-provider"].BaseURL != "http://custom:8080/v1" {
		t.Errorf("ExpandEnvVars(BaseURL) = %q, want %q",
			cfg.AI.Providers["test-provider"].BaseURL, "http://custom:8080/v1")
	}
}

func TestExpandEnvVarNestedBraces(t *testing.T) {
	result := ExpandEnvVar("prefix-${NESTED:-default-val}-suffix")
	if result != "prefix-default-val-suffix" {
		t.Errorf("got %q, want %q", result, "prefix-default-val-suffix")
	}
}

func TestValidateProviderName(t *testing.T) {
	valid := []string{"ollama", "anthropic", "openai", "openrouter", "gemini", "groq"}
	for _, name := range valid {
		if !ValidateProviderName(name) {
			t.Errorf("ValidateProviderName(%q) = false, want true", name)
		}
	}

	if ValidateProviderName("unknown") {
		t.Errorf("ValidateProviderName(%q) = true, want false", "unknown")
	}
	if ValidateProviderName("claude") {
		t.Errorf("ValidateProviderName(%q) = true, want false", "claude")
	}
}

func TestValidateProviderNameOpenRouter(t *testing.T) {
	if !ValidateProviderName("openrouter") {
		t.Errorf("ValidateProviderName(\"openrouter\") = false, want true — openrouter must be accepted")
	}
}

func TestResolveCredentialsEnvVar(t *testing.T) {
	setEnv(t, "ANTHROPIC_API_KEY", "sk-test-resolve")
	defer unsetEnv(t, "ANTHROPIC_API_KEY")

	key := ResolveCredentials("anthropic", "fallback-key")
	if key != "sk-test-resolve" {
		t.Errorf("ResolveCredentials = %q, want %q (env var should take priority)", key, "sk-test-resolve")
	}
}

func TestResolveCredentialsFallback(t *testing.T) {
	key := ResolveCredentials("unknown-provider", "config-fallback-key")
	if key != "config-fallback-key" {
		t.Errorf("ResolveCredentials = %q, want %q (should fall back to config key)", key, "config-fallback-key")
	}
}

func TestHasCredentialsOpenRouter(t *testing.T) {
	setEnv(t, "OPENROUTER_API_KEY", "sk-or-test")
	defer unsetEnv(t, "OPENROUTER_API_KEY")

	if !HasCredentials("openrouter") {
		t.Errorf("HasCredentials(\"openrouter\") = false, want true after setting OPENROUTER_API_KEY")
	}
}

func TestHasCredentialsOpenRouterUnset(t *testing.T) {
	unsetEnv(t, "OPENROUTER_API_KEY")

	if HasCredentials("openrouter") {
		t.Errorf("HasCredentials(\"openrouter\") = true, want false when no env/creds configured")
	}
}

func TestCredentialSourceEnv(t *testing.T) {
	setEnv(t, "OPENAI_API_KEY", "sk-oi-test")
	defer unsetEnv(t, "OPENAI_API_KEY")

	source := CredentialSource("openai")
	if source != "env" {
		t.Errorf("CredentialSource(\"openai\") = %q, want %q", source, "env")
	}
}

func TestCredentialSourceNone(t *testing.T) {
	unsetEnv(t, "GEMINI_API_KEY")

	source := CredentialSource("gemini")
	if source != "" {
		t.Errorf("CredentialSource(\"gemini\") = %q, want %q", source, "")
	}
}
