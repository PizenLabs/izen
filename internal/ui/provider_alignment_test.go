package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/llm"
)

func mkAlignmentModel(provider, id string) llm.ModelInfo {
	sup := llm.ModelSupportsEffortWithProvider(provider, id)
	m := llm.ModelInfo{
		ID:                id,
		Name:              id,
		Provider:          provider,
		SupportsReasoning: &sup,
		ContextWindow:     llm.ContextWindowFor(id),
		MaxOutputTokens:   64000,
	}
	m.SyncNativeBounds()
	return m
}

func TestProviderAlignmentClaudeVsGPT(t *testing.T) {
	mp := NewModelPickerModal()
	mp.SetSize(80, 24)

	// Reasoning model with custom variants: Claude exposes exact strings.
	claude := mkAlignmentModel("openrouter", "anthropic/claude-3-7-sonnet-20250219")
	mp.models = []llm.ModelInfo{claude}
	mp.filtered = []llm.ModelInfo{claude}
	// Row 0 is the provider header; row 1 is the model item.
	mp.cursor = 1
	mp.effortIdx = 0

	variants := VariantsForModel(claude)
	want := []string{"default", "minimal", "low", "medium", "high", "xhigh"}
	if len(variants) != len(want) {
		t.Fatalf("claude variants = %v, want %v", variants, want)
	}
	for i := range want {
		if variants[i] != want[i] {
			t.Fatalf("claude variants = %v, want %v", variants, want)
		}
	}
	if !mp.shouldShowEffort() {
		t.Fatal("variant selector should be visible for Claude")
	}
	view := mp.renderEffortSlider()
	for _, w := range want {
		if !strings.Contains(view, w) {
			t.Fatalf("variant slider should render %q, got:\n%s", w, view)
		}
	}
	// Dynamic budget: slider must show computed tokens, no hardcoded "2k tokens".
	if strings.Contains(view, "2k tokens") {
		t.Fatalf("slider must not contain hardcoded 2k tokens string:\n%s", view)
	}
	if !strings.Contains(view, "Thinking Budget") && !strings.Contains(view, "Total Max") {
		t.Fatalf("slider should show dynamic budgets, got:\n%s", view)
	}

	// Standard model: GPT-4o-mini hides the selector entirely.
	gpt := mkAlignmentModel("openai", "gpt-4o-mini")
	mp.models = []llm.ModelInfo{gpt}
	mp.filtered = []llm.ModelInfo{gpt}
	mp.cursor = 1
	mp.effortIdx = 0
	if got := VariantsForModel(gpt); len(got) != 0 {
		t.Fatalf("gpt-4o-mini variants should be empty, got %v", got)
	}
	if mp.shouldShowEffort() {
		t.Fatal("variant selector should be hidden for GPT-4o-mini")
	}
	if s := mp.renderEffortSlider(); s != "" {
		t.Fatalf("slider should be empty for GPT-4o-mini, got %q", s)
	}
}
