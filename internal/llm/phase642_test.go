package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOllamaClientRejectsNamespacedModel pins the Provider Routing Isolation
// Invariant at the llm client layer: vendor-prefixed IDs must fail fast with
// no HTTP attempt against the local endpoint.
func TestOllamaClientRejectsNamespacedModel(t *testing.T) {
	for _, model := range []string{
		"thinkingmachines/inkling",
		"nvidia/nemotron-reasoning",
	} {
		c := NewOllamaClient("http://127.0.0.1:1/v1", "", model)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := c.GenerateResponse(ctx, PromptRequest{Model: model}); !errors.Is(err, ErrOllamaNamespacedModel) {
			t.Errorf("GenerateResponse(%q) = %v, want ErrOllamaNamespacedModel", model, err)
		}
		if _, err := c.StreamResponse(ctx, PromptRequest{Model: model}, nil); !errors.Is(err, ErrOllamaNamespacedModel) {
			t.Errorf("StreamResponse(%q) = %v, want ErrOllamaNamespacedModel", model, err)
		}
		cancel()
	}
}

// TestOllamaStreamResponseReasoningFallback pins the Reasoning Content
// Fallback Invariant: a reasoning-only stream (empty content, thinking text
// present) must synthesize content from reasoning instead of returning empty.
func TestOllamaStreamResponseReasoningFallback(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"reasoning_content\":\"fallback plan text\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sse)
	}))
	defer srv.Close()
	c := NewOllamaClient(srv.URL, "", "llama3.2:3b")

	resp, err := c.StreamResponse(context.Background(), PromptRequest{
		Model:    "llama3.2:3b",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if resp.Content != "fallback plan text" {
		t.Errorf("Content = %q, want reasoning fallback %q", resp.Content, "fallback plan text")
	}
}
