package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

func TestSanitizeModelForOpenRouter(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		fallback string
		want     string
	}{
		{"valid vendor/model", "anthropic/claude-3.5-sonnet", "anthropic/claude-3.5-sonnet", "anthropic/claude-3.5-sonnet"},
		{"vendor with hyphen", "meta-llama/llama-3.3-70b-instruct", "anthropic/claude-3.5-sonnet", "meta-llama/llama-3.3-70b-instruct"},
		{"model with free variant", "mistralai/mistral-7b-instruct:free", "anthropic/claude-3.5-sonnet", "mistralai/mistral-7b-instruct:free"},
		{"ollama id maps to fallback", "qwen2.5-coder:7b", "anthropic/claude-3.5-sonnet", "anthropic/claude-3.5-sonnet"},
		{"bare id maps to fallback", "gpt-4o", "anthropic/claude-3.5-sonnet", "anthropic/claude-3.5-sonnet"},
		{"whitespace trimmed", "  anthropic/claude-3.5-sonnet ", "x/y", "anthropic/claude-3.5-sonnet"},
		{"empty maps to fallback", "", "anthropic/claude-3.5-sonnet", "anthropic/claude-3.5-sonnet"},
		{"both invalid empty", "", "", ""},
		{"invalid model and fallback empty", "qwen2.5-coder:7b", "qwen2.5-coder:14b", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeModelForOpenRouter(tc.model, tc.fallback); got != tc.want {
				t.Fatalf("SanitizeModelForOpenRouter(%q, %q) = %q, want %q", tc.model, tc.fallback, got, tc.want)
			}
		})
	}
}

// TestOpenRouterExecute_MapsInvalidModelID pins the reported HTTP 400 failure
// mode: an Ollama-style model ID ("qwen2.5-coder:7b") leaked into an OpenRouter
// request must be remapped to the provider default model before dispatch, so
// the API never rejects the payload with "not a valid model ID".
func TestOpenRouterExecute_MapsInvalidModelID(t *testing.T) {
	var (
		mu    sync.Mutex
		sent  string
		ready = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = body.Model
		mu.Unlock()
		close(ready)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "1", "object": "chat.completion", "model": body.Model,
			"choices": []map[string]interface{}{
				{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
			"usage": map[string]interface{}{"prompt_tokens": 5, "completion_tokens": 5},
		})
	}))
	defer srv.Close()

	p := NewOpenRouterProvider("test-key", "anthropic/claude-3.5-sonnet", srv.URL)
	resp, err := p.Execute(context.Background(), ai.Request{Model: "qwen2.5-coder:7b"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	<-ready
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want ok", resp.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if sent != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("dispatched model = %q, want provider default anthropic/claude-3.5-sonnet", sent)
	}
}

// TestOpenRouterExecute_PreservesValidModel ensures a compliant vendor/model ID
// passes through untouched.
func TestOpenRouterExecute_PreservesValidModel(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = body.Model
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "1", "object": "chat.completion", "model": body.Model,
			"choices": []map[string]interface{}{
				{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	p := NewOpenRouterProvider("test-key", "anthropic/claude-3.5-sonnet", srv.URL)
	if _, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sent != "openai/gpt-4o" {
		t.Fatalf("dispatched model = %q, want openai/gpt-4o", sent)
	}
}

// TestOpenRouterExecute_NoValidModel verifies a provider with no OpenRouter-
// valid model fails fast instead of dispatching a doomed request.
func TestOpenRouterExecute_NoValidModel(t *testing.T) {
	p := NewOpenRouterProvider("test-key", "qwen2.5-coder:7b", "https://openrouter.ai/api/v1")
	if _, err := p.Execute(context.Background(), ai.Request{Model: "llama3:8b"}); err == nil {
		t.Fatal("Execute with only invalid models should fail before dispatch")
	}
}
