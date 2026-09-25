package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/httpx"
)

func testProviderWithServer(t *testing.T, handler http.HandlerFunc) (*OpenRouterProvider, *int64) {
	t.Helper()
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	p := NewOpenRouterProvider("test-key", "openai/gpt-4o", server.URL)
	p.client = server.Client()
	return p, &hits
}

// requestToolNames decodes the tools array from a captured OpenRouter payload
// and returns the declared function names.
func requestToolNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["tools"].([]any)
	if !ok {
		return nil
	}
	var names []string
	for _, item := range raw {
		entry, _ := item.(map[string]any)
		fn, _ := entry["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// TestExecute_AgenticModelPromotesReadOnlyTools: an agentic-harness model is no
// longer rejected. The request is dispatched with a ToolEnabledCompletion
// contract carrying IZEN's authentic read-only tools so the provider accepts it.
func TestExecute_AgenticModelPromotesReadOnlyTools(t *testing.T) {
	var body map[string]any
	p, hits := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"g1","object":"chat.completion","created":1,"model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})
	resp, err := p.Execute(context.Background(), ai.Request{
		Model:    "thinkingmachines/inkling-small:free",
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v, want success (promoted, not rejected)", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("Execute() resp = %+v, want content ok", resp)
	}
	if n := atomic.LoadInt64(hits); n != 1 {
		t.Fatalf("agentic model caused %d HTTP requests, want 1", n)
	}
	names := requestToolNames(t, body)
	if len(names) == 0 {
		t.Fatalf("agentic payload must carry read-only tools, body=%v", body)
	}
	want := map[string]bool{
		ai.ToolReadFile: false, ai.ToolListDirectory: false,
		ai.ToolSearchCodebase: false, ai.ToolSymbolLookup: false,
	}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("payload missing authentic read-only tool %q (got %v)", name, names)
		}
	}
}

// TestExecuteStream_AgenticModelPromotesReadOnlyTools: the streaming path also
// promotes and carries the tool schemas (no toolless 403-inducing request).
func TestExecuteStream_AgenticModelPromotesReadOnlyTools(t *testing.T) {
	var body map[string]any
	p, _ := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"g1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"\"}]}\n\ndata: [DONE]\n\n"))
	})
	rc, err := p.ExecuteStream(context.Background(), ai.Request{
		Model:    "thinkingmachines/inkling-small:free",
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v, want success", err)
	}
	if rc == nil {
		t.Fatal("ExecuteStream() must return a stream")
	}
	defer func() { _ = rc.Close() }()
	if names := requestToolNames(t, body); len(names) == 0 {
		t.Fatalf("streaming agentic payload must carry read-only tools, body=%v", body)
	}
}

// TestExecute_StandardModelKeepsDirectCompletion: a normal model is not
// promoted and sends no tools (lower token overhead / latency).
func TestExecute_StandardModelKeepsDirectCompletion(t *testing.T) {
	var body map[string]any
	p, _ := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"g1","object":"chat.completion","created":1,"model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})
	if _, err := p.Execute(context.Background(), ai.Request{
		Model:    "openai/gpt-4o",
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if names := requestToolNames(t, body); len(names) != 0 {
		t.Fatalf("standard model must not carry tools, got %v", names)
	}
}

// TestExecute_ClassifiesAgenticHarnessGate: the provider's routing gate
// ("Gate Free Endpoints by Agentic Harness") is classified distinctly from a
// plain compatibility 403 so the UI can explain the access policy instead of
// reporting a model incompatibility. Neither class reverts the model.
func TestExecute_ClassifiesAgenticHarnessGate(t *testing.T) {
	p, _ := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"thinkingmachines/future-model is only available on agentic harnesses. Try plugging it into a coding agent or productivity app listed on https://openrouter.ai/apps","code":403,"metadata":{"routing_funnel":[{"step":"Initial Endpoints","endpoint_count":1}],"failed_routing_step":"Gate Free Endpoints by Agentic Harness"}}}`))
	})
	_, err := p.Execute(context.Background(), ai.Request{Model: "thinkingmachines/future-model"})
	if !errors.Is(err, ErrOpenRouterAgenticGate) {
		t.Fatalf("Execute() error = %v, want ErrOpenRouterAgenticGate", err)
	}
	if errors.Is(err, ErrOpenRouterModelIncompatible) {
		t.Errorf("the routing gate must not be reported as a model incompatibility: %v", err)
	}
	if !strings.Contains(err.Error(), "thinkingmachines/future-model") {
		t.Errorf("the gated model must be named in the error, got %q", err.Error())
	}
}

// TestExecute_ClassifiesAgenticHarness403: a 403 carrying the provider's
// agentic-harness refusal WITHOUT the routing-gate metadata is a compatibility
// error, not a generic streaming failure.
func TestExecute_ClassifiesAgenticHarness403(t *testing.T) {
	p, _ := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"thinkingmachines/future-model is only available on agentic harnesses.","code":403}}`))
	})
	_, err := p.Execute(context.Background(), ai.Request{Model: "thinkingmachines/future-model"})
	if !errors.Is(err, ErrOpenRouterModelIncompatible) {
		t.Fatalf("Execute() error = %v, want ErrOpenRouterModelIncompatible", err)
	}
	// The structured classification behind the wrap: same body must parse
	// to a compatibility ProviderError.
	pe := httpx.ParseProviderError("openrouter", 403, []byte(`{"error":{"message":"thinkingmachines/future-model is only available on agentic harnesses.","code":403}}`))
	if !pe.IsModelCompatibility() {
		t.Errorf("ProviderError.IsModelCompatibility() = false for agentic-harness 403")
	}
	if !strings.Contains(err.Error(), "only available on agentic harnesses") {
		t.Errorf("raw provider message must be preserved, got %q", err.Error())
	}
}

// TestExecute_Ordinary403IsNotCompatibility: other 403s keep the existing
// generic ProviderError behavior.
func TestExecute_Ordinary403IsNotCompatibility(t *testing.T) {
	p, _ := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Spend limit exceeded","code":403}}`))
	})
	_, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if err == nil {
		t.Fatalf("Execute() must fail on 403")
	}
	if errors.Is(err, ErrOpenRouterModelIncompatible) {
		t.Fatalf("ordinary 403 must not classify as compatibility: %v", err)
	}
	var pe *httpx.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want *httpx.ProviderError, got %T", err)
	}
	if pe.IsModelCompatibility() {
		t.Errorf("IsModelCompatibility() must be false for non-harness 403")
	}
}

// TestExecute_EligibleModelUnchanged: normal models execute exactly as
// before, with documented attribution headers only — no speculative
// eligibility headers on the wire.
func TestExecute_EligibleModelUnchanged(t *testing.T) {
	var gotHeaders http.Header
	p, hits := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-1","object":"chat.completion","created":1,"model":"openai/gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	})
	resp, err := p.Execute(context.Background(), ai.Request{
		Model:    "openai/gpt-4o",
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp.Content != "hi" {
		t.Errorf("Content = %q, want %q", resp.Content, "hi")
	}
	if n := atomic.LoadInt64(hits); n != 1 {
		t.Fatalf("eligible model caused %d requests, want 1", n)
	}
	if gotHeaders.Get("Authorization") == "" {
		t.Errorf("Authorization header must be sent")
	}
	if gotHeaders.Get("HTTP-Referer") == "" || gotHeaders.Get("X-OpenRouter-Title") == "" {
		t.Errorf("documented attribution headers must be sent")
	}
	for _, h := range []string{"X-OpenRouter-Categories", "X-OpenRouter-Description"} {
		if v := gotHeaders.Get(h); v != "" {
			t.Errorf("speculative header %s must not be sent, got %q", h, v)
		}
	}
}

// TestExecuteStream_EligibleModelStreams: regression — the streaming path is
// untouched for eligible models.
func TestExecuteStream_EligibleModelStreams(t *testing.T) {
	p, _ := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"gen-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai/gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"\"}]}\n\ndata: [DONE]\n\n"))
	})
	rc, err := p.ExecuteStream(context.Background(), ai.Request{
		Model:    "openai/gpt-4o",
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("stream body = %q, want streamed content", body)
	}
}
