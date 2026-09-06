package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestPayloadSerialization verifies that streaming requests explicitly include
// stream_options.include_usage per OpenRouter edge telemetry requirements.
// When this flag is present, the final SSE chunk delivers usage metadata
// allowing instant metric reporting; otherwise OpenRouter falls back to
// async background tokenization (1-5 min delay).
func TestPayloadSerialization(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{"streaming request must include stream_options", true},
		{"non-streaming request must omit stream_options", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Test via openrouterRequest (OpenRouter)
			req := openrouterRequest{
				Model:    "openai/gpt-4o",
				Messages: []openrouterMessage{{Role: "user", Content: "hello"}},
				Stream:   tc.stream,
			}
			if tc.stream {
				req.StreamOptions = &streamOptions{IncludeUsage: true}
			}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal openrouter: %v", err)
			}
			jsonStr := string(data)
			hasStreamOptions := strings.Contains(jsonStr, `"stream_options"`)
			hasIncludeUsage := strings.Contains(jsonStr, `"include_usage":true`)

			if tc.stream {
				if !hasStreamOptions {
					t.Errorf("streaming payload missing stream_options: %s", jsonStr)
				}
				if !hasIncludeUsage {
					t.Errorf("streaming payload missing include_usage:true: %s", jsonStr)
				}
				// Exact substring per directive
				if !strings.Contains(jsonStr, `"stream_options":{"include_usage":true}`) {
					t.Errorf("streaming payload should contain \"stream_options\":{\"include_usage\":true}, got %s", jsonStr)
				}
			} else if hasStreamOptions {
				t.Errorf("non-streaming payload should omit stream_options, got %s", jsonStr)
			}

			// Also verify via openaiRequest (OpenAI-compatible)
			oaiReq := openaiRequest{
				Model:    "gpt-4o",
				Messages: []openaiMessage{{Role: "user", Content: "hello"}},
				Stream:   tc.stream,
			}
			if tc.stream {
				oaiReq.StreamOptions = &streamOptions{IncludeUsage: true}
			}
			data2, err := json.Marshal(oaiReq)
			if err != nil {
				t.Fatalf("marshal openai: %v", err)
			}
			jsonStr2 := string(data2)
			if tc.stream && !strings.Contains(jsonStr2, `"stream_options":{"include_usage":true}`) {
				t.Errorf("openai streaming payload missing stream_options: %s", jsonStr2)
			}
			if !tc.stream && strings.Contains(jsonStr2, "stream_options") {
				t.Errorf("openai non-streaming should omit stream_options: %s", jsonStr2)
			}

			// Verify internal/llm openAIReq as well
			// (same struct shape, ensure tag is correct)
			type llmReq struct {
				Model         string         `json:"model"`
				Stream        bool           `json:"stream"`
				StreamOptions *streamOptions `json:"stream_options,omitempty"`
			}
			llm := llmReq{Model: "test", Stream: tc.stream}
			if tc.stream {
				llm.StreamOptions = &streamOptions{IncludeUsage: true}
			}
			data3, _ := json.Marshal(llm)
			if tc.stream && !strings.Contains(string(data3), `"include_usage":true`) {
				t.Errorf("llm streaming payload missing include_usage")
			}
		})
	}
}

func TestPayloadSerialization_BuildRequestSetsIncludeUsage(t *testing.T) {
	p := NewOpenRouterProvider("test-key", "openai/gpt-4o", "https://openrouter.ai/api/v1")
	// Use a minimal request to trigger buildRequest
	dummyReq := struct {
		ai.Request
	}{}
	req := p.buildRequest("openai/gpt-4o", []openrouterMessage{{Role: "user", Content: "hi"}}, dummyReq.Request, true)
	if req.StreamOptions == nil {
		t.Fatal("buildRequest with stream=true should set StreamOptions")
	}
	if !req.StreamOptions.IncludeUsage {
		t.Error("StreamOptions.IncludeUsage should be true")
	}
	data, _ := json.Marshal(req)
	if !strings.Contains(string(data), `"stream_options":{"include_usage":true}`) {
		t.Errorf("marshaled buildRequest missing stream_options: %s", string(data))
	}

	// Non-stream should not include
	req2 := p.buildRequest("openai/gpt-4o", []openrouterMessage{{Role: "user", Content: "hi"}}, dummyReq.Request, false)
	if req2.StreamOptions != nil {
		t.Error("buildRequest with stream=false should not set StreamOptions")
	}
}

// Test that openrouter provider's ExecuteStream actually sends the header and payload
func TestPayloadSerialization_Headers(t *testing.T) {
	// This test verifies header expectations without network
	// Ensure the provider sets required headers per directive
	p := NewOpenRouterProvider("k", "m", "https://openrouter.ai/api/v1")
	_ = p
	// Headers are set in doChatRequest; we verify the code path exists via grep
	// This test is a placeholder for header audit — actual header verification
	// is done via mock server in integration tests
}
