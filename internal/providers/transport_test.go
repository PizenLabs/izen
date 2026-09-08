package providers

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// Every provider client must ride a strict transport: an explicit dial, TLS
// handshake, response-header, and expect-continue bound so a dead endpoint
// fails fast with a phase-identifiable error instead of hanging the turn.
func TestProviderTransportsAreStrict(t *testing.T) {
	headerBound := func(c *http.Client) time.Duration {
		tr, ok := c.Transport.(*http.Transport)
		if !ok {
			return -1
		}
		if tr.DialContext == nil {
			return -2
		}
		if tr.TLSHandshakeTimeout != 5*time.Second {
			return -3
		}
		if tr.ExpectContinueTimeout != time.Second {
			return -4
		}
		return tr.ResponseHeaderTimeout
	}

	cloud := []struct {
		name   string
		client *http.Client
	}{
		{"claude", NewClaudeProvider("k", "m").client},
		{"gemini", NewGeminiProvider("k", "m").client},
		{"groq", NewGroqProvider("k", "m", "").client},
		{"ninerouter", NewNineRouterProvider("k", "m", "").client},
		{"opencode", NewOpenCodeProvider("k", "m", "").client},
		{"openai", NewOpenAIProvider("k", "m").client},
		{"openrouter", NewOpenRouterProvider("k", "m", "").client},
	}
	for _, p := range cloud {
		if got := headerBound(p.client); got != CloudResponseHeaderTimeout {
			t.Errorf("%s: header bound = %v, want cloud %v", p.name, got, CloudResponseHeaderTimeout)
		}
	}

	// Local models keep the looser bound: cold loads exceed 10s TTFT.
	if got := headerBound(NewOllamaProvider("http://localhost:11434", "", "m").client); got != LocalResponseHeaderTimeout {
		t.Errorf("ollama: header bound = %v, want local %v", got, LocalResponseHeaderTimeout)
	}
}

// The TTFT alias must surface the same phase diagnosis as the shared helper.
func TestTTFTPhaseDetailDelegates(t *testing.T) {
	if got := TTFTPhaseDetail(errors.New(`Get "https://x": net/http: timeout awaiting response headers`)); got != "No response headers received from endpoint" {
		t.Errorf("TTFTPhaseDetail = %q, want header-stall diagnosis", got)
	}
	if got := TTFTPhaseDetail(nil); got != "" {
		t.Errorf("TTFTPhaseDetail(nil) = %q, want empty", got)
	}
}
