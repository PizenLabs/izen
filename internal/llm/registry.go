package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type ModelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Provider string `json:"provider"`
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
	errCh := make(chan error, 4)

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

	// Wait for all fetches with a 3-second overall timeout so the model
	// picker never hangs on unreachable remote providers. Goroutines that
	// complete after the timeout are discarded.
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

type openRouterResponse struct {
	Data []struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
	} `json:"data"`
}

func fetchOpenRouterModels(client *http.Client, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

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
		models = append(models, ModelInfo{
			ID:       m.ID,
			Name:     name,
			Provider: "openrouter",
		})
	}

	return models, nil
}

type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		ModifiedAt string `json:"modified_at,omitempty"`
		Size       int64  `json:"size,omitempty"`
	} `json:"models"`
}

func fetchOllamaModels(client *http.Client) ([]ModelInfo, error) {
	// Step 1: try HTTP API with a fast 3-second timeout.
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
						models = append(models, ModelInfo{
							ID:       m.Name,
							Name:     m.Name,
							Provider: "ollama",
						})
					}
					return models, nil
				}
			}
		}
	}

	// Step 2 (fallback): ollama list CLI.
	return fetchOllamaModelsCLI()
}

func fetchOllamaModelsCLI() ([]ModelInfo, error) {
	ollamaPath, err := resolveOllamaBinary()
	if err != nil {
		// Neither PATH nor common locations contain ollama.
		// Return a fallback list so the model picker is never empty.
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
	return models, nil
}

// resolveOllamaBinary finds the ollama binary, searching common installation
// paths when the system PATH is insufficient (e.g., minimal PATH from TUI launch).
func resolveOllamaBinary() (string, error) {
	// Step 1: try exec.LookPath (checks $PATH).
	if p, err := exec.LookPath("ollama"); err == nil {
		return p, nil
	}

	// Step 2: check common installation locations.
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
	return []ModelInfo{
		{ID: "qwen2.5-coder:7b", Name: "qwen2.5-coder:7b", Provider: "ollama"},
		{ID: "llama3.2:3b", Name: "llama3.2:3b", Provider: "ollama"},
		{ID: "llama3.1:8b", Name: "llama3.1:8b", Provider: "ollama"},
		{ID: "mistral:7b", Name: "mistral:7b", Provider: "ollama"},
	}
}

// parseOllamaListOutput parses the tabular output from `ollama list`.
// Expected format (header + rows):
//
//	NAME                    ID                   SIZE      MODIFIED
//	qwen2.5-coder:7b        3a8f7c0e1b2c        4.1 GB    2 days ago
//	llama3.2:3b             a1b2c3d4e5f6        2.0 GB    5 days ago
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
		models[i] = ModelInfo{
			ID:       id,
			Name:     id,
			Provider: "anthropic",
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
		models[i] = ModelInfo{
			ID:       id,
			Name:     id,
			Provider: "openai",
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
		}
		if strings.Contains(strings.ToLower(m.Provider), lower) {
			results = append(results, m)
		}
	}

	if len(results) > 50 {
		results = results[:50]
	}

	return results
}
