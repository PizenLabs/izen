package httpx

import (
	"strings"
	"testing"
)

func TestParseProviderErrorRawMessage(t *testing.T) {
	body := []byte(`{"error":{"message":"Model cohere/north-mini-code requires higher max_tokens budget","type":"invalid_request_error","code":400}}`)
	pe := ParseProviderError("openrouter", 400, body)
	if pe.StatusCode != 400 {
		t.Fatalf("StatusCode = %d, want 400", pe.StatusCode)
	}
	if pe.RawMessage != "Model cohere/north-mini-code requires higher max_tokens budget" {
		t.Fatalf("RawMessage = %q", pe.RawMessage)
	}
	banner := pe.Error()
	if !strings.Contains(banner, "400") || !strings.Contains(banner, "Model cohere/north-mini-code requires higher max_tokens budget") {
		t.Fatalf("banner should carry status + raw message, got %q", banner)
	}
	if !strings.Contains(banner, "OpenRouter") {
		t.Fatalf("banner should name provider, got %q", banner)
	}
}

func TestParseProviderErrorInvalidKey(t *testing.T) {
	body := []byte(`{"error":{"message":"Invalid API key provided","type":"auth_error","code":"invalid_api_key"}}`)
	pe := ParseProviderError("openai", 401, body)
	if pe.RawMessage != "Invalid API key provided" {
		t.Fatalf("RawMessage = %q", pe.RawMessage)
	}
	if pe.Code != "invalid_api_key" {
		t.Fatalf("Code = %q", pe.Code)
	}
	if !strings.Contains(pe.Error(), "401") {
		t.Fatalf("banner missing status: %q", pe.Error())
	}
}

func TestFormatProviderErrorBanner(t *testing.T) {
	s := FormatProviderError("openrouter", 400, "Model cohere/north-mini-code requires higher max_tokens budget")
	if !strings.Contains(s, "400") || !strings.Contains(s, "requires higher max_tokens budget") {
		t.Fatalf("unexpected banner %q", s)
	}
}
