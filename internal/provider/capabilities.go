package provider

import "strings"

// ConstrainedOutputThreshold is the max_output ceiling at or below which a
// model is treated as constrained (free-tier style). Mirrors
// providers/capability.ConstrainedOutputThreshold without importing it.
const ConstrainedOutputThreshold = 1024

// ConstrainedMaxTokens is the enforced output budget for constrained models.
const ConstrainedMaxTokens = 980

// ProviderCapabilities is the provider/model capability view that drives
// dynamic ASK generation budgeting. It is populated from provider endpoint
// metadata (OpenRouter /models, Ollama /api/show) with heuristic fallback.
//
// Authority Static Invariant: this struct MUST NEVER influence Control Plane
// authorization state (CapMutate eligibility). Authorization is governed
// exclusively by Control Plane input parsing ($prompt/$hot, /build).
// Provider model capabilities, context length, or token pricing MUST NOT
// alter capability evaluation.
type ProviderCapabilities struct {
	// OutputTokenCap is the maximum completion/output tokens the model
	// accepts (OpenRouter top_provider.max_completion_tokens). Zero means
	// unknown ceiling.
	OutputTokenCap int
	// SupportsReasoningBudget reports whether the model exposes a numeric
	// thinking-budget control (e.g. Anthropic thinking.budget_tokens).
	SupportsReasoningBudget bool
	// SupportsReasoningEffort reports whether the model exposes a qualitative
	// effort control (e.g. OpenAI reasoning_effort).
	SupportsReasoningEffort bool
	// ContextWindow is the maximum context window in tokens. Zero means unknown.
	ContextWindow int
	// Provider is the provider namespace ("openrouter", "ollama", ...).
	Provider string
	// ModelID is the provider-resolved model identifier.
	ModelID string
}

// IsConstrained reports whether the advertised output cap marks the model as
// constrained (OutputTokenCap <= 1024). Zero/negative means unknown ceiling
// and is NOT constrained.
func (c ProviderCapabilities) IsConstrained() bool {
	return c.OutputTokenCap > 0 && c.OutputTokenCap <= ConstrainedOutputThreshold
}

// StreamOutcome is the universal stream outcome vocabulary. It maps directly
// across ALL providers (free or paid):
//
//	stop   -> COMPLETE
//	length -> PARTIAL (EvidenceState.PARTIAL)
//	error  -> FAILED
//	cancel -> CANCELLED
//
// Truncated streams MUST preserve canonical token buffers without synthetic
// content mutation.
type StreamOutcome string

const (
	StreamComplete  StreamOutcome = "COMPLETE"
	StreamPartial   StreamOutcome = "PARTIAL"
	StreamFailed    StreamOutcome = "FAILED"
	StreamCancelled StreamOutcome = "CANCELLED"
	StreamUnknown   StreamOutcome = "UNKNOWN"
)

// MapFinishReason maps a provider-native finish_reason onto the universal
// StreamOutcome. Matching is case-insensitive and whitespace-tolerant.
// Unknown/empty reasons map to UNKNOWN (callers decide fail-closed).
func MapFinishReason(finishReason string) StreamOutcome {
	switch strings.ToLower(strings.TrimSpace(finishReason)) {
	case "stop", "end_turn", "complete", "completed", "tool_calls", "function_call":
		return StreamComplete
	case "length", "max_tokens", "max-output-tokens", "output_truncated":
		return StreamPartial
	case "error", "failed", "failure":
		return StreamFailed
	case "cancel", "cancelled", "canceled", "abort", "aborted", "interrupt", "interrupted":
		return StreamCancelled
	default:
		return StreamUnknown
	}
}

// IsPartial reports whether the outcome is a truncation that must surface as
// EvidenceState.PARTIAL with a UI boundary badge.
func (o StreamOutcome) IsPartial() bool { return o == StreamPartial }

// String returns the raw outcome label.
func (o StreamOutcome) String() string { return string(o) }
