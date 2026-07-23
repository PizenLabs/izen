package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	wg.Wait()
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
	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost:11434/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama not reachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result ollamaTagsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

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
