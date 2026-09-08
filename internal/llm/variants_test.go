package llm

import (
	"strings"
	"testing"
)

func TestVariantsForStandardModelHidden(t *testing.T) {
	if v := VariantsFor("openai", "gpt-4o-mini"); v != nil {
		t.Fatalf("gpt-4o-mini should expose no variants, got %v", v)
	}
	if v := VariantsFor("openrouter", "openai/gpt-4o-mini"); v != nil {
		t.Fatalf("openrouter gpt-4o-mini should expose no variants, got %v", v)
	}
}

func TestVariantsForClaudeExactStrings(t *testing.T) {
	want := []string{"default", "minimal", "low", "medium", "high", "xhigh"}
	got := VariantsFor("openrouter", "anthropic/claude-3-7-sonnet-20250219")
	if len(got) != len(want) {
		t.Fatalf("claude variants = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("claude variants = %v, want %v", got, want)
		}
	}
}

func TestCapabilitiesSyncNativeBounds(t *testing.T) {
	sup := true
	m := ModelInfo{
		ID:                "anthropic/claude-3-7-sonnet-20250219",
		Provider:          "openrouter",
		SupportsReasoning: &sup,
		ContextWindow:     200000,
		MaxOutputTokens:   64000,
	}
	m.SyncNativeBounds()
	if m.MaxContextTokens != 200000 || m.MaxCompletionTokens != 64000 {
		t.Fatalf("native bounds not synced: %+v", m)
	}
	caps := m.Capabilities()
	if !caps.SupportsReasoning {
		t.Fatal("Capabilities should report SupportsReasoning=true")
	}
	if len(caps.Variants) == 0 {
		t.Fatal("Capabilities.Variants should be populated for Claude")
	}
	if caps.MaxCompletionTokens != 64000 {
		t.Fatalf("MaxCompletionTokens = %d, want 64000", caps.MaxCompletionTokens)
	}
}

func TestVariantBudgetDynamic(t *testing.T) {
	if got := VariantBudget("medium", 64000); got != 32000 {
		t.Fatalf("medium budget = %d, want 32000", got)
	}
	if got := VariantBudget("xhigh", 64000); got != int(0.95*64000) {
		t.Fatalf("xhigh budget = %d", got)
	}
	if got := VariantBudget("default", 64000); got != 0 {
		t.Fatalf("default budget should be 0, got %d", got)
	}
	// Dynamic scaling: same variant, different native bound.
	if strings.TrimSpace("") != "" {
		t.Fatal("unreachable")
	}
	if VariantBudget("low", 32000) == VariantBudget("low", 64000) {
		t.Fatal("budgets must scale with MaxCompletionTokens")
	}
}
