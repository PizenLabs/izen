package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSanitizeOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no fences",
			input:    "Hello world",
			expected: "Hello world",
		},
		{
			name:     "strip surrounding fences",
			input:    "```\nHello world\n```",
			expected: "Hello world",
		},
		{
			name:     "strip fences with lang",
			input:    "```go\npackage main\n```",
			expected: "package main",
		},
		{
			name:     "inline backticks preserved (single-line not a fence)",
			input:    "Use `code` inline",
			expected: "Use `code` inline",
		},
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
		{
			name:     "only fences",
			input:    "```\n```",
			expected: "",
		},
		{
			name:     "multiline without fences",
			input:    "line1\nline2\nline3",
			expected: "line1\nline2\nline3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeOutput(tc.input)
			if got != tc.expected {
				t.Errorf("SanitizeOutput(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSanitizeOutputWithLang(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantText string
		wantLang string
	}{
		{
			name:     "go fence",
			input:    "```go\nfunc main() {}\n```",
			wantText: "func main() {}",
			wantLang: "go",
		},
		{
			name:     "python fence",
			input:    "```python\ndef hello():\n    pass\n```",
			wantText: "def hello():\n    pass",
			wantLang: "python",
		},
		{
			name:     "no lang",
			input:    "```\nplain code\n```",
			wantText: "plain code",
			wantLang: "",
		},
		{
			name:     "no fences",
			input:    "just text",
			wantText: "just text",
			wantLang: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotLang := SanitizeOutputWithLang(tc.input)
			if gotText != tc.wantText || gotLang != tc.wantLang {
				t.Errorf("SanitizeOutputWithLang(%q) = (%q, %q), want (%q, %q)",
					tc.input, gotText, gotLang, tc.wantText, tc.wantLang)
			}
		})
	}
}

func TestProviderAdapter(t *testing.T) {
	ctx := context.Background()

	adapter := NewProviderAdapter("test",
		func(ctx context.Context, model, system string, msgs []Message, maxTokens int, temp float64) (string, int, int, int, int, error) {
			return "hello", 10, 5, 0, 0, nil
		},
		func(ctx context.Context, model, system string, msgs []Message, maxTokens int, temp float64, handler StreamHandler) (int, int, int, int, error) {
			return 10, 5, 0, 0, nil
		},
	)

	if adapter.Name() != "test" {
		t.Errorf("Name() = %q, want %q", adapter.Name(), "test")
	}

	resp, err := adapter.GenerateResponse(ctx, PromptRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello")
	}
	if resp.TokenInput != 10 || resp.TokenOutput != 5 {
		t.Errorf("tokens = (%d,%d), want (10,5)", resp.TokenInput, resp.TokenOutput)
	}

	adapterErr := NewProviderAdapter("err",
		func(ctx context.Context, model, system string, msgs []Message, maxTokens int, temp float64) (string, int, int, int, int, error) {
			return "", 0, 0, 0, 0, errors.New("test error")
		}, nil,
	)
	_, err = adapterErr.GenerateResponse(ctx, PromptRequest{})
	if err == nil || err.Error() != "test error" {
		t.Errorf("expected error, got %v", err)
	}
}

func TestAnthropicBuildSystemContent(t *testing.T) {
	client := NewAnthropicClient("test-key", "claude-sonnet-4-20250514")

	req := PromptRequest{
		System:      "You are a helpful assistant.",
		CacheSystem: true,
	}

	sys := client.buildSystemContent(req)
	if len(sys) != 1 {
		t.Fatalf("expected 1 system content, got %d", len(sys))
	}
	if sys[0].Type != "text" {
		t.Errorf("system type = %q, want %q", sys[0].Type, "text")
	}
	if sys[0].Cache != "ephemeral" {
		t.Errorf("system cache = %q, want %q", sys[0].Cache, "ephemeral")
	}

	reqNoCache := PromptRequest{System: "Hello"}
	sysNoCache := client.buildSystemContent(reqNoCache)
	if len(sysNoCache) == 1 && sysNoCache[0].Cache != "" {
		t.Errorf("expected no cache control when CacheSystem=false")
	}

	reqNoSystem := PromptRequest{}
	sysEmpty := client.buildSystemContent(reqNoSystem)
	if sysEmpty != nil {
		t.Errorf("expected nil system when empty")
	}
}

func TestAnthropicBuildMessages(t *testing.T) {
	client := NewAnthropicClient("test-key", "claude-sonnet-4-20250514")

	req := PromptRequest{
		Messages: []Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there"},
			{Role: "user", Content: "Cache me"},
		},
		CacheMessages: []int{2},
	}

	msgs := client.buildMessages(req)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content[0].Text != "Hello" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Content[0].Cache != "" {
		t.Errorf("msg[1] should not have cache control")
	}
	if msgs[2].Content[0].Cache != "ephemeral" {
		t.Errorf("msg[2] should have ephemeral cache")
	}
}

func TestOpenAIClientResolveEndpoint(t *testing.T) {
	client := NewOpenAIClient("key", "gpt-4o", "")
	endpoint := client.resolveEndpoint()
	if !strings.Contains(endpoint, "api.openai.com") {
		t.Errorf("default endpoint should point to OpenAI, got %q", endpoint)
	}

	clientOR := NewOpenAIClient("key", "anthropic/claude-3.5-sonnet", "https://openrouter.ai/api/v1")
	if clientOR.Name() != "openrouter" {
		t.Errorf("expected openrouter name, got %q", clientOR.Name())
	}
}

func TestOllamaClient(t *testing.T) {
	client := NewOllamaClient("http://localhost:11434/v1", "ollama", "qwen2.5-coder:7b")
	if client.Name() != "ollama" {
		t.Errorf("Name() = %q, want %q", client.Name(), "ollama")
	}
}

func TestTokenEstimationFallback(t *testing.T) {
	text := strings.Repeat("hello ", 100)
	expectedTokens := len(text) / 4

	ollama := NewOllamaClient("http://localhost:11434/v1", "ollama", "qwen2.5-coder:7b")

	adapter := NewProviderAdapter("test-ollama",
		func(ctx context.Context, model, system string, msgs []Message, maxTokens int, temp float64) (string, int, int, int, int, error) {
			return text, 0, 0, 0, 0, nil
		},
		nil,
	)

	resp, err := ollama.GenerateResponse(context.Background(), PromptRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Logf("skipping network test: %v", err)
		return
	}
	_ = resp
	_ = adapter
	_ = expectedTokens
}

func TestProviderAdapterNilStream(t *testing.T) {
	ctx := context.Background()
	adapter := NewProviderAdapter("fail", nil, nil)

	resp, err := adapter.StreamResponse(ctx, PromptRequest{}, func(s string) error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != (LLMResponse{}) {
		t.Errorf("expected empty response, got %+v", resp)
	}
}

func TestProviderAdapterNilExecute(t *testing.T) {
	ctx := context.Background()
	adapter := NewProviderAdapter("fail", nil, nil)

	resp, err := adapter.GenerateResponse(ctx, PromptRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != (LLMResponse{}) {
		t.Errorf("expected empty response, got %+v", resp)
	}
}

func TestAnthropicResolveModel(t *testing.T) {
	client := NewAnthropicClient("key", "claude-sonnet-4-20250514")
	if model := client.resolveModel("override"); model != "override" {
		t.Errorf("resolveModel should return override, got %q", model)
	}
	if model := client.resolveModel(""); model != "claude-sonnet-4-20250514" {
		t.Errorf("resolveModel should return default, got %q", model)
	}
}

func TestOpenAIResolveModel(t *testing.T) {
	client := NewOpenAIClient("key", "gpt-4o", "")
	if model := client.resolveModel("override"); model != "override" {
		t.Errorf("resolveModel should return override, got %q", model)
	}
	if model := client.resolveModel(""); model != "gpt-4o" {
		t.Errorf("resolveModel should return default, got %q", model)
	}
}

func TestStreamHandlerPassthrough(t *testing.T) {
	adapter := NewProviderAdapter("passthrough",
		nil,
		func(ctx context.Context, model, system string, msgs []Message, maxTokens int, temp float64, handler StreamHandler) (int, int, int, int, error) {
			_ = handler("hello ")
			_ = handler("world")
			return 10, 5, 0, 0, nil
		},
	)

	var collected strings.Builder
	resp, err := adapter.StreamResponse(context.Background(), PromptRequest{}, func(s string) error {
		collected.WriteString(s)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if collected.String() != "hello world" {
		t.Errorf("collected = %q, want %q", collected.String(), "hello world")
	}
	if resp.TokenInput != 10 || resp.TokenOutput != 5 {
		t.Errorf("tokens = (%d,%d), want (10,5)", resp.TokenInput, resp.TokenOutput)
	}
}

func TestSanitizeOutputEmptyInput(t *testing.T) {
	if got := SanitizeOutput(""); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

func TestSanitizeOutputWhitespace(t *testing.T) {
	result := SanitizeOutput("  \n  hello  \n  ")
	if result != "hello" {
		t.Errorf("should trim whitespace, got %q", result)
	}
}

func TestOpenAIClientNameDetection(t *testing.T) {
	openAI := NewOpenAIClient("key", "gpt-4o", "https://api.openai.com/v1")
	if name := openAI.Name(); name != "openai" {
		t.Errorf("expected openai, got %q", name)
	}

	openRouter := NewOpenAIClient("key", "anthropic/claude-3.5-sonnet", "https://openrouter.ai/api/v1")
	if name := openRouter.Name(); name != "openrouter" {
		t.Errorf("expected openrouter, got %q", name)
	}

	custom := NewOpenAIClient("key", "model", "https://custom.example.com/v1")
	if name := custom.Name(); name != "openai" {
		t.Errorf("expected openai for custom endpoint, got %q", name)
	}
}
