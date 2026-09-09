// Package detector implements provider auto-detection for Phase 1.
//
// Precedence: environment variables always override credentials stored in
// ~/.izen/credentials/providers.json. Detection performs no network I/O
// and never writes credentials.
package detector

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ProviderConfig describes a single detected provider credential.
type ProviderConfig struct {
	Name    string `json:"name"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	// Source is "env" when the key came from the environment and "file"
	// when it was loaded from providers.json. It is never persisted.
	Source string `json:"-"`
}

// envBinding maps a provider name to its environment variable and
// OpenAI-compatible base URL.
type envBinding struct {
	EnvVar  string
	BaseURL string
}

// bindings is the authoritative provider -> env/baseURL table.
// GROQ and OpenRouter entries carry the exact BaseURLs required by spec.
var bindings = map[string]envBinding{
	"groq": {
		EnvVar:  "GROQ_API_KEY",
		BaseURL: "https://api.groq.com/openai/v1",
	},
	"openrouter": {
		EnvVar:  "OPENROUTER_API_KEY",
		BaseURL: "https://openrouter.ai/api/v1",
	},
	"anthropic": {
		EnvVar:  "ANTHROPIC_API_KEY",
		BaseURL: "https://api.anthropic.com/v1",
	},
	"openai": {
		EnvVar:  "OPENAI_API_KEY",
		BaseURL: "https://api.openai.com/v1",
	},
	"gemini": {
		EnvVar:  "GEMINI_API_KEY",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
	},
}

// fileProvider is one entry inside providers.json. The on-disk format is
// tolerant: it accepts {name, token|api_key, base_url?} objects under either
// "encrypted_providers" (legacy auth.go format) or "providers".
type fileProvider struct {
	Name    string `json:"name"`
	Token   string `json:"token"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
}

// DetectProviders scans environment variables with absolute precedence and
// merges credentials from ~/.izen/credentials/providers.json.
// Environment keys MUST override file credentials for the same provider.
func DetectProviders() []ProviderConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return DetectProvidersWithHome(home)
}

// DetectProvidersWithHome is DetectProviders with an injectable home
// directory for tests. An empty home disables file-credential loading.
func DetectProvidersWithHome(home string) []ProviderConfig {
	env := detectFromEnv()
	if home == "" {
		return env
	}
	fileCreds := loadFileCredentials(filepath.Join(home, ".izen", "credentials", "providers.json"))
	return mergeProviders(env, fileCreds)
}

// detectFromEnv returns one ProviderConfig per set, non-empty env var.
func detectFromEnv() []ProviderConfig {
	out := make([]ProviderConfig, 0, len(bindings))
	for name, b := range bindings {
		key := os.Getenv(b.EnvVar)
		if key == "" {
			continue
		}
		out = append(out, ProviderConfig{
			Name:    name,
			APIKey:  key,
			BaseURL: b.BaseURL,
			Source:  "env",
		})
	}
	return out
}

// loadFileCredentials reads providers.json tolerantly. Unknown shapes,
// missing files, and parse errors yield an empty slice (never an error:
// detection must not fail when credentials are absent).
func loadFileCredentials(path string) []ProviderConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// Shape 1: {"encrypted_providers": [...]} (written by config.SaveProviderToken).
	var shape1 struct {
		EncryptedProviders []fileProvider `json:"encrypted_providers"`
	}
	if err := json.Unmarshal(data, &shape1); err == nil && shape1.EncryptedProviders != nil {
		return normalizeFileProviders(shape1.EncryptedProviders)
	}
	// Shape 2: {"providers": [...]}.
	var shape2 struct {
		Providers []fileProvider `json:"providers"`
	}
	if err := json.Unmarshal(data, &shape2); err == nil && shape2.Providers != nil {
		return normalizeFileProviders(shape2.Providers)
	}
	// Shape 3: bare array [...].
	var shape3 []fileProvider
	if err := json.Unmarshal(data, &shape3); err == nil {
		return normalizeFileProviders(shape3)
	}
	// Shape 4: map {"openrouter": "sk-..."} or {"openrouter": {"api_key": ...}}.
	var shape4 map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape4); err == nil {
		out := make([]ProviderConfig, 0, len(shape4))
		for name, raw := range shape4 {
			if name == "note" {
				continue
			}
			var asString string
			if err := json.Unmarshal(raw, &asString); err == nil {
				if asString == "" {
					continue
				}
				out = append(out, ProviderConfig{
					Name:    name,
					APIKey:  asString,
					BaseURL: defaultBaseURL(name),
					Source:  "file",
				})
				continue
			}
			var obj fileProvider
			if err := json.Unmarshal(raw, &obj); err == nil {
				obj.Name = name
				out = append(out, normalizeFileProviders([]fileProvider{obj})...)
			}
		}
		return out
	}
	return nil
}

// normalizeFileProviders converts tolerant file entries into ProviderConfigs,
// skipping entries without a usable key.
func normalizeFileProviders(entries []fileProvider) []ProviderConfig {
	out := make([]ProviderConfig, 0, len(entries))
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		key := e.APIKey
		if key == "" {
			key = e.Token
		}
		if key == "" {
			continue
		}
		base := e.BaseURL
		if base == "" {
			base = defaultBaseURL(e.Name)
		}
		out = append(out, ProviderConfig{
			Name:    e.Name,
			APIKey:  key,
			BaseURL: base,
			Source:  "file",
		})
	}
	return out
}

// mergeProviders overlays env providers on top of file credentials by name.
// Env entries win; file-only entries are appended.
func mergeProviders(env, file []ProviderConfig) []ProviderConfig {
	seen := make(map[string]struct{}, len(env)+len(file))
	out := make([]ProviderConfig, 0, len(env)+len(file))
	for _, p := range env {
		seen[p.Name] = struct{}{}
		out = append(out, p)
	}
	for _, p := range file {
		if _, ok := seen[p.Name]; ok {
			continue
		}
		seen[p.Name] = struct{}{}
		out = append(out, p)
	}
	return out
}

// defaultBaseURL returns the known BaseURL for a provider, or "" when unknown.
func defaultBaseURL(name string) string {
	if b, ok := bindings[name]; ok {
		return b.BaseURL
	}
	return ""
}

// BaseURLFor returns the canonical BaseURL for a known provider name.
func BaseURLFor(name string) string {
	return defaultBaseURL(name)
}
