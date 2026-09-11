package llm

import (
	"testing"
	"time"
)

// TestResolveTTFTTimeoutTiers pins the dynamic TTFT tiers: reasoning,
// heavy and free-tier models wait 45s–90s, standard/fast models fail fast
// at 15s–20s, and the user override wins over every tier.
func TestResolveTTFTTimeoutTiers(t *testing.T) {
	cases := []struct {
		name string
		spec ModelSpec
		want time.Duration
	}{
		{"standard cloud model", ModelSpec{Provider: "groq", ModelID: "llama-3.3-70b-versatile"}, TTFTStandardTimeout},
		{"fast flash model", ModelSpec{Provider: "gemini", ModelID: "gemini-2.5-flash"}, TTFTStandardTimeout},
		{"empty model", ModelSpec{Provider: "openai"}, TTFTStandardTimeout},
		{"local ollama model", ModelSpec{Provider: "ollama", ModelID: "qwen2.5-coder:7b"}, TTFTLocalTimeout},
		{"openai reasoning model", ModelSpec{Provider: "openai", ModelID: "o1"}, TTFTReasoningTimeout},
		{"openrouter reasoning vendor model", ModelSpec{Provider: "openrouter", ModelID: "openai/o3-mini"}, TTFTReasoningTimeout},
		{"explicit reasoning flag", ModelSpec{Provider: "custom", ModelID: "thinker-1", IsReasoning: true}, TTFTReasoningTimeout},
		{"reasoning low effort", ModelSpec{Provider: "openai", ModelID: "o3-mini", Variant: "low"}, TTFTReasoningLowTimeout},
		{"reasoning high effort", ModelSpec{Provider: "openai", ModelID: "o3-mini", Variant: "high"}, TTFTHeavyTimeout},
		{"reasoning max effort", ModelSpec{Provider: "openai", ModelID: "o1", Variant: "max"}, TTFTHeavyTimeout},
		{"free-tier suffix", ModelSpec{Provider: "openrouter", ModelID: "nex-agi/nex-n2.5-mini:free"}, TTFTHeavyTimeout},
		{"explicit free tier", ModelSpec{Provider: "openrouter", ModelID: "some/model", IsFreeTier: true}, TTFTHeavyTimeout},
		{"heavy variant on standard model", ModelSpec{Provider: "groq", ModelID: "llama-3.3-70b", Variant: "xhigh"}, TTFTReasoningTimeout},
		{"off variant stays standard", ModelSpec{Provider: "groq", ModelID: "llama-3.3-70b", Variant: "off"}, TTFTStandardTimeout},
		{"user override wins", ModelSpec{Provider: "openai", ModelID: "o1", TimeoutOverride: 5 * time.Second}, 5 * time.Second},
		{"user override extends fast model", ModelSpec{Provider: "groq", ModelID: "llama", TimeoutOverride: 2 * time.Minute}, 2 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveTTFTTimeout(tc.spec); got != tc.want {
				t.Errorf("ResolveTTFTTimeout(%+v) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}

// TestResolveTTFTTimeoutNeverNonPositive guards the footer countdown and
// the stream watchdog: a zero/negative override must fall through to the
// profile tiers, never surface as a deadline.
func TestResolveTTFTTimeoutNeverNonPositive(t *testing.T) {
	for _, override := range []time.Duration{0, -5 * time.Second} {
		got := ResolveTTFTTimeout(ModelSpec{Provider: "groq", ModelID: "llama", TimeoutOverride: override})
		if got <= 0 {
			t.Errorf("override %v yielded non-positive deadline %v", override, got)
		}
		if got != TTFTStandardTimeout {
			t.Errorf("override %v yielded %v, want standard %v", override, got, TTFTStandardTimeout)
		}
	}
}
