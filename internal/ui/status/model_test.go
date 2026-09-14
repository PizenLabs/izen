package status

import (
	"strings"
	"testing"
)

func TestCompressModelSlug(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"qwen2.5-coder:7b", "qwen2.5-coder:7b"},
		{"cohere/north-mini-code:free", "cohere/north…ni-code"},
		{"model:latest", "model"},
		{"model:default", "model"},
		{"openrouter/cohere/north-code", "or/cohere/north-code"},
		{"github-copilot/gpt-4o", "copilot/gpt-4o"},
		{"ollama/qwen2.5-coder:7b", "qwen2.5-coder:7b"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CompressModelSlug(c.raw); got != c.want {
			t.Errorf("CompressModelSlug(%q) = %q, want %q", c.raw, got, c.want)
		}
	}

	// Middle truncation: capped at 20 cells with ….
	long := CompressModelSlug("openrouter/cohere/north-star-code-v2")
	if len([]rune(long)) > 20 {
		t.Errorf("long slug exceeds 20 cells: %q", long)
	}
	if !strings.Contains(long, "…") {
		t.Errorf("long slug missing ellipsis: %q", long)
	}
	if !strings.HasPrefix(long, "or/cohere/n") {
		t.Errorf("long slug bad head: %q", long)
	}
}
