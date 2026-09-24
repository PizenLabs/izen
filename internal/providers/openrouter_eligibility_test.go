package providers

import (
	"context"
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

// TestExecute_RejectsIneligibleModelWithoutNetwork: a known-ineligible model
// must fail locally with a compatibility error before any HTTP request.
func TestExecute_RejectsIneligibleModelWithoutNetwork(t *testing.T) {
	p, hits := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	_, err := p.Execute(context.Background(), ai.Request{Model: "thinkingmachines/inkling:free"})
	if !errors.Is(err, ErrOpenRouterModelIncompatible) {
		t.Fatalf("Execute() error = %v, want ErrOpenRouterModelIncompatible", err)
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Fatalf("ineligible model caused %d HTTP requests, want 0", n)
	}
}

// TestExecuteStream_RejectsIneligibleModelWithoutNetwork: same guard on the
// streaming path — no request, no stream state corruption.
func TestExecuteStream_RejectsIneligibleModelWithoutNetwork(t *testing.T) {
	p, hits := testProviderWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rc, err := p.ExecuteStream(context.Background(), ai.Request{Model: "thinkingmachines/inkling:free"})
	if !errors.Is(err, ErrOpenRouterModelIncompatible) {
		t.Fatalf("ExecuteStream() error = %v, want ErrOpenRouterModelIncompatible", err)
	}
	if rc != nil {
		_ = rc.Close()
		t.Fatalf("ExecuteStream() must not return a stream on guard rejection")
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Fatalf("ineligible model caused %d HTTP requests, want 0", n)
	}
}

// TestExecute_ClassifiesAgenticHarness403: a 403 carrying the provider's
// agentic-harness refusal for a not-seeded model is a compatibility error,
// not a generic streaming failure.
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
