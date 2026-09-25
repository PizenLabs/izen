package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/httpx"
)

// TestClassifyFallbackError_Eligible: only network-transient failures may
// switch to the role's configured fallback model.
func TestClassifyFallbackError_Eligible(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want FallbackClass
	}{
		{"rate limit 429", NewProviderError("openrouter", http.StatusTooManyRequests, []byte(`{"error":{"message":"rate limited"}}`)), FallbackClassRateLimit},
		{"server 500", NewProviderError("openrouter", 500, []byte(`{"error":{"message":"upstream boom"}}`)), FallbackClassServerError},
		{"bad gateway 502", NewProviderError("openrouter", 502, []byte(`{"error":{"message":"bad gateway"}}`)), FallbackClassServerError},
		{"unavailable 503", NewProviderError("openrouter", 503, []byte(`{"error":{"message":"overloaded"}}`)), FallbackClassServerError},
		{"context deadline", context.DeadlineExceeded, FallbackClassTimeout},
		{"os deadline", os.ErrDeadlineExceeded, FallbackClassTimeout},
		{"wrapped deadline", fmt.Errorf("stream: %w", context.DeadlineExceeded), FallbackClassTimeout},
		{"connection refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, FallbackClassNetwork},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "api.openrouter.ai", IsNotFound: true}, FallbackClassNetwork},
		{"wrapped provider 500", fmt.Errorf("openrouter: %w", NewProviderError("openrouter", 520, []byte(`{}`))), FallbackClassServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFallbackError(tc.err); got != tc.want {
				t.Errorf("ClassifyFallbackError(%v) = %q, want %q", tc.err, got, tc.want)
			}
			if !IsFallbackEligible(tc.err) {
				t.Errorf("IsFallbackEligible(%v) = false, want true", tc.err)
			}
		})
	}
}

// TestClassifyFallbackError_NotEligible: wire-policy requirements (handled by
// Dynamic Contract Promotion), client errors, cancellation and local contract
// failures must NEVER switch models.
func TestClassifyFallbackError_NotEligible(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"agentic harness wire refusal", fmt.Errorf("%w: %w", ErrOpenRouterModelIncompatible, NewProviderError("openrouter", 403, []byte(`{"error":{"message":"only available on agentic harnesses"}}`)))},
		{"agentic harness routing gate", fmt.Errorf("%w: %w", ErrOpenRouterAgenticGate, NewProviderError("openrouter", 403, []byte(`{"error":{"message":"only available on agentic harnesses"}}`)))},
		{"plain 403", NewProviderError("openrouter", http.StatusForbidden, []byte(`{"error":{"message":"spend limit"}}`))},
		{"401 unauthorized", NewProviderError("openrouter", http.StatusUnauthorized, []byte(`{"error":{"message":"no key"}}`))},
		{"404 unknown model", NewProviderError("openrouter", http.StatusNotFound, []byte(`{"error":{"message":"no such model"}}`))},
		{"400 bad request", NewProviderError("openrouter", http.StatusBadRequest, []byte(`{"error":{"message":"max_tokens too low"}}`))},
		{"user cancellation", context.Canceled},
		{"auth sentinel", fmt.Errorf("%w: missing key", ErrOpenRouterAuth)},
		{"unassigned model", ErrUnassignedTargetModel},
		{"local decode failure", errors.New("openrouter: decode: unexpected end of JSON input")},
		{"plain error", errors.New("something went wrong")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFallbackError(tc.err); got != FallbackClassNone {
				t.Errorf("ClassifyFallbackError(%v) = %q, want no fallback", tc.err, got)
			}
			if IsFallbackEligible(tc.err) {
				t.Errorf("IsFallbackEligible(%v) = true, want false", tc.err)
			}
		})
	}
}

// TestFallbackReason pins the trace-view cause text.
func TestFallbackReason(t *testing.T) {
	rate := NewProviderError("openrouter", 429, []byte(`{"error":{"message":"slow down"}}`))
	if got := FallbackReason(rate); got != "rate limit (HTTP 429)" {
		t.Errorf("FallbackReason(429) = %q", got)
	}
	if got := FallbackReason(context.DeadlineExceeded); !strings.Contains(got, "timeout") {
		t.Errorf("FallbackReason(deadline) = %q, want a timeout reason", got)
	}
	if got := FallbackReason(errors.New("boom")); got != "unknown" {
		t.Errorf("FallbackReason(other) = %q, want unknown", got)
	}
}

// TestFormatFallbackEvent pins the exact trace line emitted on a chain switch.
func TestFormatFallbackEvent(t *testing.T) {
	got := FormatFallbackEvent("openrouter/anthropic/claude-3.5-sonnet", "rate limit (HTTP 429)")
	want := "[fallback] Primary model failed (rate limit (HTTP 429)). Switched to openrouter/anthropic/claude-3.5-sonnet."
	if got != want {
		t.Errorf("FormatFallbackEvent() = %q, want %q", got, want)
	}
}

// TestClassifyFallbackError_UsesStructuredStatusNotText: classification reads
// the authoritative status code, never the provider's prose. A 200-era body
// mentioning "rate limit" must not be treated as a rate limit.
func TestClassifyFallbackError_UsesStructuredStatusNotText(t *testing.T) {
	err := httpx.ParseProviderError("openrouter", 400, []byte(`{"error":{"message":"rate limit exceeded, try 429"}}`))
	if got := ClassifyFallbackError(err); got != FallbackClassNone {
		t.Fatalf("class = %q, want no fallback for HTTP 400", got)
	}
}
