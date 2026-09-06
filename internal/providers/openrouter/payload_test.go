package openrouter

import (
	"encoding/json"
	"strings"
	"testing"
)

// StreamOptions mirrors the provider's request option.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatCompletionRequest struct {
	Model         string         `json:"model"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// TestPayloadSerialization verifies the OpenRouter payload includes stream_options.
func TestPayloadSerialization(t *testing.T) {
	req := ChatCompletionRequest{
		Model:  "openai/gpt-4o",
		Stream: true,
	}
	if req.Stream {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"stream_options":{"include_usage":true}`) {
		t.Errorf("expected stream_options with include_usage:true, got %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"include_usage":true`) {
		t.Errorf("missing include_usage:true, got %s", jsonStr)
	}

	// Non-streaming should omit
	req2 := ChatCompletionRequest{
		Model:  "openai/gpt-4o",
		Stream: false,
	}
	data2, _ := json.Marshal(req2)
	if strings.Contains(string(data2), "stream_options") {
		t.Errorf("non-streaming should omit stream_options, got %s", string(data2))
	}
}
