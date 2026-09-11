package ui

import "testing"

// 2-step activation footer badge: the active model renders with its selected
// reasoning variant in parentheses when non-empty and non-default.
func TestFormatModelWithVariant(t *testing.T) {
	cases := []struct {
		model, variant, want string
	}{
		{"cohere/north-mini-code:free", "medium", "cohere/north-mini-code:free (medium)"},
		{"cohere/north-mini-code:free", "high", "cohere/north-mini-code:free (high)"},
		{"cohere/north-mini-code:free", "", "cohere/north-mini-code:free"},
		{"cohere/north-mini-code:free", "default", "cohere/north-mini-code:free"},
		{"cohere/north-mini-code:free", "off", "cohere/north-mini-code:free"},
		{"cohere/north-mini-code:free", "none", "cohere/north-mini-code:free"},
		{"cohere/north-mini-code:free", "DEFAULT", "cohere/north-mini-code:free"},
	}
	for _, c := range cases {
		if got := formatModelWithVariant(c.model, c.variant); got != c.want {
			t.Errorf("formatModelWithVariant(%q,%q) = %q, want %q", c.model, c.variant, got, c.want)
		}
	}
}
