package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtraParamsPassthrough(t *testing.T) {
	body := openrouterRequest{
		Model:       "openai/gpt-4o-mini",
		Messages:    []openrouterMessage{{Role: "user", Content: "hi"}},
		MaxTokens:   100,
		ExtraParams: map[string]any{"top_k": 40, "min_p": 0.05},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["top_k"] != float64(40) {
		t.Fatalf("top_k not merged: %v", m)
	}
	if m["model"] != "openai/gpt-4o-mini" {
		t.Fatalf("native model lost: %v", m)
	}
	// Native keys win over ExtraParams collisions.
	body2 := openaiRequest{Model: "gpt-4o", ExtraParams: map[string]any{"model": "evil"}}
	raw2, _ := json.Marshal(body2)
	var m2 map[string]any
	_ = json.Unmarshal(raw2, &m2)
	if m2["model"] != "gpt-4o" {
		t.Fatalf("native key should win, got %v", m2)
	}
}

func TestRawErrorPipelineBanner(t *testing.T) {
	raw := []byte(`{"error":{"message":"Model cohere/north-mini-code requires higher max_tokens budget","type":"invalid_request_error"}}`)
	pe := NewProviderError("openrouter", 400, raw)
	if pe.RawMessage != "Model cohere/north-mini-code requires higher max_tokens budget" {
		t.Fatalf("RawMessage = %q", pe.RawMessage)
	}
	banner := FormatProviderError("openrouter", 400, pe.RawMessage)
	if !strings.Contains(banner, "400") || !strings.Contains(banner, "requires higher max_tokens budget") {
		t.Fatalf("banner = %q", banner)
	}
}
