package stepadmission

import (
	"github.com/PizenLabs/izen/internal/mutationstrategy"
)

// ModelCapabilityProfile is the minimal domain-neutral capability view that
// bounds one step. It describes observable operational limits, not
// intelligence tiers (no LOW/MEDIUM/HIGH/REASONING/CODING labels).
//
// Only fields actually required by Phase 5 are included: the output ceiling
// is the primary constraint that drives admission; the remaining fields
// are the observable envelope dimensions listed in the spec and remain
// optional (zero/false means unknown/unsupported).
type ModelCapabilityProfile struct {
	// MaxOutputTokens is the provider-advertised per-step output ceiling
	// (e.g. max_output_tokens or known free-tier hard ceiling). Zero means
	// unknown/unbounded for admission (the scheduler still gates at execution).
	MaxOutputTokens int `json:"max_output_tokens"`
	// ContextLimit is the maximum context window in tokens. Zero means unknown.
	ContextLimit int `json:"context_limit,omitempty"`
	// StructuredOutputSupport reports whether the model supports structured
	// output (e.g. JSON mode). False means unknown/unsupported.
	StructuredOutputSupport bool `json:"structured_output_support,omitempty"`
	// ToolSupport reports whether the model supports tool/function calling.
	ToolSupport bool `json:"tool_support,omitempty"`
	// StreamingSupport reports whether the model supports streaming.
	StreamingSupport bool `json:"streaming_support,omitempty"`
}

// Valid reports whether the profile is well-formed (non-negative ceilings).
func (m ModelCapabilityProfile) Valid() bool {
	return m.MaxOutputTokens >= 0 && m.ContextLimit >= 0
}

// IsConstrained reports whether the profile represents a constrained
// ceiling at or below the free-tier style threshold (<= 1024). Zero is not
// constrained. This mirrors the provider-constrained detection without
// branching on provider/model name outside the resolution boundary.
func (m ModelCapabilityProfile) IsConstrained() bool {
	return m.MaxOutputTokens > 0 && m.MaxOutputTokens <= 1024
}

// CapabilityFromLimits is the provider-boundary resolver that converts raw
// endpoint metadata into the runtime ModelCapabilityProfile. Provider-specific
// knowledge belongs here; the rest of Izen consumes ModelCapabilityProfile
// rather than branching on provider or model strings.
//
// Negative inputs normalize to zero (unknown).
func CapabilityFromLimits(maxOutputTokens, contextLimit int, structured, tool, streaming bool) ModelCapabilityProfile {
	if maxOutputTokens < 0 {
		maxOutputTokens = 0
	}
	if contextLimit < 0 {
		contextLimit = 0
	}
	return ModelCapabilityProfile{
		MaxOutputTokens:         maxOutputTokens,
		ContextLimit:            contextLimit,
		StructuredOutputSupport: structured,
		ToolSupport:             tool,
		StreamingSupport:        streaming,
	}
}

// CapabilityFromOutputCeiling is the minimal resolver for the spec's
// examples:
//
//	model max_output_tokens = 1024 → MaxOutputTokens = 1024
//	known free-tier hard ceiling → MaxOutputTokens = 1024
func CapabilityFromOutputCeiling(ceiling int) ModelCapabilityProfile {
	return CapabilityFromLimits(ceiling, 0, false, false, false)
}

// EffectiveBudget derives the available step budget from the capability
// profile and the fallback planning budget. It reuses the existing
// StepBudget model (Phase 3) and the same fixed planning margin as
// mutationstrategy.StepBudgetFor so there is exactly one budget abstraction.
//
// Relationship (spec §7):
//
//	ModelCapabilityProfile → Available Step Budget → Candidate Step Estimate → Admission
func EffectiveBudget(cap ModelCapabilityProfile, fallback mutationstrategy.StepBudget) mutationstrategy.StepBudget {
	if cap.MaxOutputTokens <= 0 {
		return fallback
	}
	mc := mutationstrategy.ModelConstraints{OutputCeiling: cap.MaxOutputTokens}
	return mutationstrategy.StepBudgetFor(mc, fallback)
}

// DefaultFallbackBudget is the fallback when no provider ceiling is known.
// It mirrors mutationstrategy's default envelope (4096) and keeps Phase 5
// consistent with Phase 3 without duplicating the constant in prose.
var DefaultFallbackBudget = mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
