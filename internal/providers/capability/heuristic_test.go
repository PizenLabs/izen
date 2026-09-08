package capability

import "testing"

func TestContextWindowFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		modelID string
		want    int
	}{
		{"gemini-2.0-flash", 1_000_000},
		{"claude-sonnet-4-20250514", 200_000},
		{"gpt-4o", 128_000},
		{"gpt-3.5-turbo", 16_385},
		{"o1", 200_000},
		{"o3-mini", 200_000},
		{"deepseek-v3", 128_000},
		{"deepseek-r1", 128_000},
		{"command-r-plus", 128_000},
		{"llama3.1:70b", 128_000},
		{"qwen2.5-coder:7b", 32_768},
		{"mistral:7b", 32_768},
		{"totally-unknown-model", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := ContextWindowFor(tt.modelID); got != tt.want {
			t.Errorf("ContextWindowFor(%q) = %d, want %d", tt.modelID, got, tt.want)
		}
	}
}

func TestMaxOutputTokensFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		modelID  string
		want     int
	}{
		{"openai", "o1", 32000},
		{"openai", "o3-mini", 32000},
		{"openai", "gpt-4o", 4096},
		{"openrouter", "openai/o1", 32000},
		{"openrouter", "openai/o3", 32000},
		{"anthropic", "claude-3-7-sonnet-20250219", 64000},
		{"openrouter", "anthropic/claude-sonnet-4", 64000},
		{"deepseek", "deepseek-r1", 65536},
		{"deepseek", "deepseek-chat", 65536},
		{"openai", "gemini-2.0-flash", 8192},
		{"unknown", "some-model", 4096},
	}
	for _, tt := range tests {
		if got := MaxOutputTokensFor(tt.provider, tt.modelID); got != tt.want {
			t.Errorf("MaxOutputTokensFor(%q, %q) = %d, want %d", tt.provider, tt.modelID, got, tt.want)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n    int
		want string
	}{
		{0, ""},
		{512, "512"},
		{999, "999"},
		{1000, "1k"},
		{4096, "4k"},
		{16385, "16k"},
		{128000, "128k"},
		{1000000, "1M"},
		{1500000, "1.5M"},
	}
	for _, tt := range tests {
		if got := FormatTokens(tt.n); got != tt.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
