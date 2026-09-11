package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/state"
)

type storedCredentials struct {
	EncryptedProviders []storedProvider `json:"encrypted_providers"`
}

type storedProvider struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// envVarForProvider returns the primary env var name for a given provider
func envVarForProvider(provider string) string {
	return EnvVarForProvider(provider)
}

// EnvVarForProvider returns the primary env var name for a given provider.
// It is exported so runtime composition roots (compose, cmd/izen, ui) share
// the single authoritative provider -> env var table with this package.
func EnvVarForProvider(provider string) string {
	switch provider {
	case "ollama":
		return ""
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "opencode":
		return "OPENCODE_API_KEY"
	case "9router":
		return "9ROUTER_API_KEY"
	default:
		return strings.ToUpper(provider) + "_API_KEY"
	}
}

// ResolveProviderAPIKey resolves the API key for a provider with strict
// 3-tier precedence:
//
//  1. Top priority: explicit key saved in ~/.izen/config.yml
//     (cfg.AI.Providers[provider].APIKey, already env-expanded on load).
//  2. Fallback: shell environment variable (e.g. OPENROUTER_API_KEY).
//  3. Empty: return "" so the caller triggers the missing-key prompt.
//
// The config file always wins: a key the user explicitly saved via
// SaveProviderAPIKey (or hand-edited into config.yml) is authoritative over
// any value exported from ~/.zshrc or the process environment.
func ResolveProviderAPIKey(cfg *Config, provider string, envVar string) string {
	if cfg != nil {
		if p, ok := cfg.AI.Providers[strings.ToLower(strings.TrimSpace(provider))]; ok {
			if key := strings.TrimSpace(p.APIKey); key != "" {
				return key
			}
		}
	}
	if strings.TrimSpace(envVar) != "" {
		if envVal := strings.TrimSpace(os.Getenv(envVar)); envVal != "" {
			return envVal
		}
	}
	return ""
}

// ResolveAPIKey resolves the API key for a provider against this Config,
// deriving the environment variable from the canonical provider -> env var
// table. Precedence: config file > env var > "".
func (c *Config) ResolveAPIKey(provider string) string {
	return ResolveProviderAPIKey(c, provider, EnvVarForProvider(provider))
}

// ResolveCredentials resolves the best available API key for a provider.
// Priority:
//
//  1. Config file api_key string (already expanded)
//  2. Environment variable (e.g., ANTHROPIC_API_KEY)
//  3. Stored OAuth/session token from ~/.izen/credentials/providers.json
//
// A key explicitly saved in ~/.izen/config.yml is authoritative and always
// wins over the shell environment.
func ResolveCredentials(provider, configKey string) string {
	if key := strings.TrimSpace(configKey); key != "" {
		return key
	}

	envVar := envVarForProvider(provider)
	if envVar != "" {
		if envVal := strings.TrimSpace(os.Getenv(envVar)); envVal != "" {
			return envVal
		}
	}

	token := loadStoredToken(provider)
	if token != "" {
		return token
	}

	return ""
}

// HasCredentials returns true if the provider has credentials available
// through the environment or the stored token file. For the config-aware
// check (config file > env > token) use HasCredentialsFor.
func HasCredentials(provider string) bool {
	envVar := envVarForProvider(provider)
	if envVar != "" && strings.TrimSpace(os.Getenv(envVar)) != "" {
		return true
	}
	if token := loadStoredToken(provider); token != "" {
		return true
	}
	return false
}

// HasCredentialsFor returns true when the provider resolves to a non-empty
// key under the strict precedence (config file > env var > stored token).
func HasCredentialsFor(cfg *Config, provider string) bool {
	if cfg != nil {
		if p, ok := cfg.AI.Providers[strings.ToLower(strings.TrimSpace(provider))]; ok {
			if strings.TrimSpace(p.APIKey) != "" {
				return true
			}
		}
	}
	return HasCredentials(provider)
}

// CredentialSource returns a human-readable description of where the
// credential was found. Returns "env", "token", or "".
func CredentialSource(provider string) string {
	envVar := envVarForProvider(provider)
	if envVar != "" && strings.TrimSpace(os.Getenv(envVar)) != "" {
		return "env"
	}
	if token := loadStoredToken(provider); token != "" {
		return "token"
	}
	return ""
}

// CredentialSourceFor returns where the credential was found under the strict
// precedence: "config" (explicit key in ~/.izen/config.yml), "env",
// "token", or "" when no credential exists.
func CredentialSourceFor(cfg *Config, provider string) string {
	if cfg != nil {
		if p, ok := cfg.AI.Providers[strings.ToLower(strings.TrimSpace(provider))]; ok {
			if strings.TrimSpace(p.APIKey) != "" {
				return "config"
			}
		}
	}
	return CredentialSource(provider)
}

func loadStoredToken(provider string) string {
	providersPath := state.GlobalPath(state.GlobalCredentialsDir, state.GlobalProvidersFile)
	data, err := os.ReadFile(providersPath)
	if err != nil {
		return ""
	}

	var creds storedCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return ""
	}

	for _, p := range creds.EncryptedProviders {
		if p.Name == provider && p.Token != "" {
			return p.Token
		}
	}
	return ""
}

// SaveProviderToken persists an OAuth/session token for a provider to
// ~/.izen/credentials/providers.json.
func SaveProviderToken(provider, token string) error {
	providersPath := state.GlobalPath(state.GlobalCredentialsDir, state.GlobalProvidersFile)

	var creds storedCredentials
	data, err := os.ReadFile(providersPath)
	if err == nil {
		_ = json.Unmarshal(data, &creds)
	}

	found := false
	for i, p := range creds.EncryptedProviders {
		if p.Name == provider {
			creds.EncryptedProviders[i].Token = token
			found = true
			break
		}
	}
	if !found {
		creds.EncryptedProviders = append(creds.EncryptedProviders, storedProvider{
			Name:  provider,
			Token: token,
		})
	}

	out, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	dir := filepath.Dir(providersPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir credentials: %w", err)
	}

	if err := os.WriteFile(providersPath, out, 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	return nil
}
