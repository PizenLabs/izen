package providers

import (
	"io"
	"strings"
	"testing"
)

// drainStream reads an SSE-backed stream result to EOF and returns the
// concatenated content bytes.
func drainStream(t *testing.T, r io.Reader) string {
	t.Helper()
	var got strings.Builder
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err == io.EOF {
			return got.String()
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
}

func openAICompatBody(content, finishReason string) string {
	sse := ""
	if content != "" {
		sse += "data: " + `{"choices":[{"delta":{"content":"` + content + `"}}]}` + "\n\n"
	}
	if finishReason != "" {
		sse += "data: " + `{"choices":[{"delta":{},"finish_reason":"` + finishReason + `"}]}` + "\n\n"
	}
	sse += "data: [DONE]\n\n"
	return sse
}

// TestOpenRouterStreamResult_FinishReason verifies the OpenRouter SSE reader
// surfaces the terminal finish_reason (the reported 78-token "length" truncation
// wall) instead of silently dropping it.
func TestOpenRouterStreamResult_FinishReason(t *testing.T) {
	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("Hello world", "length")))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "Hello world" {
		t.Errorf("content = %q, want %q", got, "Hello world")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

func TestOpenRouterStreamResult_FinishReason_Stop(t *testing.T) {
	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("done", "stop")))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "done" {
		t.Errorf("content = %q, want %q", got, "done")
	}
	if got := res.FinishReason(); got != "stop" {
		t.Errorf("FinishReason() = %q, want %q", got, "stop")
	}
}

func TestOpenRouterStreamResult_FinishReason_None(t *testing.T) {
	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("plain", "")))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	drainStream(t, res)
	if got := res.FinishReason(); got != "" {
		t.Errorf("FinishReason() = %q, want empty", got)
	}
}

// TestOpenAIStreamResult_FinishReason verifies the OpenAI-compatible readers
// capture the terminal finish_reason as well.
func TestOpenAIStreamResult_FinishReason(t *testing.T) {
	sr := &openaiSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("hi", "length")))}
	res := &OpenAIStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

func TestGroqStreamResult_FinishReason(t *testing.T) {
	sr := &groqSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("hi", "length")))}
	res := &GroqStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

func TestNineRouterStreamResult_FinishReason(t *testing.T) {
	sr := &ninerouterSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("hi", "length")))}
	res := &NineRouterStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

func TestOpenCodeStreamResult_FinishReason(t *testing.T) {
	sr := &opencodeSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("hi", "length")))}
	res := &OpenCodeStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

func TestOllamaStreamResult_FinishReason(t *testing.T) {
	sr := &sseReader{body: io.NopCloser(strings.NewReader(openAICompatBody("hi", "length")))}
	res := &StreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

// TestClaudeStreamResult_FinishReason verifies the Anthropic stop_reason
// "max_tokens" (completion ceiling) is normalized to "length".
func TestClaudeStreamResult_FinishReason(t *testing.T) {
	body := "" +
		"data: " + `{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial answer"}}` + "\n\n" +
		"data: " + `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":78}}` + "\n\n" +
		"data: " + `{"type":"message_stop"}` + "\n\n"

	sr := &claudeSSEReader{body: io.NopCloser(strings.NewReader(body))}
	res := &ClaudeStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "partial answer" {
		t.Errorf("content = %q, want %q", got, "partial answer")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

func TestClaudeStreamResult_FinishReason_EndTurn(t *testing.T) {
	body := "" +
		"data: " + `{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"data: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" +
		"data: " + `{"type":"message_stop"}` + "\n\n"

	sr := &claudeSSEReader{body: io.NopCloser(strings.NewReader(body))}
	res := &ClaudeStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "ok" {
		t.Errorf("content = %q, want %q", got, "ok")
	}
	if got := res.FinishReason(); got != "end_turn" {
		t.Errorf("FinishReason() = %q, want %q", got, "end_turn")
	}
}

// TestGeminiStreamResult_FinishReason verifies the Gemini finishReason
// "MAX_TOKENS" (completion ceiling) is normalized to "length".
func TestGeminiStreamResult_FinishReason(t *testing.T) {
	body := "" +
		"data: " + `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"truncated answer"}]}}]}` + "\n\n"

	sr := &geminiSSEReader{body: io.NopCloser(strings.NewReader(body))}
	res := &GeminiStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "truncated answer" {
		t.Errorf("content = %q, want %q", got, "truncated answer")
	}
	if got := res.FinishReason(); got != "length" {
		t.Errorf("FinishReason() = %q, want %q", got, "length")
	}
}

func TestGeminiStreamResult_FinishReason_Stop(t *testing.T) {
	body := "" +
		"data: " + `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"done"}]}}]}` + "\n\n"

	sr := &geminiSSEReader{body: io.NopCloser(strings.NewReader(body))}
	res := &GeminiStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "done" {
		t.Errorf("content = %q, want %q", got, "done")
	}
	if got := res.FinishReason(); got != "STOP" {
		t.Errorf("FinishReason() = %q, want %q", got, "STOP")
	}
}
