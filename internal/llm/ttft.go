// Dynamic TTFT (Time-To-First-Token) timeout resolution.
//
// The pre-first-byte wait is the only phase where a fixed deadline makes
// sense: once bytes flow, liveness is governed by the inter-token idle
// watchdog instead. A single 15s budget is wrong in both directions —
// reasoning models and high-latency free tiers legitimately need 45-90s
// before the first token, while fast standard models should fail fast at
// 15-20s so a hung socket never holds the turn hostage.
//
// ResolveTTFTTimeout maps a ModelSpec onto its deadline. It is pure string
// logic with zero I/O (safe to call on every footer frame): reasoning
// detection reuses the package's offline heuristic, never the
// registry/cache-backed resolver.
package llm

import (
	"strings"
	"time"
)

// TTFT timeout tiers resolved by ResolveTTFTTimeout.
const (
	// TTFTStandardTimeout is the fail-fast bound for standard/fast models.
	TTFTStandardTimeout = 15 * time.Second
	// TTFTLocalTimeout is the bound for local models (ollama): cold model
	// loads can legitimately exceed the cloud header bound.
	TTFTLocalTimeout = 20 * time.Second
	// TTFTReasoningLowTimeout is the bound for reasoning models on a light
	// effort tier (low/minimal): thinking starts fast.
	TTFTReasoningLowTimeout = 45 * time.Second
	// TTFTReasoningTimeout is the bound for reasoning models on the
	// default/medium effort tier.
	TTFTReasoningTimeout = 60 * time.Second
	// TTFTHeavyTimeout is the bound for heavy first-token waits: high-tier
	// free routing or high/xhigh/max reasoning effort.
	TTFTHeavyTimeout = 90 * time.Second
)

// ModelSpec describes the model/provider profile a TTFT deadline is
// resolved for.
type ModelSpec struct {
	// Provider is the active provider id (e.g. "openrouter", "groq",
	// "ollama"). Empty falls back to the standard tier.
	Provider string
	// ModelID is the active model id, including any OpenRouter-style
	// vendor prefix or ":free" suffix (e.g. "nex-agi/nex-n2.5-mini:free").
	ModelID string
	// Variant is the active reasoning effort / variant label (e.g.
	// "low", "medium", "high", "xhigh", "max"). Empty means provider
	// default.
	Variant string
	// IsReasoning forces the reasoning tier when the caller already knows
	// the model thinks (e.g. from the registry-backed capability
	// resolver). When false the offline name heuristic decides.
	IsReasoning bool
	// IsFreeTier forces the heavy free-tier bound. When false the ":free"
	// model suffix is auto-detected.
	IsFreeTier bool
	// TimeoutOverride is the user configuration fallback
	// (config.Timeout.TTFT). Positive values win over every tier.
	TimeoutOverride time.Duration
}

// ResolveTTFTTimeout returns the Time-To-First-Token deadline for spec:
//
// reasoning / heavy / free-tier models → 45s–90s
// standard / fast models → 15s–20s
// user override (TimeoutOverride > 0) → returned verbatim
//
// It never returns a non-positive duration.
func ResolveTTFTTimeout(spec ModelSpec) time.Duration {
	if spec.TimeoutOverride > 0 {
		return spec.TimeoutOverride
	}
	modelID := strings.TrimSpace(spec.ModelID)
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	variant := strings.ToLower(strings.TrimSpace(spec.Variant))

	free := spec.IsFreeTier || isFreeTierModel(modelID)
	reasoning := spec.IsReasoning || heuristicSupportsReasoning(spec.Provider, modelID)
	heavyVariant := variant == "high" || variant == "xhigh" || variant == "max"

	// Free-tier routing queues behind shared capacity: always heavy.
	if free {
		return TTFTHeavyTimeout
	}
	if reasoning {
		if heavyVariant {
			return TTFTHeavyTimeout
		}
		if variant == "low" || variant == "minimal" {
			return TTFTReasoningLowTimeout
		}
		return TTFTReasoningTimeout
	}
	// A heavy effort variant on a non-reasoning model still implies a
	// long first-token wait (large max-token / extended budget).
	if heavyVariant {
		return TTFTReasoningTimeout
	}
	if provider == "ollama" || provider == "local" {
		return TTFTLocalTimeout
	}
	if modelID == "" {
		return TTFTStandardTimeout
	}
	return TTFTStandardTimeout
}

// isFreeTierModel reports whether the model id routes via a high-latency
// free tier (OpenRouter ":free" suffix, e.g.
// "nex-agi/nex-n2.5-mini:free").
func isFreeTierModel(modelID string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(modelID)), ":free")
}
