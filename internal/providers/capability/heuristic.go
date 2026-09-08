package capability

import (
	"fmt"
	"strings"
)

// ContextWindowFor returns the model's maximum context window in tokens using
// a curated family heuristic. It is the deterministic fallback the registry
// applies when a provider does not advertise a context length. Returns 0 when
// the model family is unknown.
func ContextWindowFor(modelID string) int {
	lower := strings.ToLower(modelID)
	switch {
	case strings.Contains(lower, "gemini"):
		return 1_000_000
	case strings.Contains(lower, "claude"):
		return 200_000
	case strings.Contains(lower, "gpt-4"):
		return 128_000
	case strings.Contains(lower, "gpt-3.5"):
		return 16_385
	case strings.Contains(lower, "o1"), strings.Contains(lower, "o3"):
		return 200_000
	case strings.Contains(lower, "deepseek"):
		return 128_000
	case strings.Contains(lower, "north"), strings.Contains(lower, "command"):
		return 128_000
	case strings.Contains(lower, "llama"):
		return 128_000
	case strings.Contains(lower, "qwen2.5"):
		return 32_768
	case strings.Contains(lower, "mistral"):
		return 32_768
	default:
		return 0
	}
}

// MaxOutputTokensFor returns the model's maximum output token budget using a
// family heuristic. It is the deterministic fallback applied when a provider
// does not advertise a completion limit. The generic 4,096 default is
// conservative so the budget advisor flags overflow rather than silently
// over-allocating.
func MaxOutputTokensFor(provider, modelID string) int {
	lower := strings.ToLower(modelID)
	switch {
	case strings.Contains(lower, "claude-3-7-sonnet"),
		strings.Contains(lower, "claude-sonnet-4"),
		strings.Contains(lower, "claude-opus-4"):
		return 64000
	case strings.Contains(lower, "o1"), strings.Contains(lower, "o3"):
		return 32000
	case strings.Contains(lower, "deepseek"):
		return 65536
	case strings.Contains(lower, "gemini"):
		return 8192
	}
	if strings.HasPrefix(lower, "openai/o1") || strings.HasPrefix(lower, "openai/o3") {
		return 32000
	}
	if strings.HasPrefix(lower, "anthropic/claude") {
		return 64000
	}
	_ = provider // provider is consulted by adapters; the heuristic is ID-driven
	return 4096
}

// FormatTokens renders a token count in the compact UI form: exact integers
// under 1,000, "k"-suffixed thousands (16385 → "16k", 128000 → "128k"), and
// "M"-suffixed millions (1000000 → "1M").
func FormatTokens(n int) string {
	if n <= 0 {
		return ""
	}
	if n >= 1_000_000 {
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}
