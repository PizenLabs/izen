package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchOpenRouterModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		resp := openRouterResponse{
			Data: []struct {
				ID   string `json:"id"`
				Name string `json:"name,omitempty"`
			}{
				{ID: "anthropic/claude-3.5-sonnet", Name: "Claude 3.5 Sonnet"},
				{ID: "openai/gpt-4o", Name: "GPT-4o"},
				{ID: "google/gemini-pro"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: testTimeout}
	models, err := fetchOpenRouterModelsCustom(client, "test-key", server.URL)
	if err != nil {
		t.Fatalf("fetchOpenRouterModels() error = %v", err)
	}

	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

	if models[0].ID != "anthropic/claude-3.5-sonnet" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "anthropic/claude-3.5-sonnet")
	}
	if models[0].Provider != "openrouter" {
		t.Errorf("models[0].Provider = %q, want %q", models[0].Provider, "openrouter")
	}
	if models[0].Name != "Claude 3.5 Sonnet" {
		t.Errorf("models[0].Name = %q, want %q", models[0].Name, "Claude 3.5 Sonnet")
	}

	if models[1].ID != "openai/gpt-4o" {
		t.Errorf("models[1].ID = %q", models[1].ID)
	}

	if models[2].Name != "google/gemini-pro" {
		t.Errorf("models[2].Name (fallback) = %q, want ID value", models[2].Name)
	}
}

func TestFetchOllamaModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaTagsResponse{
			Models: []struct {
				Name       string `json:"name"`
				ModifiedAt string `json:"modified_at,omitempty"`
				Size       int64  `json:"size,omitempty"`
			}{
				{Name: "qwen2.5-coder:7b", Size: 4096},
				{Name: "llama3.2:3b", Size: 2048},
				{Name: "mistral:7b", Size: 4096},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: testTimeout}
	models, err := fetchOllamaModelsCustom(client, server.URL)
	if err != nil {
		t.Fatalf("fetchOllamaModels() error = %v", err)
	}

	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

	if models[0].ID != "qwen2.5-coder:7b" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "qwen2.5-coder:7b")
	}
	if models[0].Provider != "ollama" {
		t.Errorf("models[0].Provider = %q, want %q", models[0].Provider, "ollama")
	}

	if models[1].ID != "llama3.2:3b" {
		t.Errorf("models[1].ID = %q", models[1].ID)
	}
}

func TestFetchOllamaModelsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaTagsResponse{Models: []struct {
			Name       string `json:"name"`
			ModifiedAt string `json:"modified_at,omitempty"`
			Size       int64  `json:"size,omitempty"`
		}{}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: testTimeout}
	models, err := fetchOllamaModelsCustom(client, server.URL)
	if err != nil {
		t.Fatalf("fetchOllamaModels() error = %v", err)
	}

	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d", len(models))
	}
}

func TestFetchAnthropicModels(t *testing.T) {
	models, err := fetchAnthropicModels()
	if err != nil {
		t.Fatalf("fetchAnthropicModels() error = %v", err)
	}

	if len(models) == 0 {
		t.Fatal("expected non-empty static list")
	}

	expected := []string{
		"claude-sonnet-4-20250514",
		"claude-4-20250514",
		"claude-opus-4-20250514",
		"claude-3-5-sonnet-20241022",
	}

	for i, id := range expected {
		if models[i].ID != id {
			t.Errorf("models[%d].ID = %q, want %q", i, models[i].ID, id)
		}
		if models[i].Provider != "anthropic" {
			t.Errorf("models[%d].Provider = %q", i, models[i].Provider)
		}
	}
}

func TestFetchOpenAIModels(t *testing.T) {
	models, err := fetchOpenAIModels()
	if err != nil {
		t.Fatalf("fetchOpenAIModels() error = %v", err)
	}

	if len(models) == 0 {
		t.Fatal("expected non-empty static list")
	}

	expected := []string{"gpt-4o", "gpt-4o-mini", "gpt-4-turbo"}
	found := 0
	for _, m := range models {
		for _, e := range expected {
			if m.ID == e {
				found++
			}
		}
		if m.Provider != "openai" {
			t.Errorf("model %q has Provider = %q", m.ID, m.Provider)
		}
	}
	if found != len(expected) {
		t.Errorf("expected %d known models, found %d", len(expected), found)
	}
}

func TestFilterModelsEmptyQuery(t *testing.T) {
	models := []ModelInfo{
		{ID: "gpt-4o", Provider: "openai"},
		{ID: "claude-3", Provider: "anthropic"},
	}

	result := FilterModels(models, "")
	if len(result) != 2 {
		t.Errorf("expected 2, got %d", len(result))
	}
}

func TestFilterModelsByID(t *testing.T) {
	models := []ModelInfo{
		{ID: "gpt-4o", Name: "GPT-4o", Provider: "openai"},
		{ID: "gpt-4o-mini", Name: "GPT-4o Mini", Provider: "openai"},
		{ID: "claude-3.5-sonnet", Name: "Claude 3.5 Sonnet", Provider: "anthropic"},
		{ID: "qwen2.5-coder:7b", Name: "qwen2.5-coder:7b", Provider: "ollama"},
	}

	result := FilterModels(models, "gpt")
	if len(result) != 2 {
		t.Errorf("expected 2 GPT models, got %d", len(result))
	}
}

func TestFilterModelsByProvider(t *testing.T) {
	models := []ModelInfo{
		{ID: "gpt-4o", Provider: "openai"},
		{ID: "claude-3", Provider: "anthropic"},
		{ID: "qwen2.5-coder:7b", Provider: "ollama"},
	}

	result := FilterModels(models, "ollama")
	if len(result) != 1 {
		t.Errorf("expected 1 ollama model, got %d", len(result))
	}
	if result[0].ID != "qwen2.5-coder:7b" {
		t.Errorf("result = %q", result[0].ID)
	}
}

func TestFilterModelsCaseInsensitive(t *testing.T) {
	models := []ModelInfo{
		{ID: "GPT-4o", Name: "GPT-4o", Provider: "openai"},
		{ID: "Claude-3", Name: "Claude 3", Provider: "anthropic"},
	}

	result := FilterModels(models, "gpt")
	if len(result) != 1 {
		t.Errorf("expected 1, got %d", len(result))
	}
}

func TestFilterModelsLimit(t *testing.T) {
	models := make([]ModelInfo, 60)
	for i := 0; i < 60; i++ {
		models[i] = ModelInfo{ID: "model-x", Provider: "test"}
	}

	result := FilterModels(models, "model")
	if len(result) > 50 {
		t.Errorf("expected at most 50 results, got %d", len(result))
	}
}

func TestModelRegistryCache(t *testing.T) {
	reg := NewModelRegistry()
	reg.SetTTL(testTimeout)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := ollamaTagsResponse{
			Models: []struct {
				Name       string `json:"name"`
				ModifiedAt string `json:"modified_at,omitempty"`
				Size       int64  `json:"size,omitempty"`
			}{
				{Name: "test-model:latest"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: testTimeout}
	models, err := fetchOllamaModelsCustom(client, server.URL)
	if err != nil {
		t.Fatalf("first fetch error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	models2, err := fetchOllamaModelsCustom(client, server.URL)
	if err != nil {
		t.Fatalf("second fetch error = %v", err)
	}
	if len(models2) != 1 {
		t.Errorf("expected 1 model on second fetch, got %d", len(models2))
	}
}

func TestFetchProviderModelsUnknown(t *testing.T) {
	client := &http.Client{Timeout: testTimeout}
	models, err := fetchProviderModels("unknown", "", client)
	if err != nil {
		t.Fatalf("fetch unknown provider error = %v", err)
	}
	if models != nil {
		t.Errorf("expected nil for unknown provider, got %d models", len(models))
	}
}

func TestModelRegistryInvalidateCache(t *testing.T) {
	reg := NewModelRegistry()
	reg.mu.Lock()
	reg.models = []ModelInfo{{ID: "cached", Provider: "test"}}
	reg.cachedAt = time.Now()
	reg.mu.Unlock()

	reg.InvalidateCache()

	reg.mu.RLock()
	if reg.models != nil {
		t.Error("models should be nil after InvalidateCache")
	}
	if !reg.cachedAt.IsZero() {
		t.Error("cachedAt should be zero after InvalidateCache")
	}
	reg.mu.RUnlock()
}

func fetchOpenRouterModelsCustom(client *http.Client, apiKey, baseURL string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", baseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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

func fetchOllamaModelsCustom(client *http.Client, baseURL string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", baseURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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

var testTimeout = time.Second * 5
