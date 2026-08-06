package capability

import "testing"

func TestResolveModelCapability(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		provider   string
		wantTier   ModelTier
		wantKind   ReasoningKind
		wantMax    int
		wantCoT    int
		wantBudget int
		wantLevel  string
	}{
		{
			name:     "cohere north mini is SLM with CoT cap",
			model:    "cohere/north-mini-code",
			provider: "openrouter",
			wantTier: TierSLM,
			wantKind: ReasoningKindCoT,
			wantMax:  512,
			wantCoT:  512,
		},
		{
			name:     "openai gpt-4o-mini is SLM",
			model:    "openai/gpt-4o-mini",
			provider: "openrouter",
			wantTier: TierSLM,
			wantKind: ReasoningKindCoT,
			wantMax:  512,
			wantCoT:  512,
		},
		{
			name:     "gemini flash is SLM",
			model:    "gemini-2.0-flash",
			provider: "google",
			wantTier: TierSLM,
			wantKind: ReasoningKindCoT,
			wantMax:  512,
			wantCoT:  512,
		},
		{
			name:     "local 7b coder is SLM",
			model:    "qwen2.5-coder:7b",
			provider: "ollama",
			wantTier: TierSLM,
			wantKind: ReasoningKindCoT,
			wantMax:  512,
			wantCoT:  512,
		},
		{
			name:       "claude 3.7 sonnet is frontier budget",
			model:      "anthropic/claude-3.7-sonnet",
			provider:   "openrouter",
			wantTier:   TierFrontier,
			wantKind:   ReasoningKindBudget,
			wantMax:    32768,
			wantBudget: 8192,
			wantLevel:  "high",
		},
		{
			name:     "claude opus is frontier budget",
			model:    "claude-opus-4",
			provider: "anthropic",
			wantTier: TierFrontier,
			wantKind: ReasoningKindBudget,
			wantMax:  32768,
		},
		{
			name:      "o3 is frontier level",
			model:     "o3",
			provider:  "openai",
			wantTier:  TierFrontier,
			wantKind:  ReasoningKindLevel,
			wantMax:   16384,
			wantLevel: "high",
		},
		{
			name:      "o3-mini keeps frontier reasoning family",
			model:     "openai/o3-mini",
			provider:  "openrouter",
			wantTier:  TierFrontier,
			wantKind:  ReasoningKindLevel,
			wantMax:   16384,
			wantLevel: "high",
		},
		{
			name:      "gpt-4o is frontier level",
			model:     "gpt-4o",
			provider:  "openai",
			wantTier:  TierFrontier,
			wantKind:  ReasoningKindLevel,
			wantMax:   16384,
			wantLevel: "high",
		},
		{
			name:      "gemini 2.5 pro is frontier level via provider inference",
			model:     "gemini-2.5-pro",
			wantTier:  TierFrontier,
			wantKind:  ReasoningKindLevel,
			wantMax:   8192,
			wantLevel: "high",
		},
		{
			name:     "claude haiku is SLM",
			model:    "claude-3.5-haiku",
			provider: "anthropic",
			wantTier: TierSLM,
			wantKind: ReasoningKindCoT,
			wantMax:  512,
			wantCoT:  512,
		},
		{
			name:     "mid llama is mid with no native reasoning",
			model:    "meta-llama/llama-3.3-70b",
			provider: "openrouter",
			wantTier: TierMid,
			wantKind: ReasoningKindNone,
			wantMax:  0,
		},
		{
			name:     "unknown model falls back to mid none",
			model:    "mystery-model-9000",
			wantTier: TierMid,
			wantKind: ReasoningKindNone,
			wantMax:  0,
		},
		{
			name:     "empty model is mid none",
			model:    "",
			wantTier: TierMid,
			wantKind: ReasoningKindNone,
			wantMax:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveModelCapability(tt.model, tt.provider)
			if got.Tier != tt.wantTier {
				t.Errorf("ResolveModelCapability(%q, %q).Tier = %v, want %v", tt.model, tt.provider, got.Tier, tt.wantTier)
			}
			if got.ReasoningKind != tt.wantKind {
				t.Errorf("ResolveModelCapability(%q, %q).ReasoningKind = %v, want %v", tt.model, tt.provider, got.ReasoningKind, tt.wantKind)
			}
			if got.MaxBudget != tt.wantMax {
				t.Errorf("ResolveModelCapability(%q, %q).MaxBudget = %d, want %d", tt.model, tt.provider, got.MaxBudget, tt.wantMax)
			}
			if tt.wantCoT > 0 && got.Default.CoTLimit != tt.wantCoT {
				t.Errorf("ResolveModelCapability(%q, %q).Default.CoTLimit = %d, want %d", tt.model, tt.provider, got.Default.CoTLimit, tt.wantCoT)
			}
			if tt.wantBudget > 0 && got.Default.BudgetTokens != tt.wantBudget {
				t.Errorf("ResolveModelCapability(%q, %q).Default.BudgetTokens = %d, want %d", tt.model, tt.provider, got.Default.BudgetTokens, tt.wantBudget)
			}
			if tt.wantLevel != "" && got.Default.Level != tt.wantLevel {
				t.Errorf("ResolveModelCapability(%q, %q).Default.Level = %q, want %q", tt.model, tt.provider, got.Default.Level, tt.wantLevel)
			}
		})
	}
}

func TestModelTierString(t *testing.T) {
	if got := TierSLM.String(); got != "slm" {
		t.Errorf("TierSLM.String() = %q, want slm", got)
	}
	if got := TierMid.String(); got != "mid" {
		t.Errorf("TierMid.String() = %q, want mid", got)
	}
	if got := TierFrontier.String(); got != "frontier" {
		t.Errorf("TierFrontier.String() = %q, want frontier", got)
	}
	if !TierSLM.IsSLM() || TierFrontier.IsSLM() {
		t.Error("IsSLM classification mismatch")
	}
	if !TierFrontier.IsFrontier() || TierMid.IsFrontier() {
		t.Error("IsFrontier classification mismatch")
	}
}

func TestReasoningControlIsZero(t *testing.T) {
	if !(ReasoningControl{}).IsZero() {
		t.Error("zero ReasoningControl should be IsZero")
	}
	if (ReasoningControl{Level: "low"}).IsZero() {
		t.Error("ReasoningControl with a level should not be IsZero")
	}
}
