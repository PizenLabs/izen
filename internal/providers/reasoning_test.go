package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/stream"
)

// streamReasonBlock is the provider-side seam check: it mirrors the plan
// engine's reasoning extraction so the test can assert concatenation without
// importing the engine package.
func streamReasonBlock(raw string) (content, reasoning string) {
	return stream.ReasonBlock(raw)
}

// ── firstUsableContent / stripThinkingTags ───────────────────────────────────

func TestFirstUsableContent_PrefersContent(t *testing.T) {
	got := firstUsableContent("{json}", "<think>thinking</think>", "")
	if got != "{json}" {
		t.Fatalf("got %q, want %q", got, "{json}")
	}
}

func TestFirstUsableContent_FallsBackToReasoning(t *testing.T) {
	got := firstUsableContent("", "<think>the plan</think>", "ignored")
	if got != "the plan" {
		t.Fatalf("got %q, want %q", got, "the plan")
	}
}

func TestFirstUsableContent_FallsBackToReasoningContent(t *testing.T) {
	got := firstUsableContent("", "", "<thought>the plan</thought>")
	if got != "the plan" {
		t.Fatalf("got %q, want %q", got, "the plan")
	}
}

func TestFirstUsableContent_AllEmpty(t *testing.T) {
	if got := firstUsableContent("", "", ""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestFirstUsableContent_StripsReasoningSentinel(t *testing.T) {
	got := firstUsableContent("", "\x00RSNG\x00plan\x00RSNG\x00", "")
	if got != "plan" {
		t.Fatalf("got %q, want %q", got, "plan")
	}
}

func TestStripThinkingTags_RepeatedBlocks(t *testing.T) {
	got := stripThinkingTags("<think>a</think><think>b</think>")
	if got != "ab" {
		t.Fatalf("got %q, want %q", got, "ab")
	}
}

// ── OpenRouter non-streaming reasoning fallback ──────────────────────────────

// TestOpenRouterExecute_ReasoningFallback simulates the reported Mini/Free
// cloud failure mode: the provider returns an empty message.content but a
// complete plan inside message.reasoning. Execute must promote the reasoning
// text (stripped of thinking delimiters) to the response content so the plan
// engine never sees "empty response from provider".
func TestOpenRouterExecute_ReasoningFallback(t *testing.T) {
	const plan = `{"architectural_strategy":"fix dep","atomic_tasks":[{"task_id":1,"strategy":"SHELL_EXEC","file":"go mod tidy","description":"tidy","rationale":"fix"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "1", "object": "chat.completion", "model": "m",
			"choices": []map[string]interface{}{
				{"index": 0, "message": map[string]interface{}{
					"role":      "assistant",
					"content":   "",
					"reasoning": "<think>" + plan + "</think>",
				}, "finish_reason": "stop"},
			},
			"usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 20},
		})
	}))
	defer srv.Close()

	p := NewOpenRouterProvider("test-key", "anthropic/test-model", srv.URL)
	resp, err := p.Execute(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Content != plan {
		t.Fatalf("Content = %q, want %q (reasoning fallback must strip think tags)", resp.Content, plan)
	}
}

// TestOpenRouterExecute_ReasoningContentField covers the reasoning_content
// field variant used by DeepSeek-style models.
func TestOpenRouterExecute_ReasoningContentField(t *testing.T) {
	const plan = `{"architectural_strategy":"s","atomic_tasks":[{"task_id":1,"strategy":"FILE_MUTATE","file":"a.go","description":"d"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "1", "object": "chat.completion", "model": "m",
			"choices": []map[string]interface{}{
				{"index": 0, "message": map[string]interface{}{
					"role":              "assistant",
					"content":           "",
					"reasoning_content": plan,
				}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	p := NewOpenRouterProvider("test-key", "anthropic/test-model", srv.URL)
	resp, err := p.Execute(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Content != plan {
		t.Fatalf("Content = %q, want %q", resp.Content, plan)
	}
}

// TestOpenRouterExecute_ContentWinsOverReasoning ensures a normal response
// (non-empty content) is never replaced by reasoning text.
func TestOpenRouterExecute_ContentWinsOverReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "1", "object": "chat.completion", "model": "m",
			"choices": []map[string]interface{}{
				{"index": 0, "message": map[string]interface{}{
					"role":      "assistant",
					"content":   "the real answer",
					"reasoning": "<think>do not use</think>",
				}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	p := NewOpenRouterProvider("test-key", "anthropic/test-model", srv.URL)
	resp, err := p.Execute(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Content != "the real answer" {
		t.Fatalf("Content = %q, want %q", resp.Content, "the real answer")
	}
}

// ── OpenRouter streaming reasoning-only emission ─────────────────────────────

// TestOpenRouterStreamResult_ReasoningOnly pins the infra contract the plan
// engine's reasoning fallback depends on: a stream with reasoning deltas and
// no content deltas must surface the reasoning text (sentinel-wrapped, one
// block per delta) as the read stream, never an empty buffer. The engine's
// ReasonBlock concatenates the per-delta blocks back into the full thinking
// text.
func TestOpenRouterStreamResult_ReasoningOnly(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan json\"}}]}",
		"",
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\" more\"}}]}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(sse))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	got := drainStream(t, res)
	want := ReasoningSentinel + "plan json" + ReasoningSentinel + ReasoningSentinel + " more" + ReasoningSentinel
	if got != want {
		t.Errorf("reasoning-only stream = %q, want %q", got, want)
	}

	// The engine consumes this via stream.ReasonBlock: both reasoning blocks
	// must concatenate with no content leaked.
	content, reasoning := streamReasonBlock(got)
	if content != "" {
		t.Errorf("content = %q, want empty", content)
	}
	if reasoning != "plan json more" {
		t.Errorf("reasoning = %q, want %q", reasoning, "plan json more")
	}
}
