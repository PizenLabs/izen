package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestStreamUsageTracker_Authoritative verifies a reader that received a usage
// chunk reports the authoritative provider counts with Known=true.
func TestStreamUsageTracker_Authoritative(t *testing.T) {
	var tr streamUsageTracker
	tr.recordUsageFull(ai.ProviderUsage{
		PromptTokens:     128,
		CompletionTokens: 96,
		Known:            true,
	})
	u := tr.Usage()
	if u.PromptTokens != 128 || u.CompletionTokens != 96 {
		t.Errorf("Usage() = (%d, %d), want (128, 96)", u.PromptTokens, u.CompletionTokens)
	}
	if !u.Known {
		t.Error("Known = false, want true for authoritative usage")
	}
	if u.Estimated {
		t.Error("Estimated = true for an authoritative usage chunk")
	}
	if u.TotalTokens != 224 {
		t.Errorf("TotalTokens = %d, want 224 (128+96)", u.TotalTokens)
	}
	if tr.Estimated() {
		t.Error("Estimated() = true, want false for authoritative usage")
	}
	if tr.Interrupted() {
		t.Error("Interrupted() = true for a clean reader")
	}
}

// TestStreamUsageTracker_InterruptedEstimatesOutput verifies that a stream
// interrupted before any usage chunk reports an ESTIMATED output-token count
// derived from the characters that actually streamed — a real zero would
// silently zero billed work.
func TestStreamUsageTracker_InterruptedEstimatesOutput(t *testing.T) {
	var tr streamUsageTracker
	tr.recordOutput(40) // 40 chars → 10 estimated output tokens
	tr.markInterrupted()
	u := tr.Usage()
	if u.PromptTokens != 0 {
		t.Errorf("input = %d, want 0 (no usage chunk arrived)", u.PromptTokens)
	}
	if u.CompletionTokens != 10 {
		t.Errorf("output = %d, want 10 (40 chars / 4)", u.CompletionTokens)
	}
	if !u.Known {
		t.Error("Known = false, want true (estimate is displayed, never a silent 0)")
	}
	if !u.Estimated {
		t.Error("Estimated = false, want true for character estimate")
	}
	if !tr.Interrupted() {
		t.Error("Interrupted() = false, want true")
	}
}

// TestStreamUsageTracker_ReasoningCharsDoNotInflateOutputEstimate pins Phase 7
// P6: reasoning characters are accounted separately and must NEVER be mixed
// into the output-character estimate. A thinking-heavy stream with a large
// reasoning payload must report an output estimate derived from the content
// chars alone.
func TestStreamUsageTracker_ReasoningCharsDoNotInflateOutputEstimate(t *testing.T) {
	var tr streamUsageTracker
	tr.recordReasoning(8000) // heavy reasoning, would add 2000 tokens if mixed in
	tr.recordOutput(40)      // 40 content chars → 10 estimated output tokens
	tr.markInterrupted()
	u := tr.Usage()
	if u.CompletionTokens != 10 {
		t.Errorf("output = %d, want 10 (reasoning chars must not inflate the output estimate)", u.CompletionTokens)
	}
	if !u.Known || !u.Estimated {
		t.Errorf("Known=%v Estimated=%v, want true/true", u.Known, u.Estimated)
	}
}

// TestStreamUsageTracker_AuthoritativeReasoningSplitsSurvive verifies that the
// provider-reported reasoning split is preserved verbatim when authoritative
// usage arrives (estimation is only ever a fallback for interrupted streams).
func TestStreamUsageTracker_AuthoritativeReasoningSplitsSurvive(t *testing.T) {
	var tr streamUsageTracker
	tr.recordReasoning(8000)
	tr.recordOutput(40)
	tr.recordUsageFull(ai.ProviderUsage{
		PromptTokens:     100,
		CompletionTokens: 200,
		ReasoningTokens:  50,
		TotalTokens:      350,
		Known:            true,
	})
	u := tr.Usage()
	if u.CompletionTokens != 200 {
		t.Errorf("output = %d, want authoritative 200", u.CompletionTokens)
	}
	if u.ReasoningTokens != 50 {
		t.Errorf("reasoning = %d, want authoritative 50", u.ReasoningTokens)
	}
	if u.Estimated {
		t.Error("Estimated = true, want false once the authoritative chunk arrived")
	}
}

// TestStreamUsageTracker_UnknownReportsKnownFalse verifies a reader that saw
// neither a usage chunk nor any output bytes reports usage UNKNOWN (Known
// false), not a fabricated zero.
func TestStreamUsageTracker_UnknownReportsKnownFalse(t *testing.T) {
	var tr streamUsageTracker
	u := tr.Usage()
	if u.Known {
		t.Error("Known = true for a tracker with no provider usage and no bytes")
	}
	if u.CompletionTokens != 0 {
		t.Errorf("CompletionTokens = %d, want 0 (unknown)", u.CompletionTokens)
	}
}

// TestStreamUsageTracker_AuthoritativeWinsOverEstimate verifies that a usage
// chunk that arrives late overrides the character estimate.
func TestStreamUsageTracker_AuthoritativeWinsOverEstimate(t *testing.T) {
	var tr streamUsageTracker
	tr.recordOutput(400)
	tr.recordUsageFull(ai.ProviderUsage{
		PromptTokens:     64,
		CompletionTokens: 32,
		Known:            true,
	})
	u := tr.Usage()
	if u.PromptTokens != 64 || u.CompletionTokens != 32 {
		t.Errorf("Usage() = (%d, %d), want (64, 32)", u.PromptTokens, u.CompletionTokens)
	}
	if u.Estimated {
		t.Error("Estimated = true, want false once the authoritative chunk arrived")
	}
}

// TestOpenRouterStreamResult_UsageAuthoritative verifies the reader surfaces
// the provider-reported usage when a usage chunk arrives on the stream,
// including the cached/reasoning token splits OpenRouter exposes.
func TestOpenRouterStreamResult_UsageAuthoritative(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}",
		"",
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"world\"}}]}",
		"",
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":512,\"completion_tokens\":240,\"total_tokens\":752,\"prompt_tokens_details\":{\"cached_tokens\":128,\"reasoning_tokens\":32},\"completion_tokens_details\":{\"reasoning_tokens\":16}}}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(sse))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "Hello world" {
		t.Fatalf("content = %q, want %q", got, "Hello world")
	}
	u := res.Usage()
	if u.PromptTokens != 512 || u.CompletionTokens != 240 {
		t.Errorf("Usage() = (%d, %d), want (512, 240)", u.PromptTokens, u.CompletionTokens)
	}
	if !u.Known {
		t.Error("Known = false, want true")
	}
	if u.CachedTokens != 128 {
		t.Errorf("CachedTokens = %d, want 128", u.CachedTokens)
	}
	if u.ReasoningTokens != 48 {
		t.Errorf("ReasoningTokens = %d, want 48 (32 prompt + 16 completion)", u.ReasoningTokens)
	}
	if u.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", u.FinishReason, "stop")
	}
}

// TestOpenRouterStreamResult_UsageInterruptedEstimates verifies a reader that
// hits a mid-stream error (e.g. context deadline) reports an ESTIMATED
// character-based count instead of a silent unknown.
func TestOpenRouterStreamResult_UsageInterruptedEstimates(t *testing.T) {
	// A body that yields one content chunk then fails with a sentinel error.
	sse := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"partial answer \"}}]}\n\n"
	body := &failingBody{Reader: strings.NewReader(sse), err: errors.New("context deadline exceeded")}

	sr := &openrouterSSEReader{body: body}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	var got strings.Builder
	buf := make([]byte, 64)
	n, err := res.Read(buf)
	if n > 0 {
		got.Write(buf[:n])
	}
	if err == nil {
		// Keep reading until the injected failure surfaces.
		for err == nil {
			n, err = res.Read(buf)
			if n > 0 {
				got.Write(buf[:n])
			}
		}
	}

	u := res.Usage()
	if u.PromptTokens != 0 {
		t.Errorf("input = %d, want 0", u.PromptTokens)
	}
	if u.CompletionTokens < 1 {
		t.Errorf("output = %d, want a non-zero estimate for %q", u.CompletionTokens, got.String())
	}
	if !u.Estimated {
		t.Error("Estimated = false, want true for an interrupted stream estimate")
	}
}

// TestOpenRouterExecute_PreservesProviderUsage proves the NON-STREAMING
// Execute path propagates the provider's authoritative usage verbatim — the
// exact contract the regression test in Part A depends on ("provider reports
// 2048 completion tokens → Izen execution telemetry records 2048 → UI renders
// 2048 tok").
func TestOpenRouterExecute_PreservesProviderUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"1","object":"chat.completion","model":"vendor/model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2860,"completion_tokens":2048,"total_tokens":4908,
				"prompt_tokens_details":{"cached_tokens":512}}
		}`))
	}))
	defer srv.Close()

	p := NewOpenRouterProvider("test-key", "vendor/model", srv.URL)
	resp, err := p.Execute(context.Background(), ai.Request{Model: "vendor/model"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.TokenInput != 2860 {
		t.Errorf("TokenInput = %d, want 2860", resp.TokenInput)
	}
	if resp.TokenOutput != 2048 {
		t.Errorf("TokenOutput = %d, want 2048", resp.TokenOutput)
	}
	if !resp.Usage.Known {
		t.Error("Usage.Known = false, want true")
	}
	if resp.Usage.CompletionTokens != 2048 {
		t.Errorf("Usage.CompletionTokens = %d, want 2048", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 4908 {
		t.Errorf("Usage.TotalTokens = %d, want 4908", resp.Usage.TotalTokens)
	}
	if resp.Usage.CachedTokens != 512 {
		t.Errorf("Usage.CachedTokens = %d, want 512", resp.Usage.CachedTokens)
	}
	if resp.Usage.FinishReason != "stop" {
		t.Errorf("Usage.FinishReason = %q, want %q", resp.Usage.FinishReason, "stop")
	}
}

// failingBody wraps an io.Reader and fails the first read that crosses the
// boundary of the wrapped content with the given error, simulating a stream
// interrupted by a context deadline.
type failingBody struct {
	io.Reader
	err error
	all bool
}

func (f *failingBody) Read(p []byte) (int, error) {
	if f.all {
		return 0, f.err
	}
	n, err := f.Reader.Read(p)
	if err == io.EOF {
		f.all = true
		return n, f.err
	}
	return n, err
}

func (f *failingBody) Close() error { return nil }

// TestReasoningTokensDoNotPolluteOutputEstimate pins the exact 5,883-token
// repro arithmetic: a thinking-heavy stream whose reasoning alone would have
// inflated the estimate by ~2000 tokens must still estimate CompletionTokens
// from the visible content characters ONLY. The authoritative split (5883
// completion / 5000 reasoning) is the provider's, never the estimator's.
func TestReasoningTokensDoNotPolluteOutputEstimate(t *testing.T) {
	var tr streamUsageTracker
	tr.recordReasoning(20000) // ~20000 reasoning chars would add ~5000 if mixed in
	tr.recordOutput(883)      // ~883 visible content chars -> 220 estimated tokens
	tr.markInterrupted()
	u := tr.Usage()
	if !u.Known || !u.Estimated {
		t.Fatalf("Known=%v Estimated=%v, want true/true", u.Known, u.Estimated)
	}
	if u.CompletionTokens != 883/4 {
		t.Errorf("CompletionTokens = %d, want %d (estimate uses outputChars only, never reasoningChars)",
			u.CompletionTokens, 883/4)
	}
	if u.ReasoningTokens != 0 {
		t.Errorf("ReasoningTokens = %d, want 0 (estimate never fabricates a reasoning split)", u.ReasoningTokens)
	}
}

// TestProviderTimingUsesCorrectBoundary pins the timing forensics boundaries:
// RequestStartedAt -> FirstTokenAt is the time-to-first-token window,
// FirstTokenAt -> CompletedAt is the generation window. These are the exact
// intervals Izen exposes on ProviderUsage — never one opaque "latency" value.
func TestProviderTimingUsesCorrectBoundary(t *testing.T) {
	var tr streamUsageTracker
	t0 := time.Now()
	tr.markRequestStarted(t0)
	t1 := t0.Add(420 * time.Millisecond) // 0.42s time-to-first-token
	time.Sleep(time.Millisecond)         // ensure t1 is strictly after t0 wall-clock
	// recordOutput latches firstTokenAt at the real current time; to keep the
	// test deterministic we re-seed the tracker's first-token marker directly.
	tr.requestStartedAt = t0
	tr.firstTokenAt = t1
	tr.recordOutput(400)            // no-op on firstTokenAt (already set) but accumulates chars
	t2 := t1.Add(136 * time.Second) // 136s generation window (5883/43.1 ≈ 136.5s)
	tr.markCompleted(t2, "stop")

	u := tr.Usage()
	if u.RequestStartedAt != t0 {
		t.Errorf("RequestStartedAt = %v, want %v", u.RequestStartedAt, t0)
	}
	if u.FirstTokenAt != t1 {
		t.Errorf("FirstTokenAt = %v, want %v", u.FirstTokenAt, t1)
	}
	if u.CompletedAt != t2 {
		t.Errorf("CompletedAt = %v, want %v", u.CompletedAt, t2)
	}
	if got := u.FirstTokenAt.Sub(u.RequestStartedAt); got != 420*time.Millisecond {
		t.Errorf("TTFT boundary = %v, want 420ms (FirstTokenAt - RequestStartedAt)", got)
	}
	if got := u.CompletedAt.Sub(u.FirstTokenAt); got != 136*time.Second {
		t.Errorf("generation window = %v, want 136s (CompletedAt - FirstTokenAt)", got)
	}
	if tr.Interrupted() {
		t.Error("Interrupted() = true, want false (natural stop)")
	}
}
