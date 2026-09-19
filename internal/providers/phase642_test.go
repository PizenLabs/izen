package providers

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestOpenRouterStreamResult_TrailingUsageAfterFinish pins the Phase 6.4.2
// Bounded Usage Drain (Telemetry Accuracy Invariant): a trailing usage-only
// SSE event (choices: []) that arrives AFTER the terminal finish_reason
// chunk MUST be captured into Usage() before the channel is torn down —
// never dropped by the instant finish_reason close.
func TestOpenRouterStreamResult_TrailingUsageAfterFinish(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"1","choices":[{"delta":{"content":"Hello"}}]}`,
		"",
		`data: {"id":"1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"1","choices":[],"usage":{"prompt_tokens":2860,"completion_tokens":2048,"total_tokens":4908}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(sse))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "Hello" {
		t.Fatalf("content = %q, want %q", got, "Hello")
	}
	u := res.Usage()
	if u.PromptTokens != 2860 || u.CompletionTokens != 2048 {
		t.Errorf("Usage() = (%d, %d), want (2860, 2048) — trailing usage chunk was dropped", u.PromptTokens, u.CompletionTokens)
	}
	if !u.Known {
		t.Error("Known = false, want true")
	}
	if u.TotalTokens != 4908 {
		t.Errorf("TotalTokens = %d, want 4908", u.TotalTokens)
	}
	if got := res.FinishReason(); got != "stop" {
		t.Errorf("FinishReason() = %q, want %q", got, "stop")
	}
}

// TestOpenRouterStreamResult_UsageOnlyChunkMidStream pins that usage-only
// chunks (len(choices) == 0 with usage) are recorded even when they arrive
// mid-stream, before any finish_reason.
func TestOpenRouterStreamResult_UsageOnlyChunkMidStream(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":0,"total_tokens":100}}`,
		"",
		`data: {"id":"1","choices":[{"delta":{"content":"hi"}}]}`,
		"",
		`data: {"id":"1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(sse))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "hi" {
		t.Fatalf("content = %q, want %q", got, "hi")
	}
	u := res.Usage()
	if u.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100 — mid-stream usage-only chunk was dropped", u.PromptTokens)
	}
}

// TestOpenAIStreamResult_TrailingUsageAfterFinish pins the same Bounded
// Usage Drain for the OpenAI-compatible reader.
func TestOpenAIStreamResult_TrailingUsageAfterFinish(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"1","choices":[{"delta":{"content":"hi"}}]}`,
		"",
		`data: {"id":"1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"1","choices":[],"usage":{"prompt_tokens":64,"completion_tokens":32,"total_tokens":96}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	sr := &openaiSSEReader{body: io.NopCloser(strings.NewReader(sse))}
	res := &OpenAIStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "hi" {
		t.Fatalf("content = %q, want %q", got, "hi")
	}
	u := res.Usage()
	if u.PromptTokens != 64 || u.CompletionTokens != 32 {
		t.Errorf("Usage() = (%d, %d), want (64, 32) — trailing usage chunk was dropped", u.PromptTokens, u.CompletionTokens)
	}
}

// TestOllamaRejectsNamespacedModel pins the Provider Routing Isolation
// Invariant: vendor-prefixed IDs (thinkingmachines/..., nvidia/...) MUST be
// rejected by the local Ollama driver BEFORE any network call — no
// speculative trial execution against local endpoints.
func TestOllamaRejectsNamespacedModel(t *testing.T) {
	for _, model := range []string{
		"thinkingmachines/inkling",
		"nvidia/nemotron-reasoning",
		"vendor/model",
	} {
		p := NewOllamaProvider("http://127.0.0.1:1", "", model)
		if _, err := p.Execute(t.Context(), ai.Request{Model: model}); !errors.Is(err, ErrNamespacedModelID) {
			t.Errorf("Execute(%q) = %v, want ErrNamespacedModelID", model, err)
		}
		if _, err := p.ExecuteStream(t.Context(), ai.Request{Model: model}); !errors.Is(err, ErrNamespacedModelID) {
			t.Errorf("ExecuteStream(%q) = %v, want ErrNamespacedModelID", model, err)
		}
	}
	// Bare local IDs still pass validation (they fail later at dial, which
	// proves the guard did NOT reject them — routing is by schema, not by
	// reachability).
	p := NewOllamaProvider("http://127.0.0.1:1", "", "llama3.2:3b")
	if _, err := p.Execute(t.Context(), ai.Request{Model: "llama3.2:3b"}); errors.Is(err, ErrNamespacedModelID) {
		t.Errorf("Execute(local ID) wrongly rejected: %v", err)
	}
}
