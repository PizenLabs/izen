package capability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterAdapterInspect(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "data": [
    {
      "id": "openai/gpt-4o",
      "name": "GPT-4o",
      "context_length": 128000,
      "supported_parameters": ["temperature", "top_p", "max_completion_tokens"],
      "top_provider": {"context_length": 128000, "max_completion_tokens": 16384}
    },
    {
      "id": "openai/o3-mini",
      "name": "O3 mini",
      "context_length": 200000,
      "supported_parameters": ["reasoning_effort", "max_completion_tokens"],
      "top_provider": {"max_completion_tokens": 100000}
    },
    {
      "id": "deepseek/deepseek-r1",
      "context_length": 0,
      "supported_parameters": []
    },
    {
      "id": "custom/model",
      "name": "Custom Model",
      "supported_parameters": ["temperature"]
    }
  ]
}`))
	}))
	defer server.Close()

	adapter := NewOpenRouterAdapterWithEndpoint("test-key", server.URL, server.Client())
	models, err := adapter.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect error: %v", err)
	}

	if len(models) != 4 {
		t.Fatalf("models len = %d, want 4", len(models))
	}

	byID := map[string]ModelCapabilities{}
	for _, m := range models {
		byID[m.ModelID] = m
	}

	t.Run("chat model parsed", func(t *testing.T) {
		gpt := byID["openai/gpt-4o"]
		if gpt.SupportsReasoning {
			t.Error("gpt-4o must not support reasoning")
		}
		if gpt.ContextWindow != 128000 {
			t.Errorf("context window = %d, want 128000", gpt.ContextWindow)
		}
		if gpt.MaxOutputTokens != 16384 {
			t.Errorf("max output = %d, want 16384", gpt.MaxOutputTokens)
		}
		if gpt.Name != "GPT-4o" {
			t.Errorf("name = %q", gpt.Name)
		}
		if len(gpt.SupportedEfforts) != 0 {
			t.Errorf("efforts = %v, want none", gpt.SupportedEfforts)
		}
	})

	t.Run("reasoning model from supported_parameters", func(t *testing.T) {
		o3 := byID["openai/o3-mini"]
		if !o3.SupportsReasoning {
			t.Fatal("o3-mini must support reasoning via supported_parameters")
		}
		want := []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh}
		if !equalEfforts(o3.SupportedEfforts, want) {
			t.Errorf("efforts = %v, want %v", o3.SupportedEfforts, want)
		}
		if o3.MaxOutputTokens != 100000 {
			t.Errorf("max output = %d, want 100000", o3.MaxOutputTokens)
		}
	})

	t.Run("heuristic fallback reasoning + heuristics", func(t *testing.T) {
		r1 := byID["deepseek/deepseek-r1"]
		if !r1.SupportsReasoning {
			t.Fatal("deepseek-r1 must support reasoning via heuristic fallback")
		}
		if !equalEfforts(r1.SupportedEfforts, []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh}) {
			t.Errorf("efforts = %v", r1.SupportedEfforts)
		}
		if r1.ContextWindow != 128000 {
			t.Errorf("context window = %d, want heuristic 128000", r1.ContextWindow)
		}
		if r1.MaxOutputTokens != 65536 {
			t.Errorf("max output = %d, want heuristic 65536", r1.MaxOutputTokens)
		}
	})

	t.Run("name preserved from catalog", func(t *testing.T) {
		if byID["custom/model"].Name != "Custom Model" {
			t.Errorf("name = %q, want catalog value", byID["custom/model"].Name)
		}
	})
}

func TestOpenRouterAdapterErrors(t *testing.T) {
	t.Parallel()

	t.Run("non-200 status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusUnauthorized)
		}))
		defer server.Close()
		adapter := NewOpenRouterAdapterWithEndpoint("k", server.URL, server.Client())
		if _, err := adapter.Inspect(context.Background()); err == nil {
			t.Fatal("expected error for non-200 status")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{not json`))
		}))
		defer server.Close()
		adapter := NewOpenRouterAdapterWithEndpoint("k", server.URL, server.Client())
		if _, err := adapter.Inspect(context.Background()); err == nil {
			t.Fatal("expected error for malformed json")
		}
	})

	t.Run("empty catalog yields empty result", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data": []}`))
		}))
		defer server.Close()
		adapter := NewOpenRouterAdapterWithEndpoint("k", server.URL, server.Client())
		models, err := adapter.Inspect(context.Background())
		if err != nil {
			t.Fatalf("Inspect error: %v", err)
		}
		if len(models) != 0 {
			t.Errorf("models len = %d, want 0", len(models))
		}
	})

	t.Run("nil adapter", func(t *testing.T) {
		var adapter *OpenRouterAdapter
		if _, err := adapter.Inspect(context.Background()); !errors.Is(err, errAdapterNil) {
			t.Fatalf("err = %v, want errAdapterNil", err)
		}
	})

	t.Run("network error", func(t *testing.T) {
		adapter := NewOpenRouterAdapterWithEndpoint("k", "http://127.0.0.1:1/models", &http.Client{})
		if _, err := adapter.Inspect(context.Background()); err == nil {
			t.Fatal("expected network error")
		}
	})
}

func TestOllamaAdapterInspect(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "models": [
    {"name": "llama3.1:8b"},
    {"name": "deepseek-r1:7b"},
    {"name": "qwen2.5-coder:7b"}
  ]
}`))
	}))
	defer server.Close()

	adapter := NewOllamaAdapterWithBaseURL(server.URL, server.Client())
	models, err := adapter.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect error: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("models len = %d, want 3", len(models))
	}

	byID := map[string]ModelCapabilities{}
	for _, m := range models {
		if m.Provider != "ollama" {
			t.Errorf("provider = %q, want ollama", m.Provider)
		}
		byID[m.ModelID] = m
	}

	if byID["llama3.1:8b"].SupportsReasoning {
		t.Error("llama3.1 must not support reasoning")
	}
	if !byID["deepseek-r1:7b"].SupportsReasoning {
		t.Error("deepseek-r1 must support reasoning via heuristic")
	}
	if len(byID["deepseek-r1:7b"].SupportedEfforts) != 5 {
		t.Errorf("deepseek-r1 efforts = %v, want extended set", byID["deepseek-r1:7b"].SupportedEfforts)
	}
	if byID["qwen2.5-coder:7b"].ContextWindow != 32768 {
		t.Errorf("qwen context window = %d, want 32768", byID["qwen2.5-coder:7b"].ContextWindow)
	}
}

func TestOllamaAdapterErrors(t *testing.T) {
	t.Parallel()

	t.Run("non-200 status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		adapter := NewOllamaAdapterWithBaseURL(server.URL, server.Client())
		if _, err := adapter.Inspect(context.Background()); err == nil {
			t.Fatal("expected error for non-200 status")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`oops`))
		}))
		defer server.Close()
		adapter := NewOllamaAdapterWithBaseURL(server.URL, server.Client())
		if _, err := adapter.Inspect(context.Background()); err == nil {
			t.Fatal("expected error for malformed json")
		}
	})

	t.Run("trailing slash normalized", func(t *testing.T) {
		var sawPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawPath = r.URL.Path
			_, _ = w.Write([]byte(`{"models": []}`))
		}))
		defer server.Close()
		adapter := NewOllamaAdapterWithBaseURL(server.URL+"/", server.Client())
		if _, err := adapter.Inspect(context.Background()); err != nil {
			t.Fatalf("Inspect error: %v", err)
		}
		if sawPath != "/api/tags" {
			t.Errorf("path = %q, want /api/tags (trailing slash stripped)", sawPath)
		}
	})

	t.Run("nil adapter", func(t *testing.T) {
		var adapter *OllamaAdapter
		if _, err := adapter.Inspect(context.Background()); !errors.Is(err, errAdapterNil) {
			t.Fatalf("err = %v, want errAdapterNil", err)
		}
	})
}

func TestAdaptersDefaults(t *testing.T) {
	t.Parallel()
	oa := NewOpenRouterAdapter("key")
	if oa.endpoint != "https://openrouter.ai/api/v1/models" {
		t.Errorf("openrouter default endpoint wrong: %q", oa.endpoint)
	}
	om := NewOllamaAdapter()
	if om.baseURL != "http://localhost:11434" {
		t.Errorf("ollama default base url wrong: %q", om.baseURL)
	}
	// nil clients must be replaced with defaults.
	oa2 := NewOpenRouterAdapterWithEndpoint("k", "", nil)
	if oa2.client == nil {
		t.Error("openrouter nil client must be defaulted")
	}
	om2 := NewOllamaAdapterWithBaseURL("", nil)
	if om2.client == nil {
		t.Error("ollama nil client must be defaulted")
	}
	if strings.TrimSuffix(oa2.endpoint, "/") != "https://openrouter.ai/api/v1/models" {
		t.Errorf("openrouter empty endpoint must default: %q", oa2.endpoint)
	}
}

func equalEfforts(a, b []EffortLevel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
