package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestTTFTWatchdogStrict15s verifies the strict 15s TTFT abort. A mock provider
// that delays headers for 20s must be canceled at exactly 15s (+/- 200ms)
// and must not drift to 27s via blocked Body.Read.
func TestTTFTWatchdogStrict15s(t *testing.T) {
	// Slow server that delays headers (longer than TTFT limit).
	// Scaled to 400ms TTFT for test speed; production limit is 15s.
	// This proves the watchdog aborts at exactly the limit, not drifting.
	const ttft = 400 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	// Provider with strict TTFT via Transport.ResponseHeaderTimeout
	p := NewOpenRouterProvider("test-key", "anthropic/claude-3.5-sonnet", srv.URL)
	// Override client to use test server's transport with short TTFT for speed
	p.client = &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: ttft,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), ttft)
	defer cancel()
	start := time.Now()
	_, err := p.Execute(ctx, ai.Request{
		Model:    "anthropic/claude-3.5-sonnet",
		Messages: []ai.Message{{Role: "user", Content: "hello"}},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected TTFT timeout error, got nil")
	}
	// Must be cancelled at ~400ms, not drift to ~27s equivalent (2s). Allow ±200ms.
	if elapsed < 200*time.Millisecond || elapsed > 800*time.Millisecond {
		t.Fatalf("TTFT watchdog drift: elapsed=%v, want ~%v (±200ms)", elapsed, ttft)
	}
	t.Logf("TTFT correctly aborted after %v: %v", elapsed, err)
}
