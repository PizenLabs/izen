package decision

import (
	"testing"

	"github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/domain/intent"
)

func mustCap(tier capability.ModelTier, kind capability.ReasoningKind, maxBudget int) capability.ModelCapability {
	def := capability.ReasoningControl{}
	switch kind {
	case capability.ReasoningKindCoT:
		def = capability.ReasoningControl{CoTLimit: 512}
	case capability.ReasoningKindBudget:
		def = capability.ReasoningControl{Level: "high", BudgetTokens: 8192}
	case capability.ReasoningKindLevel:
		def = capability.ReasoningControl{Level: "high"}
	}
	return capability.ModelCapability{
		Tier:          tier,
		ReasoningKind: kind,
		MaxBudget:     maxBudget,
		Default:       def,
	}
}

func intentOf(prompt string) *intent.UserIntent {
	return intent.New(prompt)
}

// ── AUTO EFFORT: complexity × tier ─────────────────────────────────────────

func TestAutoEffortSLMLowComplexity(t *testing.T) {
	cap := mustCap(capability.TierSLM, capability.ReasoningKindCoT, 512)
	got := ResolveEffortConfig(intentOf("add a README license comment"), cap, "auto")
	if got.Level != "low" {
		t.Errorf("Level = %q, want low", got.Level)
	}
	if got.CoTLimit != 512 {
		t.Errorf("CoTLimit = %d, want 512", got.CoTLimit)
	}
}

func TestAutoEffortSLMHighComplexity(t *testing.T) {
	cap := mustCap(capability.TierSLM, capability.ReasoningKindCoT, 512)
	got := ResolveEffortConfig(intentOf("migrate the database schema across all services"), cap, "auto")
	if got.Level != "medium" {
		t.Errorf("Level = %q, want medium", got.Level)
	}
	if got.CoTLimit != 1024 {
		t.Errorf("CoTLimit = %d, want 1024", got.CoTLimit)
	}
}

func TestAutoEffortFrontierHighComplexity(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(intentOf("redesign the distributed pipeline architecture"), cap, "auto")
	if got.Level != "xhigh" {
		t.Errorf("Level = %q, want xhigh", got.Level)
	}
	if got.BudgetTokens != 32768 {
		t.Errorf("BudgetTokens = %d, want 32768 (MaxBudget)", got.BudgetTokens)
	}
}

func TestAutoEffortFrontierLowComplexity(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(intentOf("fix typo in the comment"), cap, "auto")
	if got.Level != "medium" {
		t.Errorf("Level = %q, want medium", got.Level)
	}
	if got.BudgetTokens != 8192 {
		t.Errorf("BudgetTokens = %d, want 8192", got.BudgetTokens)
	}
}

func TestAutoEffortMidMediumComplexity(t *testing.T) {
	cap := mustCap(capability.TierMid, capability.ReasoningKindNone, 0)
	got := ResolveEffortConfig(intentOf("add a utility function"), cap, "auto")
	if got.Level != "medium" {
		t.Errorf("Level = %q, want medium", got.Level)
	}
	if got.BudgetTokens != 4096 {
		t.Errorf("BudgetTokens = %d, want 4096", got.BudgetTokens)
	}
}

// ── MANUAL OVERRIDE: dynamic native mapping ───────────────────────────────

func TestManualLevelOnLevelModel(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindLevel, 16384)
	got := ResolveEffortConfig(intentOf("anything"), cap, "xhigh")
	if got.Level != "xhigh" {
		t.Errorf("Level = %q, want xhigh", got.Level)
	}
}

func TestManualLevelOnBudgetModel(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(intentOf("anything"), cap, "high")
	if got.BudgetTokens != 8192 {
		t.Errorf("BudgetTokens = %d, want 8192 (mapped from high)", got.BudgetTokens)
	}
}

func TestManualBudgetOnBudgetModel(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(intentOf("anything"), cap, "16k")
	if got.BudgetTokens != 16000 {
		t.Errorf("BudgetTokens = %d, want 16000", got.BudgetTokens)
	}
}

func TestManualBudgetOnLevelModel(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindLevel, 16384)
	got := ResolveEffortConfig(intentOf("anything"), cap, "8192")
	if got.Level != "high" {
		t.Errorf("Level = %q, want high (mapped from 8192)", got.Level)
	}
}

func TestManualBudgetClampedToMax(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 8192)
	got := ResolveEffortConfig(intentOf("anything"), cap, "64k")
	if got.BudgetTokens != 8192 {
		t.Errorf("BudgetTokens = %d, want 8192 (clamped to MaxBudget)", got.BudgetTokens)
	}
}

func TestManualCoTOnSLM(t *testing.T) {
	cap := mustCap(capability.TierSLM, capability.ReasoningKindCoT, 512)
	got := ResolveEffortConfig(intentOf("anything"), cap, "200")
	if got.CoTLimit != 200 {
		t.Errorf("CoTLimit = %d, want 200", got.CoTLimit)
	}
}

func TestManualCoTClampedToSLMMax(t *testing.T) {
	cap := mustCap(capability.TierSLM, capability.ReasoningKindCoT, 512)
	got := ResolveEffortConfig(intentOf("anything"), cap, "high")
	if got.CoTLimit != SLMMaxCoTLimit {
		t.Errorf("CoTLimit = %d, want %d (levelToBudget(high) capped at SLMMaxCoTLimit)", got.CoTLimit, SLMMaxCoTLimit)
	}
}

func TestManualOffDisablesReasoning(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(intentOf("anything"), cap, "off")
	if got.Level != "" || got.BudgetTokens != 0 || got.CoTLimit != 0 {
		t.Errorf("off should zero all controls, got %+v", got)
	}
}

func TestUnknownSettingFallsBackToDefault(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(intentOf("anything"), cap, "bogus-setting")
	if got.BudgetTokens != 8192 {
		t.Errorf("BudgetTokens = %d, want 8192 (default control)", got.BudgetTokens)
	}
}

// ── TIER PRESERVATION ─────────────────────────────────────────────────────

func TestEffortCarriesTier(t *testing.T) {
	cap := mustCap(capability.TierSLM, capability.ReasoningKindCoT, 512)
	got := ResolveEffortConfig(intentOf("anything"), cap, "auto")
	if got.Tier != capability.TierSLM {
		t.Errorf("Tier = %v, want TierSLM", got.Tier)
	}
}

func TestNilIntentDefaultsToMedium(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(nil, cap, "auto")
	if got.Level != "high" {
		t.Errorf("Level = %q, want high (medium complexity frontier)", got.Level)
	}
}

func TestDescription(t *testing.T) {
	cap := mustCap(capability.TierFrontier, capability.ReasoningKindBudget, 32768)
	got := ResolveEffortConfig(intentOf("anything"), cap, "high")
	if got.Description() != "budget=8192" {
		t.Errorf("Description() = %q, want budget=8192", got.Description())
	}
}

// ── PARSER EDGE CASES ─────────────────────────────────────────────────────

func TestParseBudget(t *testing.T) {
	tests := []struct {
		in     string
		wantN  int
		wantOK bool
	}{
		{"512", 512, true},
		{"16k", 16000, true},
		{"16K", 16000, true},
		{"", 0, false},
		{"abc", 0, false},
		{"0", 0, false},
		{"k", 0, false},
	}
	for _, tt := range tests {
		gotN, gotOK := parseBudget(tt.in)
		if gotN != tt.wantN || gotOK != tt.wantOK {
			t.Errorf("parseBudget(%q) = (%d, %v), want (%d, %v)", tt.in, gotN, gotOK, tt.wantN, tt.wantOK)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for _, ok := range []string{"low", "medium", "high", "xhigh", "max", "minimal"} {
		if _, isOK := parseLevel(ok); !isOK {
			t.Errorf("parseLevel(%q) should be recognized", ok)
		}
	}
	if _, isOK := parseLevel("turbo"); isOK {
		t.Error("parseLevel(turbo) should not be recognized")
	}
}
