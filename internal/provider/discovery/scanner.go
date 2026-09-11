// Package discovery implements the Live ENV & Provider Discovery Engine.
//
// It scans local environment variables for active API keys and probes local
// runtimes (Ollama) without requiring any registry imports — the registry
// layer converts discovery results into ModelDescriptors. This keeps the
// dependency direction registry -> discovery (never the reverse).
package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

// Endpoint mapping for live discovery:
//
//	OPENROUTER_API_KEY -> https://openrouter.ai/api/v1/models
//	OPENAI_API_KEY     -> https://api.openai.com/v1/models
//	ANTHROPIC_API_KEY  -> static registry (no public /models endpoint)
//	GEMINI_API_KEY / GOOGLE_API_KEY -> Google AI Studio model list
//	DEEPSEEK_API_KEY   -> https://api.deepseek.com/v1/models
//	Ollama local       -> http://localhost:11434/api/tags (no key)
const (
	openRouterBase = "https://openrouter.ai/api/v1"
	openAIBase     = "https://api.openai.com/v1"
	anthropicBase  = "https://api.anthropic.com/v1"
	geminiBase     = "https://generativelanguage.googleapis.com/v1beta/openai"
	deepseekBase   = "https://api.deepseek.com/v1"
	groqBase       = "https://api.groq.com/openai/v1"

	// OllamaTagsURL is the local runtime probe endpoint. Ollama needs no
	// API key; a responsive /api/tags means local models are available.
	OllamaTagsURL = "http://localhost:11434/api/tags"
	// OllamaBase is the OpenAI-compatible base for the ollama provider.
	OllamaBase = "http://localhost:11434/v1"
)

// ollamaProbeTimeout bounds the local runtime ping so discovery never hangs
// the TUI when Ollama is not running.
const ollamaProbeTimeout = 1500 * time.Millisecond

// extraBinding maps additional env vars (aliases + providers the detector
// table does not yet cover) to provider configs.
type extraBinding struct {
	EnvVar   string
	Provider string
	BaseURL  string
}

// extraBindings covers GOOGLE_API_KEY (gemini alias) and DEEPSEEK_API_KEY.
// Detector-native vars (OPENROUTER/OPENAI/ANTHROPIC/GEMINI/GROQ) are handled
// by detector.DetectProviders; we re-scan them here too so EnvProviders is
// usable standalone.
var extraBindings = []extraBinding{
	{EnvVar: "OPENROUTER_API_KEY", Provider: "openrouter", BaseURL: openRouterBase},
	{EnvVar: "OPENAI_API_KEY", Provider: "openai", BaseURL: openAIBase},
	{EnvVar: "ANTHROPIC_API_KEY", Provider: "anthropic", BaseURL: anthropicBase},
	{EnvVar: "GEMINI_API_KEY", Provider: "gemini", BaseURL: geminiBase},
	{EnvVar: "GOOGLE_API_KEY", Provider: "gemini", BaseURL: geminiBase},
	{EnvVar: "DEEPSEEK_API_KEY", Provider: "deepseek", BaseURL: deepseekBase},
	{EnvVar: "GROQ_API_KEY", Provider: "groq", BaseURL: groqBase},
}

// EnvProviders scans the live environment for active API keys and returns
// one ProviderConfig per set, non-empty variable. GOOGLE_API_KEY is aliased
// to the gemini provider. Empty environment yields an empty slice.
func EnvProviders() []detector.ProviderConfig {
	out := make([]detector.ProviderConfig, 0, len(extraBindings))
	seen := make(map[string]struct{}, len(extraBindings))
	for _, b := range extraBindings {
		if _, ok := seen[b.Provider]; ok {
			continue
		}
		key := strings.TrimSpace(os.Getenv(b.EnvVar))
		if key == "" {
			continue
		}
		seen[b.Provider] = struct{}{}
		out = append(out, detector.ProviderConfig{
			Name:    b.Provider,
			APIKey:  key,
			BaseURL: b.BaseURL,
			Source:  "env",
		})
	}
	return out
}

// OllamaModel is the minimal local model identity parsed from /api/tags.
type OllamaModel struct {
	ID   string
	Name string
}

// ollamaTagsResponse is the /api/tags shape: {"models":[{"name":"..."}]}.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// OllamaAvailable pings the local Ollama runtime. False means not running;
// any error (connection refused, timeout) is swallowed by design.
func OllamaAvailable(ctx context.Context) bool {
	_, ok := OllamaModels(ctx, nil)
	return ok
}

// OllamaModels fetches the local model list from /api/tags. A nil client
// uses a short-timeout default. The boolean reports reachability (false =
// Ollama not running; not an error the caller must surface).
func OllamaModels(ctx context.Context, client *http.Client) ([]OllamaModel, bool) {
	if client == nil {
		client = &http.Client{Timeout: ollamaProbeTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OllamaTagsURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, false
	}
	return ParseOllamaTags(body), true
}

// ParseOllamaTags parses a raw /api/tags payload into model identities.
// Malformed payloads yield an empty (non-nil) slice, never an error.
func ParseOllamaTags(data []byte) []OllamaModel {
	var parsed ollamaTagsResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return []OllamaModel{}
	}
	out := make([]OllamaModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		out = append(out, OllamaModel{ID: name, Name: name})
	}
	return out
}

// DiscoverProviders merges detector results (config.yml with precedence,
// then env, then file credentials) with live extras: the
// GOOGLE_API_KEY/DEEPSEEK_API_KEY aliases and the Ollama local runtime (no
// key required). Ollama is appended only when its /api/tags probe responds. Results are deduplicated by provider
// name; detector entries win on conflict except the gemini alias, which
// backfills only when gemini is otherwise absent.
func DiscoverProviders(ctx context.Context) []detector.ProviderConfig {
	base := detector.DetectProviders()
	seen := make(map[string]detector.ProviderConfig, len(base)+2)
	for _, p := range base {
		seen[p.Name] = p
	}
	// Backfill alias providers absent from the detector table.
	for _, p := range EnvProviders() {
		if _, ok := seen[p.Name]; ok {
			continue
		}
		seen[p.Name] = p
	}
	// Local runtime: reachable Ollama joins with a placeholder key so sync
	// workers (which skip empty keys) still query it. The key is never sent
	// as meaningful auth; /api/tags needs none and /v1/models ignores it.
	if _, ok := seen["ollama"]; !ok {
		if OllamaAvailable(ctx) {
			seen["ollama"] = detector.ProviderConfig{
				Name:    "ollama",
				APIKey:  "local",
				BaseURL: OllamaBase,
				Source:  "env",
			}
		}
	}
	out := make([]detector.ProviderConfig, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	return out
}
