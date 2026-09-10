package registry

import (
	"strings"

	coredomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/provider/adapter"
)

// CapabilityResolver resolves the truthful reasoning capability for a model
// based on Model ID / Provider matching (never inferring unknown grades).
//
// Resolution order (first match wins):
//  1. DeepSeek R1 / Reasoner family -> Supported but provider-managed (Fixed).
//  2. Claude reasoning family -> configurable effort ladder with human labels.
//  3. Gemini 2.5 family -> configurable concrete token-budget options.
//  4. Explicit non-reasoning denylist (gpt-4o, dots-3-note, ...) -> unsupported.
//  5. Provider fallback via adapter.CapabilityForProvider (preserves generic
//     EnumStandard / EnumExtended / ToggleAuto / Fixed / None behavior).
type CapabilityResolver struct{}

// Resolve returns the reasoning capability for provider/modelID.
func (CapabilityResolver) Resolve(provider, modelID string) coredomain.ReasoningCapability {
	return ResolveReasoningCapability(provider, modelID)
}

// ResolveReasoningCapability is the functional entry point for capability
// lookup. See CapabilityResolver for the resolution order.
func ResolveReasoningCapability(provider, modelID string) coredomain.ReasoningCapability {
	lowerID := strings.ToLower(strings.TrimSpace(modelID))

	// 1. DeepSeek R1 / Reasoner: always on, provider managed (PATH B).
	if strings.Contains(lowerID, "deepseek-r1") || strings.Contains(lowerID, "deepseek-reasoner") {
		return coredomain.ReasoningCapability{Supported: true, Configurable: false}
	}

	// 2. Claude reasoning family: Low -> Medium -> High -> Extra High -> Max.
	if strings.Contains(lowerID, "claude-3-7-sonnet") ||
		strings.Contains(lowerID, "claude-sonnet-4") ||
		strings.Contains(lowerID, "claude-opus-4") ||
		strings.Contains(lowerID, "claude-3-7") {
		return coredomain.ReasoningCapability{
			Supported:    true,
			Configurable: true,
			Options: []coredomain.ReasoningOption{
				{ID: "low", Label: "Low Effort"},
				{ID: "medium", Label: "Medium Effort"},
				{ID: "high", Label: "High Effort"},
				{ID: "xhigh", Label: "Extra High Effort"},
				{ID: "max", Label: "Max Effort"},
			},
		}
	}

	// 3. Gemini 2.5 family: concrete token-budget options.
	if strings.Contains(lowerID, "gemini") && strings.Contains(lowerID, "2.5") {
		return coredomain.ReasoningCapability{
			Supported:    true,
			Configurable: true,
			Options: []coredomain.ReasoningOption{
				{ID: "off", Label: "Off (0 tokens)"},
				{ID: "budget_2k", Label: "2,048 tokens"},
				{ID: "budget_8k", Label: "8,192 tokens"},
				{ID: "budget_16k", Label: "16,384 tokens"},
				{ID: "budget_24k", Label: "24,576 tokens"},
			},
		}
	}

	// 4. Explicit non-reasoning denylist: never infer grades.
	// The generic provider fallback below (step 5) must not fabricate
	// reasoning options for known non-reasoning families served through
	// reasoning-capable providers (e.g. OpenRouter serves the fast
	// InclusionAI Ling flash chat/finance-tuned models alongside real
	// reasoning models; per-model truth wins over provider default).
	if strings.Contains(lowerID, "gpt-4o-mini") ||
		strings.Contains(lowerID, "gpt-4o") ||
		strings.Contains(lowerID, "dots-3-note") ||
		strings.Contains(lowerID, "dots-studio") ||
		strings.Contains(lowerID, "inclusionai") ||
		strings.Contains(lowerID, "ling-3") {
		return coredomain.ReasoningCapability{Supported: false, Configurable: false}
	}

	// 5. Provider fallback (generic modes keep default-first behavior).
	mode := adapter.CapabilityForProvider(provider).ReasoningMode
	switch mode {
	case adapter.ReasoningModeEnumStandard, adapter.ReasoningModeEnumExtended, adapter.ReasoningModeToggleAuto:
		base := adapter.OptionsForMode(mode)
		opts := make([]coredomain.ReasoningOption, 0, len(base)+1)
		opts = append(opts, coredomain.ReasoningOption{ID: "default", Label: "default"})
		for _, o := range base {
			opts = append(opts, coredomain.ReasoningOption{ID: o, Label: o})
		}
		return coredomain.ReasoningCapability{Supported: true, Configurable: true, Options: opts}
	case adapter.ReasoningModeFixed:
		return coredomain.ReasoningCapability{Supported: true, Configurable: false}
	default:
		return coredomain.ReasoningCapability{Supported: false, Configurable: false}
	}
}

// GetReasoningCapability retrieves the real ReasoningCapability for this
// model via CapabilityResolver based on Model ID / Provider matching.
func (d ModelDescriptor) GetReasoningCapability() coredomain.ReasoningCapability {
	return ResolveReasoningCapability(d.Provider, d.ID)
}
