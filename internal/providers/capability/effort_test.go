package capability

import (
	"reflect"
	"testing"
)

func TestEffortLevelsForModel(t *testing.T) {
	t.Parallel()
	wantBase := []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh}
	wantExt := []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh}

	tests := []struct {
		name      string
		provider  string
		modelID   string
		reasoning bool
		want      []EffortLevel
	}{
		{"chat model never gets efforts", "openai", "gpt-4o", false, nil},
		{"chat model with reasoning flag still empty when false", "openai", "o3-mini", false, nil},
		{"openai o1 extended", "openai", "o1", true, wantExt},
		{"openai o3 extended", "openai", "o3-mini", true, wantExt},
		{"openrouter openai o3 extended", "openrouter", "openai/o3", true, wantExt},
		{"deepseek r1 extended", "deepseek", "deepseek-r1", true, wantExt},
		{"deepseek reasoner extended", "openrouter", "deepseek/deepseek-reasoner", true, wantExt},
		{"openrouter deepseek r1 extended", "openrouter", "deepseek/deepseek-r1", true, wantExt},
		{"claude reasoning base set", "anthropic", "claude-3-7-sonnet", true, wantBase},
		{"plain deepseek chat base set", "deepseek", "deepseek-chat", true, wantBase},
		{"empty id yields base set", "openai", "", true, wantBase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effortLevelsForModel(tt.provider, tt.modelID, tt.reasoning)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("effortLevelsForModel() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("returned slices are independent copies", func(t *testing.T) {
		a := effortLevelsForModel("openai", "o3", true)
		b := effortLevelsForModel("openai", "o3", true)
		a[0] = EffortXHigh
		if b[0] != EffortAuto {
			t.Error("callers must not share the package-level effort backing slice")
		}
	})
}

func TestSupportsEffortWithProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		modelID  string
		want     bool
	}{
		{"openai", "o1", true},
		{"openai", "o3-mini", true},
		{"openai", "gpt-4o", false},
		{"anthropic", "claude-3-7-sonnet-20250219", true},
		{"anthropic", "claude-sonnet-4-20250514", false},
		{"deepseek", "deepseek-r1", true},
		{"deepseek", "deepseek-chat", false},
		{"openrouter", "openai/o1", true},
		{"openrouter", "openai/o3-mini", true},
		{"openrouter", "deepseek/deepseek-r1", true},
		{"openrouter", "anthropic/claude-3-7-sonnet", true},
		{"openrouter", "anthropic/claude-sonnet-4", false},
		{"openrouter", "gpt-4o", false},
		{"ollama", "llama3.1:8b", false},
		{"ollama", "", false},
		{"openai", "", false},
	}
	for _, tt := range tests {
		if got := SupportsEffortWithProvider(tt.provider, tt.modelID); got != tt.want {
			t.Errorf("SupportsEffortWithProvider(%q, %q) = %v, want %v", tt.provider, tt.modelID, got, tt.want)
		}
	}
}

func TestHasReasoningParameter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		parameters []string
		want       bool
	}{
		{"reasoning_effort", []string{"temperature", "reasoning_effort", "max_completion_tokens"}, true},
		{"reasoning", []string{"reasoning"}, true},
		{"thinking", []string{"thinking"}, true},
		{"include_reasoning", []string{"include_reasoning"}, true},
		{"case insensitive", []string{"Reasoning_Effort"}, true},
		{"none", []string{"temperature", "top_p"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasReasoningParameter(tt.parameters); got != tt.want {
				t.Errorf("HasReasoningParameter(%v) = %v, want %v", tt.parameters, got, tt.want)
			}
		})
	}
}
