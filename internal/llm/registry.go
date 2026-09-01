package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ModelInfo represents a discovered model with dynamic capabilities.
type ModelInfo struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name,omitempty"`
	Provider            string        `json:"provider"`
	SupportsReasoning   *bool         `json:"supports_reasoning,omitempty"`
	ContextWindow       int           `json:"context_window,omitempty"`
	MaxOutputTokens     int           `json:"max_output_tokens,omitempty"`
	SupportedParameters []string      `json:"supported_parameters,omitempty"`
	Architecture        *Architecture `json:"architecture,omitempty"`
}

// Architecture mirrors OpenRouter architecture.modality.
type Architecture struct {
	Modality string `json:"modality,omitempty"`
}

type ModelRegistry struct {
	mu       sync.RWMutex
	models   []ModelInfo
	cachedAt time.Time
	ttl      time.Duration
	client   *http.Client
}

func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		ttl:    5 * time.Minute,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (r *ModelRegistry) GetModels(providers map[string]string) ([]ModelInfo, error) {
	r.mu.RLock()
	if r.models != nil && time.Since(r.cachedAt) < r.ttl {
		cpy := make([]ModelInfo, len(r.models))
		copy(cpy, r.models)
		r.mu.RUnlock()
		return cpy, nil
	}
	r.mu.RUnlock()

	return r.Refresh(providers)
}

func (r *ModelRegistry) Refresh(providers map[string]string) ([]ModelInfo, error) {
	var all []ModelInfo
	var mu sync.Mutex
	var wg sync.WaitGroup
	errCh := make(chan error, 8)

	for name, apiKey := range providers {
		wg.Add(1)
		go func(provider, key string) {
			defer wg.Done()
			models, err := fetchProviderModels(provider, key, r.client)
			if err != nil {
				errCh <- fmt.Errorf("%s: %w", provider, err)
				return
			}
			mu.Lock()
			all = append(all, models...)
			mu.Unlock()
		}(name, apiKey)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	close(errCh)

	var firstErr error
	for err := range errCh {
		if firstErr == nil {
			firstErr = err
		}
	}

	// Dynamic discovery failure handling with fallback chain:
	// Local Override -> API Endpoint Metadata -> Cached Schema -> Heuristic Spec Fallback.
	// If API fetched at least some models, enrich via cache/override and persist.
	// If API fetched nothing (all providers failed), try cached schema (24h TTL), else heuristic.

	overrides := loadOverrides()

	if len(all) == 0 {
		// Try cached schema (24h TTL)
		if cached, ok := loadCachedIfFresh(24 * time.Hour); ok && len(cached) > 0 {
			all = cached
			firstErr = nil // cached fallback masks API errors
		} else {
			// Heuristic fallback: static catalog for known providers
			all = heuristicFallbackModels()
		}
	} else {
		// Persist successful API fetch to cache (24h TTL backing)
		_ = saveCachedSchema(all)
	}

	// Apply local overrides (highest priority)
	all = applyOverrides(all, overrides)

	// Ensure every model has heuristic values for missing fields
	all = enrichWithHeuristics(all)

	sort.Slice(all, func(i, j int) bool {
		if all[i].Provider != all[j].Provider {
			return all[i].Provider < all[j].Provider
		}
		return all[i].ID < all[j].ID
	})

	r.mu.Lock()
	r.models = all
	r.cachedAt = time.Now()
	r.mu.Unlock()

	return all, firstErr
}

func (r *ModelRegistry) InvalidateCache() {
	r.mu.Lock()
	r.models = nil
	r.cachedAt = time.Time{}
	r.mu.Unlock()
	// Also remove file cache so next Refresh forces API fetch
	_ = os.Remove(cacheFilePath())
}

func (r *ModelRegistry) SetTTL(d time.Duration) {
	r.mu.Lock()
	r.ttl = d
	r.mu.Unlock()
}

func fetchProviderModels(provider, apiKey string, client *http.Client) ([]ModelInfo, error) {
	switch provider {
	case "openrouter":
		return fetchOpenRouterModels(client, apiKey)
	case "ollama":
		return fetchOllamaModels(client)
	case "anthropic":
		return fetchAnthropicModels()
	case "openai":
		return fetchOpenAIModels()
	default:
		return nil, nil
	}
}

// ── OpenRouter dynamic discovery ────────────────────────────────────────────

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

type openRouterModel struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name,omitempty"`
	ContextLength       int           `json:"context_length,omitempty"`
	SupportedParameters []string      `json:"supported_parameters,omitempty"`
	Architecture        *Architecture `json:"architecture,omitempty"`
	TopProvider         *struct {
		ContextLength       int  `json:"context_length,omitempty"`
		MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
	} `json:"top_provider,omitempty"`
}

func fetchOpenRouterModels(client *http.Client, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
	req.Header.Set("X-OpenRouter-Title", "izen")
	req.Header.Set("X-OpenRouter-Categories", "agent-runtime")
	req.Header.Set("X-OpenRouter-Description", "AI amplifies human judgment. Humans remain in control.")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result openRouterResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		supports := detectReasoningFromParameters(m.SupportedParameters, m.Architecture)
		// Fallback to heuristic if API doesn't advertise reasoning
		if supports == nil {
			heuristic := ModelSupportsEffortWithProvider("openrouter", m.ID)
			supports = &heuristic
		}
		// Accurate schema mapping:
		// ContextWindow from model.context_length (top-level), NOT from max_completion_tokens.
		// MaxOutputTokens from top_provider.max_completion_tokens.
		ctxWin := m.ContextLength
		if ctxWin == 0 && m.TopProvider != nil && m.TopProvider.ContextLength != 0 {
			ctxWin = m.TopProvider.ContextLength
		}
		if ctxWin == 0 {
			ctxWin = ContextWindowFor(m.ID)
		}
		// DeepSeek V3/R1 family: context window is 128k (128000), not 64k.
		// Correct legacy 64k fallback for deepseek/*.
		lowerID := strings.ToLower(m.ID)
		if strings.Contains(lowerID, "deepseek") {
			if ctxWin == 64000 || ctxWin == 65536 {
				ctxWin = 128000
			}
			// Ensure deepseek-v3/v4/chat/r1 get 128k even when heuristic returns 0 (should not, but safety)
			if ctxWin == 0 {
				ctxWin = 128000
			}
		}
		maxOut := 0
		if m.TopProvider != nil && m.TopProvider.MaxCompletionTokens != nil {
			maxOut = *m.TopProvider.MaxCompletionTokens
		}
		if maxOut == 0 {
			maxOut = heuristicMaxOutputTokens("openrouter", m.ID)
		}
		// Fallback for DeepSeek V3/R1 max output: 65536 (64k)
		if maxOut == 0 && strings.Contains(lowerID, "deepseek") {
			maxOut = 65536
		}
		models = append(models, ModelInfo{
			ID:                  m.ID,
			Name:                name,
			Provider:            "openrouter",
			SupportsReasoning:   supports,
			ContextWindow:       ctxWin,
			MaxOutputTokens:     maxOut,
			SupportedParameters: m.SupportedParameters,
			Architecture:        m.Architecture,
		})
	}

	return models, nil
}

func detectReasoningFromParameters(params []string, arch *Architecture) *bool {
	if len(params) == 0 && arch == nil {
		return nil
	}
	for _, p := range params {
		lower := strings.ToLower(p)
		if lower == "reasoning" || lower == "reasoning_effort" || lower == "thinking" || lower == "include_reasoning" {
			t := true
			return &t
		}
	}
	// Architecture modality check is not sufficient alone; return nil to fallback to heuristic
	return nil
}

// ── Cache (24h TTL, ~/.cache/izen/models_schema.json) ───────────────────────

type cachedSchema struct {
	Timestamp time.Time   `json:"timestamp"`
	Models    []ModelInfo `json:"models"`
}

func cacheFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "izen_models_schema.json")
	}
	return filepath.Join(home, ".cache", "izen", "models_schema.json")
}

func saveCachedSchema(models []ModelInfo) error {
	path := cacheFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cachedSchema{Timestamp: time.Now(), Models: models})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func loadCachedIfFresh(ttl time.Duration) ([]ModelInfo, bool) {
	path := cacheFilePath()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var c cachedSchema
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, false
	}
	if time.Since(c.Timestamp) > ttl {
		return nil, false
	}
	return c.Models, true
}

// ── Override (TOML, ~/.config/izen/models_override.toml) ─────────────────────

type modelOverride struct {
	SupportsReasoning *bool `json:"supports_reasoning"`
	ContextWindow     *int  `json:"context_window"`
	MaxOutputTokens   *int  `json:"max_output_tokens"`
}

func overrideFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "izen", "models_override.toml")
}

func loadOverrides() map[string]modelOverride {
	path := overrideFilePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseOverrideTOML(string(b))
}

// parseOverrideTOML parses a minimal TOML subset for model overrides:
// [models."custom/my-r1-model"]
// supports_reasoning = true
// context_window = 131072
func parseOverrideTOML(content string) map[string]modelOverride {
	result := make(map[string]modelOverride)
	lines := strings.Split(content, "\n")
	var currentKey string
	var current modelOverride
	flush := func() {
		if currentKey != "" {
			// copy to avoid alias
			cp := current
			result[currentKey] = cp
		}
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Section header: [models."id"] or [models.'id'] or [models.id]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			inner := strings.TrimSpace(line[1 : len(line)-1])
			// Expect models."id"
			if strings.HasPrefix(inner, "models.") {
				keyPart := strings.TrimSpace(inner[len("models."):])
				// Remove surrounding quotes if present
				keyPart = strings.Trim(keyPart, "\"'")
				currentKey = keyPart
				current = modelOverride{}
			} else {
				currentKey = ""
			}
			continue
		}
		if currentKey == "" {
			continue
		}
		// key = value
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		// Remove comments after value
		if idx := strings.Index(v, "#"); idx >= 0 {
			v = strings.TrimSpace(v[:idx])
		}
		v = strings.Trim(v, "\"'")
		switch strings.ToLower(k) {
		case "supports_reasoning":
			b := strings.EqualFold(v, "true")
			if strings.EqualFold(v, "false") {
				b = false
			} else if !strings.EqualFold(v, "true") {
				continue
			}
			bb := b
			current.SupportsReasoning = &bb
		case "context_window", "context_length":
			var iv int
			_, _ = fmt.Sscanf(v, "%d", &iv)
			if iv > 0 {
				iv2 := iv
				current.ContextWindow = &iv2
			}
		case "max_output_tokens", "max_tokens":
			var iv int
			_, _ = fmt.Sscanf(v, "%d", &iv)
			if iv > 0 {
				iv2 := iv
				current.MaxOutputTokens = &iv2
			}
		}
	}
	flush()
	return result
}

func applyOverrides(models []ModelInfo, overrides map[string]modelOverride) []ModelInfo {
	if len(overrides) == 0 {
		return models
	}
	// Index overrides for quick lookup; also try bare ID match
	for i, m := range models {
		// Try full provider/id, then bare id, then quoted forms
		keys := []string{m.ID, m.Provider + "/" + m.ID, m.Provider + ":" + m.ID}
		// For openrouter models, ID already contains provider prefix like openai/o1
		// Override file uses [models."custom/my-r1-model"] without provider prefix split
		for _, k := range keys {
			if ov, ok := overrides[k]; ok {
				if ov.SupportsReasoning != nil {
					models[i].SupportsReasoning = ov.SupportsReasoning
				}
				if ov.ContextWindow != nil {
					models[i].ContextWindow = *ov.ContextWindow
				}
				if ov.MaxOutputTokens != nil {
					models[i].MaxOutputTokens = *ov.MaxOutputTokens
				}
				break
			}
		}
		// Also check bare ID without provider for cases where override uses full openrouter ID
		if ov, ok := overrides[m.ID]; ok {
			if ov.SupportsReasoning != nil {
				models[i].SupportsReasoning = ov.SupportsReasoning
			}
			if ov.ContextWindow != nil {
				models[i].ContextWindow = *ov.ContextWindow
			}
			if ov.MaxOutputTokens != nil {
				models[i].MaxOutputTokens = *ov.MaxOutputTokens
			}
		}
	}
	// Also inject override-only models that aren't in the API list (custom models)
	for key, ov := range overrides {
		found := false
		for _, m := range models {
			if m.ID == key {
				found = true
				break
			}
		}
		if !found {
			// Custom model: infer provider from key prefix before "/"
			prov := "custom"
			id := key
			if idx := strings.Index(key, "/"); idx >= 0 {
				prov = key[:idx]
				id = key
			}
			sup := ov.SupportsReasoning
			ctxWin := 0
			if ov.ContextWindow != nil {
				ctxWin = *ov.ContextWindow
			}
			maxOut := 0
			if ov.MaxOutputTokens != nil {
				maxOut = *ov.MaxOutputTokens
			}
			models = append(models, ModelInfo{
				ID:                id,
				Name:              id,
				Provider:          prov,
				SupportsReasoning: sup,
				ContextWindow:     ctxWin,
				MaxOutputTokens:   maxOut,
			})
		}
	}
	return models
}

func enrichWithHeuristics(models []ModelInfo) []ModelInfo {
	for i, m := range models {
		if m.SupportsReasoning == nil {
			h := ModelSupportsEffortWithProvider(m.Provider, m.ID)
			models[i].SupportsReasoning = &h
		}
		if m.ContextWindow == 0 {
			models[i].ContextWindow = ContextWindowFor(m.ID)
		}
		if m.MaxOutputTokens == 0 {
			models[i].MaxOutputTokens = heuristicMaxOutputTokens(m.Provider, m.ID)
		}
	}
	return models
}

func heuristicMaxOutputTokens(provider, modelID string) int {
	// Heuristic fallback for max output tokens
	lower := strings.ToLower(modelID)
	if strings.Contains(lower, "claude-3-7-sonnet") || strings.Contains(lower, "claude-sonnet-4") || strings.Contains(lower, "claude-opus-4") {
		return 64000
	}
	if strings.Contains(lower, "o1") || strings.Contains(lower, "o3") {
		return 32000
	}
	if strings.Contains(lower, "deepseek") {
		return 65536
	}
	if strings.Contains(lower, "gemini") {
		return 8192
	}
	if provider == "openrouter" {
		// For openrouter, check embedded vendor
		if strings.HasPrefix(lower, "openai/o1") || strings.HasPrefix(lower, "openai/o3") {
			return 32000
		}
		if strings.HasPrefix(lower, "anthropic/claude") {
			return 64000
		}
	}
	return 4096
}

func heuristicFallbackModels() []ModelInfo {
	// Combine static catalogs as heuristic fallback
	var all []ModelInfo
	if m, _ := fetchAnthropicModels(); m != nil {
		all = append(all, m...)
	}
	if m, _ := fetchOpenAIModels(); m != nil {
		all = append(all, m...)
	}
	all = append(all, ollamaFallbackModels()...)
	// Enrich with heuristics for fallback models
	all = enrichWithHeuristics(all)
	// Deduplicate? Keep as is, sorting will be done by caller
	return all
}

type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		ModifiedAt string `json:"modified_at,omitempty"`
		Size       int64  `json:"size,omitempty"`
	} `json:"models"`
}

func fetchOllamaModels(client *http.Client) ([]ModelInfo, error) {
	shortClient := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost:11434/api/tags", nil)
	if err == nil {
		resp, err := shortClient.Do(req)
		if err == nil {
			defer func() { _ = resp.Body.Close() }()
			body, readErr := io.ReadAll(resp.Body)
			if readErr == nil {
				var result ollamaTagsResponse
				if json.Unmarshal(body, &result) == nil {
					models := make([]ModelInfo, 0, len(result.Models))
					for _, m := range result.Models {
						sup := ModelSupportsEffortWithProvider("ollama", m.Name)
						models = append(models, ModelInfo{
							ID:                m.Name,
							Name:              m.Name,
							Provider:          "ollama",
							SupportsReasoning: &sup,
							ContextWindow:     ContextWindowFor(m.Name),
							MaxOutputTokens:   heuristicMaxOutputTokens("ollama", m.Name),
						})
					}
					return models, nil
				}
			}
		}
	}

	return fetchOllamaModelsCLI()
}

func fetchOllamaModelsCLI() ([]ModelInfo, error) {
	ollamaPath, err := resolveOllamaBinary()
	if err != nil {
		return ollamaFallbackModels(), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ollamaPath, "list")
	output, err := cmd.Output()
	if err != nil {
		return ollamaFallbackModels(), nil
	}

	models, err := parseOllamaListOutput(string(output))
	if err != nil || len(models) == 0 {
		return ollamaFallbackModels(), nil
	}
	// Enrich CLI parsed models
	for i := range models {
		sup := ModelSupportsEffortWithProvider(models[i].Provider, models[i].ID)
		models[i].SupportsReasoning = &sup
		models[i].ContextWindow = ContextWindowFor(models[i].ID)
		models[i].MaxOutputTokens = heuristicMaxOutputTokens(models[i].Provider, models[i].ID)
	}
	return models, nil
}

// resolveOllamaBinary finds the ollama binary, searching common installation
// paths when the system PATH is insufficient (e.g., minimal PATH from TUI launch).
func resolveOllamaBinary() (string, error) {
	if p, err := exec.LookPath("ollama"); err == nil {
		return p, nil
	}

	candidates := []string{
		"/usr/local/bin/ollama",
		"/opt/homebrew/bin/ollama",
	}
	if home, err := getUserHomeDir(); err == nil {
		candidates = append(candidates, home+"/.ollama/bin/ollama")
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	return "", fmt.Errorf("ollama binary not found")
}

func getUserHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

// ollamaFallbackModels returns a minimal list of common Ollama models so
// the model picker is never empty when detection fails.
func ollamaFallbackModels() []ModelInfo {
	fallback := []ModelInfo{
		{ID: "qwen2.5-coder:7b", Name: "qwen2.5-coder:7b", Provider: "ollama"},
		{ID: "llama3.2:3b", Name: "llama3.2:3b", Provider: "ollama"},
		{ID: "llama3.1:8b", Name: "llama3.1:8b", Provider: "ollama"},
		{ID: "mistral:7b", Name: "mistral:7b", Provider: "ollama"},
	}
	for i := range fallback {
		sup := ModelSupportsEffortWithProvider(fallback[i].Provider, fallback[i].ID)
		fallback[i].SupportsReasoning = &sup
		fallback[i].ContextWindow = ContextWindowFor(fallback[i].ID)
		fallback[i].MaxOutputTokens = heuristicMaxOutputTokens(fallback[i].Provider, fallback[i].ID)
	}
	return fallback
}

// parseOllamaListOutput parses the tabular output from `ollama list`.
func parseOllamaListOutput(output string) ([]ModelInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return []ModelInfo{}, nil
	}

	var models []ModelInfo
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		models = append(models, ModelInfo{
			ID:       name,
			Name:     name,
			Provider: "ollama",
		})
	}

	return models, nil
}

func fetchAnthropicModels() ([]ModelInfo, error) {
	staticModels := []string{
		"claude-sonnet-4-20250514",
		"claude-4-20250514",
		"claude-opus-4-20250514",
		"claude-3-5-sonnet-20241022",
		"claude-3-5-haiku-20241022",
		"claude-3-opus-20240229",
		"claude-3-sonnet-20240229",
		"claude-3-haiku-20240307",
	}

	models := make([]ModelInfo, len(staticModels))
	for i, id := range staticModels {
		sup := ModelSupportsEffortWithProvider("anthropic", id)
		models[i] = ModelInfo{
			ID:                id,
			Name:              id,
			Provider:          "anthropic",
			SupportsReasoning: &sup,
			ContextWindow:     ContextWindowFor(id),
			MaxOutputTokens:   heuristicMaxOutputTokens("anthropic", id),
		}
	}
	return models, nil
}

func fetchOpenAIModels() ([]ModelInfo, error) {
	staticModels := []string{
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-4-turbo",
		"gpt-4",
		"gpt-3.5-turbo",
		"o1",
		"o1-mini",
		"o3-mini",
	}

	models := make([]ModelInfo, len(staticModels))
	for i, id := range staticModels {
		sup := ModelSupportsEffortWithProvider("openai", id)
		models[i] = ModelInfo{
			ID:                id,
			Name:              id,
			Provider:          "openai",
			SupportsReasoning: &sup,
			ContextWindow:     ContextWindowFor(id),
			MaxOutputTokens:   heuristicMaxOutputTokens("openai", id),
		}
	}
	return models, nil
}

func FilterModels(models []ModelInfo, query string) []ModelInfo {
	if query == "" {
		return models
	}

	lower := strings.ToLower(query)
	var results []ModelInfo
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) {
			results = append(results, m)
		} else if strings.Contains(strings.ToLower(m.Provider), lower) {
			results = append(results, m)
		}
	}

	if len(results) > 50 {
		results = results[:50]
	}

	return results
}
