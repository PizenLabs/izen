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
	default:
		return strings.ToUpper(provider) + "_API_KEY"
	}
}

// ResolveCredentials resolves the best available API key for a provider.
// Priority:
//  1. Environment variable (e.g., ANTHROPIC_API_KEY)
//  2. Stored OAuth/session token from ~/.izen/credentials/providers.json
//  3. Config file api_key string (already expanded)
func ResolveCredentials(provider, configKey string) string {
	envVar := envVarForProvider(provider)
	if envVar != "" {
		if envVal := os.Getenv(envVar); envVal != "" {
			return envVal
		}
	}

	token := loadStoredToken(provider)
	if token != "" {
		return token
	}

	return configKey
}

// HasCredentials returns true if the provider has credentials available
// through any of the three sources (env var, stored token, config key).
func HasCredentials(provider string) bool {
	envVar := envVarForProvider(provider)
	if envVar != "" && os.Getenv(envVar) != "" {
		return true
	}
	if token := loadStoredToken(provider); token != "" {
		return true
	}
	return false
}

// CredentialSource returns a human-readable description of where the
// credential was found. Returns "env", "token", "config", or "".
func CredentialSource(provider string) string {
	envVar := envVarForProvider(provider)
	if envVar != "" && os.Getenv(envVar) != "" {
		return "env"
	}
	if token := loadStoredToken(provider); token != "" {
		return "token"
	}
	return ""
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
