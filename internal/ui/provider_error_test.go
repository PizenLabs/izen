package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/httpx"
)

func TestFormatProviderErrorBannerRaw(t *testing.T) {
	pe := httpx.ParseProviderError("openrouter", 400, []byte(`{"error":{"message":"Model cohere/north-mini-code requires higher max_tokens budget"}}`))
	banner := FormatProviderErrorBanner(pe)
	if !strings.Contains(banner, "400") {
		t.Fatalf("banner missing status: %q", banner)
	}
	if !strings.Contains(banner, "Model cohere/north-mini-code requires higher max_tokens budget") {
		t.Fatalf("banner must carry exact raw message: %q", banner)
	}
	// Invalid API key simulation.
	pe2 := httpx.ParseProviderError("openai", 401, []byte(`{"error":{"message":"Incorrect API key provided"}}`))
	b2 := FormatProviderErrorBanner(pe2)
	if !strings.Contains(b2, "401") || !strings.Contains(b2, "Incorrect API key provided") {
		t.Fatalf("banner = %q", b2)
	}
}
