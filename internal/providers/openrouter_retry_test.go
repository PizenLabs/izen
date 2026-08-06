package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestOpenRouterReasoningOmittedForNonReasoningModel pins the sanitization rule:
// models on the openRouterNonReasoningModels denylist (e.g. gemma families that
// reject OpenRouter's reasoning schema with HTTP 400) must never receive a
// reasoning payload, even when the request carries a reasoning directive.
func TestOpenRouterReasoningOmittedForNonReasoningModel(t *testing.T) {
	cases := []string{
		"google/gemma-4-26b-a4b",
		"Google/Gemma-4-27B",
	}
	for _, model := range cases {
		client, body := captureClient(t)
		p := NewOpenRouterProvider("key", model, "https://openrouter.example.com/api/v1")
		p.client = client
		_, err := p.Execute(context.Background(), ai.Request{
			Model:     model,
			Reasoning: &ai.ReasoningConfig{Level: "high"},
		})
		if err != nil {
			t.Fatalf("%s: Execute: %v", model, err)
		}
		raw := decodeBody(t, body())
		if _, ok := raw["reasoning"]; ok {
			t.Fatalf("%s: reasoning injected into a non-reasoning model (payload %s)", model, body())
		}
	}
}

// TestOpenRouterModelSupportsReasoning asserts the denylist gating and that
// unknown/reasoning-capable models pass through.
func TestOpenRouterModelSupportsReasoning(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"google/gemma-4-26b-a4b", false},
		{"gemma", false},
		{"openai/o3", true},
		{"anthropic/claude-3.5-sonnet", true},
		{"cohere/north-mini-code", true},
		{"mistralai/mistral-7b-instruct", true},
	}
	for _, tc := range cases {
		if got := openRouterModelSupportsReasoning(tc.model); got != tc.want {
			t.Errorf("openRouterModelSupportsReasoning(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// reasoningRejectTransport answers HTTP 400 to the first request that carries a
// reasoning payload (mimicking an OpenRouter model that rejects the reasoning
// schema) and HTTP 200 to every subsequent request. It records each attempt's
// body so tests can assert the retry stripped the reasoning field.
type reasoningRejectTransport struct {
	mu       sync.Mutex
	attempts int
	bodies   [][]byte
}

func (t *reasoningRejectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	t.mu.Lock()
	t.bodies = append(t.bodies, body)
	t.attempts++
	attempt := t.attempts
	hasReasoning := strings.Contains(string(body), `"reasoning"`)
	t.mu.Unlock()

	if hasReasoning && attempt == 1 {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"reasoning not supported"}}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	}
	payload := cannedCompletion
	if r.Header.Get("Accept") == "text/event-stream" {
		payload = "data: [DONE]\n"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// TestOpenRouterExecute_RetriesOnceOn400ReasoningRejected drives the full
// Execute path: a reasoning-capable model whose gateway answers 400 while the
// reasoning payload is present must be retried exactly once with the reasoning
// field stripped, and the retry must succeed.
func TestOpenRouterExecute_RetriesOnceOn400ReasoningRejected(t *testing.T) {
	tr := &reasoningRejectTransport{}
	p := NewOpenRouterProvider("key", "openai/o3", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	resp, err := p.Execute(context.Background(), ai.Request{
		Model:     "openai/o3",
		Reasoning: &ai.ReasoningConfig{Level: "high"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want ok", resp.Content)
	}
	if tr.attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (initial with reasoning + retry stripped)", tr.attempts)
	}
	first := decodeBody(t, tr.bodies[0])
	if _, ok := first["reasoning"]; !ok {
		t.Fatalf("first attempt should carry reasoning (payload %s)", tr.bodies[0])
	}
	second := decodeBody(t, tr.bodies[1])
	if _, ok := second["reasoning"]; ok {
		t.Fatalf("retry should strip reasoning (payload %s)", tr.bodies[1])
	}
}

// TestOpenRouterExecuteStream_RetriesOnceOn400ReasoningRejected asserts the same
// fallback on the streaming path, which is the path the fast-track build uses.
func TestOpenRouterExecuteStream_RetriesOnceOn400ReasoningRejected(t *testing.T) {
	tr := &reasoningRejectTransport{}
	p := NewOpenRouterProvider("key", "openai/o3", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	rc, err := p.ExecuteStream(context.Background(), ai.Request{
		Model:     "openai/o3",
		Reasoning: &ai.ReasoningConfig{Level: "high"},
	})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if tr.attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (initial with reasoning + retry stripped)", tr.attempts)
	}
	second := decodeBody(t, tr.bodies[1])
	if _, ok := second["reasoning"]; ok {
		t.Fatalf("retry should strip reasoning (payload %s)", tr.bodies[1])
	}
}

// always400Transport answers HTTP 400 to every request and records the attempt
// bodies.
type always400Transport struct {
	mu       sync.Mutex
	attempts int
}

func (t *always400Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.attempts++
	t.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Status:     "400 Bad Request",
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad request"}}`)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// TestOpenRouterExecute_NoRetryOn400WithoutReasoning pins the guard: a request
// without a reasoning payload is never retried — the fallback exists solely for
// reasoning-schema rejection.
func TestOpenRouterExecute_NoRetryOn400WithoutReasoning(t *testing.T) {
	tr := &always400Transport{}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	if _, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"}); err == nil {
		t.Fatal("Execute should fail on a persistent 400")
	}
	if tr.attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no reasoning -> no retry)", tr.attempts)
	}
}

// TestOpenRouterExecute_DenylistModelSkipsReasoningRetry asserts that a
// denylisted model never reaches the retry path: its payload has no reasoning,
// so a 400 is returned once and surfaces immediately.
func TestOpenRouterExecute_DenylistModelSkipsReasoningRetry(t *testing.T) {
	tr := &always400Transport{}
	p := NewOpenRouterProvider("key", "google/gemma-4-26b-a4b", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	if _, err := p.Execute(context.Background(), ai.Request{
		Model:     "google/gemma-4-26b-a4b",
		Reasoning: &ai.ReasoningConfig{Level: "high"},
	}); err == nil {
		t.Fatal("Execute should fail on a persistent 400")
	}
	if tr.attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (denylist strips reasoning up front)", tr.attempts)
	}
}

// TestBuildRequestToolsCarriedOver guards the payload assembly: tool definitions
// survive the buildRequest refactor alongside reasoning sanitization.
func TestBuildRequestToolsCarriedOver(t *testing.T) {
	client, body := captureClient(t)
	p := NewOpenRouterProvider("key", "openai/o3", "https://openrouter.example.com/api/v1")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{
		Model:     "openai/o3",
		Reasoning: &ai.ReasoningConfig{Level: "high"},
		Tools: []ai.ToolDefinition{
			{Type: "function", Function: ai.ToolFunction{Name: "read", Description: "read a file", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	raw := decodeBody(t, body())
	if _, ok := raw["reasoning"]; !ok {
		t.Fatalf("reasoning missing (payload %s)", body())
	}
	if _, ok := raw["tools"]; !ok {
		t.Fatalf("tools missing (payload %s)", body())
	}
}
