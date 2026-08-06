package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

const cannedCompletion = `{"id":"1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

// capturingTransport intercepts every HTTP request and records its body, then
// returns a canned chat-completion response. It lets the test drive the full
// Execute path (payload building + request marshal) without a real API — the
// OpenAI and Claude providers hardcode their API base URLs, so an httptest
// server alone cannot intercept them.
type capturingTransport struct {
	body *[]byte
}

func (ct *capturingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	*ct.body = b
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(cannedCompletion)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// captureClient returns an http.Client whose transport records request bodies,
// plus a getter for the last captured body.
func captureClient(t *testing.T) (*http.Client, func() []byte) {
	t.Helper()
	var body []byte
	return &http.Client{Transport: &capturingTransport{body: &body}}, func() []byte { return body }
}

func decodeBody(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var req map[string]interface{}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v (payload %s)", err, raw)
	}
	return req
}

func TestOpenAIReasoningEffortInjected(t *testing.T) {
	client, body := captureClient(t)
	p := NewOpenAIProvider("key", "o3")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{Reasoning: &ai.ReasoningConfig{Level: "xhigh"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := decodeBody(t, body())["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh (payload %s)", got, body())
	}
}

func TestOpenAIReasoningEffortOmittedWhenNil(t *testing.T) {
	client, body := captureClient(t)
	p := NewOpenAIProvider("key", "gpt-4o")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := decodeBody(t, body())["reasoning_effort"]; ok {
		t.Fatalf("reasoning_effort should be omitted when Reasoning is nil (payload %s)", body())
	}
}

func TestClaudeThinkingBudgetInjected(t *testing.T) {
	client, body := captureClient(t)
	p := NewClaudeProvider("key", "claude-3.7-sonnet")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{Reasoning: &ai.ReasoningConfig{BudgetTokens: 8192}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	thinking, ok := decodeBody(t, body())["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("thinking not injected (payload %s)", body())
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}
	if int(thinking["budget_tokens"].(float64)) != 8192 {
		t.Errorf("thinking.budget_tokens = %v, want 8192", thinking["budget_tokens"])
	}
}

func TestClaudeThinkingOmittedWhenNil(t *testing.T) {
	client, body := captureClient(t)
	p := NewClaudeProvider("key", "claude-3.7-sonnet")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := decodeBody(t, body())["thinking"]; ok {
		t.Fatalf("thinking should be omitted when Reasoning is nil (payload %s)", body())
	}
}

func TestOpenRouterReasoningEffortInjected(t *testing.T) {
	client, body := captureClient(t)
	p := NewOpenRouterProvider("key", "openai/o3", "https://openrouter.example.com/api/v1")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{Reasoning: &ai.ReasoningConfig{Level: "high"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	reasoning, ok := decodeBody(t, body())["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("reasoning not injected (payload %s)", body())
	}
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, want high", reasoning["effort"])
	}
}

func TestOpenRouterReasoningCoTLimitInjected(t *testing.T) {
	client, body := captureClient(t)
	p := NewOpenRouterProvider("key", "cohere/north-mini-code", "https://openrouter.example.com/api/v1")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{Reasoning: &ai.ReasoningConfig{CoTLimit: 512}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	reasoning, ok := decodeBody(t, body())["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("reasoning not injected (payload %s)", body())
	}
	if int(reasoning["max_tokens"].(float64)) != 512 {
		t.Errorf("reasoning.max_tokens = %v, want 512", reasoning["max_tokens"])
	}
}

func TestOpenRouterReasoningOmittedWhenNil(t *testing.T) {
	client, body := captureClient(t)
	p := NewOpenRouterProvider("key", "openai/o3", "https://openrouter.example.com/api/v1")
	p.client = client
	_, err := p.Execute(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := decodeBody(t, body())["reasoning"]; ok {
		t.Fatalf("reasoning should be omitted when Reasoning is nil (payload %s)", body())
	}
}

func TestReasoningForCombinesLevelAndBudget(t *testing.T) {
	r := reasoningFor(ai.Request{Reasoning: &ai.ReasoningConfig{Level: "high", BudgetTokens: 8192}})
	if r == nil {
		t.Fatal("reasoningFor should not return nil")
	}
	if r.Effort != "high" || r.MaxTokens != 8192 {
		t.Errorf("reasoningFor = %+v, want effort=high max_tokens=8192", r)
	}
}

func TestReasoningForCoTPriority(t *testing.T) {
	r := reasoningFor(ai.Request{Reasoning: &ai.ReasoningConfig{CoTLimit: 512, BudgetTokens: 8192}})
	if r == nil || r.MaxTokens != 512 {
		t.Errorf("CoTLimit must take priority over BudgetTokens, got %+v", r)
	}
}

func TestReasoningForNil(t *testing.T) {
	if r := reasoningFor(ai.Request{}); r != nil {
		t.Errorf("reasoningFor(nil) = %+v, want nil", r)
	}
}
