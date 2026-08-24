package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// ── HTTP 429 RATE LIMIT BACKOFF ───────────────────────────────────────────

// setBackoffBase temporarily overrides the exponential backoff base unit so a
// test can shrink (or inflate) the retry delays without slowing the suite. It
// returns a restore function to be deferred by the caller.
func setBackoffBase(base time.Duration) func() {
	prev := openRouterRateLimitBackoffBase
	openRouterRateLimitBackoffBase = base
	return func() { openRouterRateLimitBackoffBase = prev }
}

// TestOpenRouterRateLimitBackoff pins the exponential backoff schedule for
// HTTP 429 retries: 1s -> 2s -> 4s across the three retry slots.
func TestOpenRouterRateLimitBackoff(t *testing.T) {
	defer setBackoffBase(time.Second)()
	cases := []struct {
		retry int
		want  time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
	}
	for _, tc := range cases {
		if got := openRouterRateLimitBackoff(tc.retry); got != tc.want {
			t.Errorf("openRouterRateLimitBackoff(%d) = %v, want %v", tc.retry, got, tc.want)
		}
	}
}

// TestRetryAfterDelay covers the Retry-After header parser: integer seconds,
// HTTP-date, absent, and garbage values.
func TestRetryAfterDelay(t *testing.T) {
	if _, ok := retryAfterDelay(&http.Response{Header: make(http.Header)}); ok {
		t.Error("missing Retry-After header must report ok=false")
	}
	if _, ok := retryAfterDelay(nil); ok {
		t.Error("nil response must report ok=false")
	}

	if d, ok := retryAfterDelay(&http.Response{Header: http.Header{"Retry-After": {"120"}}}); !ok || d != 120*time.Second {
		t.Errorf("Retry-After: 120 = %v, %v; want 120s, true", d, ok)
	}
	if d, ok := retryAfterDelay(&http.Response{Header: http.Header{"Retry-After": {"0"}}}); !ok || d != 0 {
		t.Errorf("Retry-After: 0 = %v, %v; want 0s, true", d, ok)
	}

	future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	if d, ok := retryAfterDelay(&http.Response{Header: http.Header{"Retry-After": {future}}}); !ok || d < 40*time.Second || d > 50*time.Second {
		t.Errorf("Retry-After HTTP-date = %v, %v; want ~45s, true", d, ok)
	}

	if _, ok := retryAfterDelay(&http.Response{Header: http.Header{"Retry-After": {"garbage"}}}); ok {
		t.Error("garbage Retry-After must report ok=false")
	}
}

// rateLimitTransport answers HTTP 429 for the first rateLimits requests (with
// an optional Retry-After header) and HTTP 200 afterwards. It records every
// attempt's start time so tests can assert the backoff schedule actually
// elapsed between retries.
type rateLimitTransport struct {
	mu         sync.Mutex
	rateLimits int
	retryAfter string
	attempts   int
	attemptAt  []time.Time
}

func (t *rateLimitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.attempts++
	t.attemptAt = append(t.attemptAt, time.Now())
	rateLimited := t.attempts <= t.rateLimits
	t.mu.Unlock()

	status := http.StatusOK
	statusText := "200 OK"
	payload := cannedCompletion
	if r.Header.Get("Accept") == "text/event-stream" {
		payload = "data: [DONE]\n"
	}
	if rateLimited {
		status = http.StatusTooManyRequests
		statusText = "429 Too Many Requests"
		payload = `{"error":{"message":"Rate limit exceeded"}}`
	}
	header := make(http.Header)
	if t.retryAfter != "" {
		header.Set("Retry-After", t.retryAfter)
	}
	return &http.Response{
		StatusCode: status,
		Status:     statusText,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     header,
		Request:    r,
	}, nil
}

func (t *rateLimitTransport) attemptTimes() []time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]time.Time(nil), t.attemptAt...)
}

// TestOpenRouterExecute_Retries429WithBackoff drives the non-streaming path: a
// 429 rate-limit response must be retried with backoff instead of aborting the
// turn, and a later 200 must succeed.
func TestOpenRouterExecute_Retries429WithBackoff(t *testing.T) {
	tr := &rateLimitTransport{rateLimits: 2, retryAfter: "0"}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	resp, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want ok", resp.Content)
	}
	if tr.attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (1 initial + 2 retries)", tr.attempts)
	}
}

// TestOpenRouterExecuteStream_Retries429WithBackoff drives the streaming path
// (the fast-track build stream) through the same 429 backoff: a rate-limited
// free-tier model must recover and stream instead of dying on the first 429.
func TestOpenRouterExecuteStream_Retries429WithBackoff(t *testing.T) {
	tr := &rateLimitTransport{rateLimits: 1, retryAfter: "0"}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	rc, err := p.ExecuteStream(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if _, readErr := io.ReadAll(rc); readErr != nil {
		t.Fatalf("stream read: %v", readErr)
	}
	if tr.attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (1 initial + 1 retry)", tr.attempts)
	}
}

// TestOpenRouterExecute_Persistent429ExhaustsRetries pins the retry ceiling:
// a persistently rate-limited endpoint is retried exactly
// openRouterMaxRateLimitRetries times (initial + 3 retries) before the error
// is surfaced.
func TestOpenRouterExecute_Persistent429ExhaustsRetries(t *testing.T) {
	tr := &rateLimitTransport{rateLimits: 100, retryAfter: "0"}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	if _, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"}); err == nil {
		t.Fatal("Execute should fail on persistent 429")
	}
	want := 1 + openRouterMaxRateLimitRetries
	if tr.attempts != want {
		t.Fatalf("attempts = %d, want %d (initial + %d retries)", tr.attempts, want, openRouterMaxRateLimitRetries)
	}
}

// TestOpenRouterExecute_429HonorsRetryAfter proves the Retry-After header takes
// precedence over the exponential schedule: with the exponential base inflated
// to 10 minutes, a Retry-After: 0 response must still complete immediately.
func TestOpenRouterExecute_429HonorsRetryAfter(t *testing.T) {
	defer setBackoffBase(10 * time.Minute)()
	tr := &rateLimitTransport{rateLimits: 1, retryAfter: "0"}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	start := time.Now()
	_, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Retry-After: 0 not honored — call took %v (exponential backoff used?)", elapsed)
	}
	if tr.attempts != 2 {
		t.Fatalf("attempts = %d, want 2", tr.attempts)
	}
}

// TestUsageIsNotDoubleCountedAcrossHTTPRetries proves the 5,883-token account
// is summed exactly ONCE across a 429 retry sequence: the provider bills one
// usage object on the final 200, and Izen must surface that single authoritative
// count with HTTPAttempts=3 / RateLimitedRetries=2 — never 3x, never a sum of
// per-attempt guesses. The 429 responses carry no usage and must not fabricate
// billed tokens.
func TestUsageIsNotDoubleCountedAcrossHTTPRetries(t *testing.T) {
	const completionTokens = 5883
	const promptTokens = 2181
	usageResponse := `{"id":"1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":` + strconv.Itoa(promptTokens) + `,"completion_tokens":` + strconv.Itoa(completionTokens) + `,"total_tokens":` + strconv.Itoa(promptTokens+completionTokens) + `}}`

	tr := &usageRateLimitTransport{rateLimits: 2, finalPayload: usageResponse, retryAfter: "0"}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	resp, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want ok", resp.Content)
	}
	if tr.attempts != 3 {
		t.Fatalf("HTTP attempts = %d, want 3 (1 initial + 2 rate-limit retries)", tr.attempts)
	}
	if resp.Usage.HTTPAttempts != 3 || resp.Usage.RateLimitedRetries != 2 {
		t.Errorf("retry forensics = HTTPAttempts:%d RateLimitedRetries:%d, want 3/2", resp.Usage.HTTPAttempts, resp.Usage.RateLimitedRetries)
	}
	// The single authoritative usage object is surfaced verbatim — the account
	// is counted once, never multiplied by the 3 HTTP attempts.
	if resp.Usage.CompletionTokens != completionTokens {
		t.Errorf("CompletionTokens = %d, want %d (counted once across 3 HTTP attempts)", resp.Usage.CompletionTokens, completionTokens)
	}
	if resp.Usage.PromptTokens != promptTokens {
		t.Errorf("PromptTokens = %d, want %d", resp.Usage.PromptTokens, promptTokens)
	}
	if !resp.Usage.Known {
		t.Error("Usage.Known = false, want true (authoritative chunk on the final 200)")
	}
	if resp.Usage.Estimated {
		t.Error("Usage.Estimated = true, want false (authoritative usage overrides any estimate)")
	}
}

// usageRateLimitTransport answers HTTP 429 for the first rateLimits requests,
// then the finalPayload (a full chat completion WITH usage) on the next 200.
type usageRateLimitTransport struct {
	mu           sync.Mutex
	rateLimits   int
	attempts     int
	finalPayload string
	retryAfter   string
}

func (t *usageRateLimitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.attempts++
	rateLimited := t.attempts <= t.rateLimits
	t.mu.Unlock()

	status := http.StatusOK
	statusText := "200 OK"
	payload := t.finalPayload
	if r.Header.Get("Accept") == "text/event-stream" {
		payload = "data: [DONE]\n"
	}
	if rateLimited {
		status = http.StatusTooManyRequests
		statusText = "429 Too Many Requests"
		payload = `{"error":{"message":"Rate limit exceeded"}}`
	}
	header := make(http.Header)
	if t.retryAfter != "" {
		header.Set("Retry-After", t.retryAfter)
	}
	return &http.Response{
		StatusCode: status,
		Status:     statusText,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     header,
		Request:    r,
	}, nil
}

// TestOpenRouterExecute_RetryForensicsSingleInvocation pins Phase 7 P5 retry
// forensics on the non-streaming path: two 429 rate-limit retries inside ONE
// logical invocation must surface as HTTPAttempts=3 / RateLimitedRetries=2 on
// the returned usage, while the invocation count stays 1. The 429 responses
// carry no billed tokens — Known stays false because the retried 200 reported
// no usage, proving 429 recovery never fabricates billed output.
func TestOpenRouterExecute_RetryForensicsSingleInvocation(t *testing.T) {
	tr := &rateLimitTransport{rateLimits: 2, retryAfter: "0"}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	resp, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want ok", resp.Content)
	}
	if tr.attempts != 3 {
		t.Fatalf("transport attempts = %d, want 3 (1 initial + 2 429 retries)", tr.attempts)
	}
	if resp.Usage.HTTPAttempts != 3 {
		t.Errorf("Usage.HTTPAttempts = %d, want 3 (one logical invocation, three HTTP attempts)", resp.Usage.HTTPAttempts)
	}
	if resp.Usage.RateLimitedRetries != 2 {
		t.Errorf("Usage.RateLimitedRetries = %d, want 2", resp.Usage.RateLimitedRetries)
	}
	if resp.Usage.Known {
		t.Error("Usage.Known = true, want false — 429 responses carry no billed tokens and the retried 200 reported no usage")
	}
	if resp.Usage.CompletionTokens != 0 {
		t.Errorf("Usage.CompletionTokens = %d, want 0 (no usage chunk -> no billed tokens)", resp.Usage.CompletionTokens)
	}
}

// TestOpenRouterExecuteStream_RetryForensicsSingleInvocation pins the same
// Phase 7 P5 forensics on the streaming path: a single logical streaming
// invocation that recovered from one 429 must report HTTPAttempts=2 /
// RateLimitedRetries=1 via the UsageProvider contract, and the stream must
// still deliver its content.
func TestOpenRouterExecuteStream_RetryForensicsSingleInvocation(t *testing.T) {
	tr := &rateLimitTransport{rateLimits: 1, retryAfter: "0"}
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	rc, err := p.ExecuteStream(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if _, readErr := io.ReadAll(rc); readErr != nil {
		t.Fatalf("stream read: %v", readErr)
	}
	if tr.attempts != 2 {
		t.Fatalf("transport attempts = %d, want 2 (1 initial + 1 429 retry)", tr.attempts)
	}
	up, ok := rc.(ai.UsageProvider)
	if !ok {
		t.Fatal("stream result does not implement ai.UsageProvider")
	}
	u := up.Usage()
	if u.HTTPAttempts != 2 {
		t.Errorf("Usage.HTTPAttempts = %d, want 2", u.HTTPAttempts)
	}
	if u.RateLimitedRetries != 1 {
		t.Errorf("Usage.RateLimitedRetries = %d, want 1", u.RateLimitedRetries)
	}
}

// TestOpenRouterExecute_429ExponentialBackoffEscalates proves the real request
// path sleeps on the exponential schedule (1x, 2x, 4x base) between retries:
// the gaps between consecutive attempt starts must grow by at least the
// expected multiple (halved for timing tolerance).
func TestOpenRouterExecute_429ExponentialBackoffEscalates(t *testing.T) {
	defer setBackoffBase(30 * time.Millisecond)()
	tr := &rateLimitTransport{rateLimits: 100} // no Retry-After -> exponential schedule
	p := NewOpenRouterProvider("key", "openai/gpt-4o", "https://openrouter.example.com/api/v1")
	p.client = &http.Client{Transport: tr}

	if _, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"}); err == nil {
		t.Fatal("Execute should fail on persistent 429")
	}
	if tr.attempts != 4 {
		t.Fatalf("attempts = %d, want 4 (initial + 3 retries)", tr.attempts)
	}
	at := tr.attemptTimes()
	if len(at) != 4 {
		t.Fatalf("recorded %d attempt times, want 4", len(at))
	}
	base := 30 * time.Millisecond
	for i := 1; i < len(at); i++ {
		gap := at[i].Sub(at[i-1])
		min := time.Duration(1<<(i-1)) * base / 2
		if gap < min {
			t.Errorf("gap[%d] = %v, want >= %v (exponential backoff not applied)", i-1, gap, min)
		}
	}
}
