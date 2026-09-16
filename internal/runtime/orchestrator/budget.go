package orchestrator

import (
	"github.com/PizenLabs/izen/internal/provider"
)

// ModelClass classifies the provider/model tier for dynamic ASK budgeting.
// It is descriptive only and MUST NEVER influence Control Plane authorization.
type ModelClass string

const (
	ModelClassStandard    ModelClass = "standard"
	ModelClassHighOutput  ModelClass = "high_output"
	ModelClassConstrained ModelClass = "constrained"
)

// TaskComplexity classifies the ASK task depth for budget scaling.
type TaskComplexity string

const (
	TaskComplexityLow    TaskComplexity = "low"
	TaskComplexityMedium TaskComplexity = "medium"
	TaskComplexityHigh   TaskComplexity = "high"
)

// ASKBudgetPolicy is the resolved dynamic generation budget for one ASK turn.
// No field is a global hardcoded semantic invariant: every bound is derived
// from the provider's advertised OutputTokenCap.
type ASKBudgetPolicy struct {
	// MaxTokens is the effective max_tokens to send to the provider.
	MaxTokens int
	// VisibleBudget is the visible output boundary for UI streaming.
	VisibleBudget int
	// ReasoningBudget is the hidden reasoning allocation (0 when unsupported).
	ReasoningBudget int
	// DetailLevel is "concise" for constrained providers, "expanded" for
	// high-output providers, "standard" otherwise.
	DetailLevel string
	// Clamped reports whether the provider cap forced a clamp.
	Clamped bool
}

// ASKBudgetResolver scales visible output boundaries according to
// provider/model capabilities. The generation budget is NEVER hardcoded
// (e.g. universal <300 tokens): it is dynamically derived from
// ProviderCapabilities (OutputTokenCap, SupportsReasoningBudget,
// SupportsReasoningEffort), ModelClass, and TaskComplexity.
//
// Dynamic Budgeting Invariant: output budgets MUST be dynamically derived via
// this resolver. Provider capabilities MUST NOT influence authorization state.
type ASKBudgetResolver struct{}

// NewASKBudgetResolver returns a ready-to-use resolver (stateless).
func NewASKBudgetResolver() *ASKBudgetResolver { return &ASKBudgetResolver{} }

// Resolve computes the effective ASK budget for caps/class/complexity.
//   - Unknown cap (<=0) defaults to 8192 (heuristic output budget).
//   - Constrained (cap <= 1024): clamps to min(requested, 980), concise
//     policy, reasoning budget forced to 0 to prevent exhaustion.
//   - High-output (cap >= 16384 or ModelClassHighOutput): expanded detail
//     policy, full cap available, reasoning scaled when supported.
//   - Standard: cap-bounded budget scaled by task complexity.
func (r *ASKBudgetResolver) Resolve(caps provider.ProviderCapabilities, class ModelClass, complexity TaskComplexity) ASKBudgetPolicy {
	cap := caps.OutputTokenCap
	if cap <= 0 {
		cap = 8192
	}
	// Constrained path: OutputTokenCap <= 1024 dynamically clamps
	// reasoning/visible allocations.
	if caps.IsConstrained() || class == ModelClassConstrained {
		effective := cap
		if effective > provider.ConstrainedMaxTokens {
			effective = provider.ConstrainedMaxTokens
		}
		return ASKBudgetPolicy{
			MaxTokens:       effective,
			VisibleBudget:   effective,
			ReasoningBudget: 0,
			DetailLevel:     "concise",
			Clamped:         true,
		}
	}
	// Reasoning allocation: only when the provider advertises support.
	reasoning := 0
	if caps.SupportsReasoningBudget || caps.SupportsReasoningEffort {
		switch complexity {
		case TaskComplexityHigh:
			reasoning = cap / 4
		case TaskComplexityMedium:
			reasoning = cap / 8
		default:
			reasoning = 0
		}
		// Never consume the entire visible budget with reasoning.
		if reasoning >= cap {
			reasoning = cap / 4
		}
	}
	visible := cap - reasoning
	if visible <= 0 {
		visible = cap
		reasoning = 0
	}
	detail := "standard"
	clamped := false
	if cap >= 16384 || class == ModelClassHighOutput {
		detail = "expanded"
	} else if complexity == TaskComplexityLow {
		detail = "concise"
		// Low complexity on a standard model still uses the dynamic cap;
		// concise here describes verbosity guidance, not an artificial
		// truncation directive.
	}
	return ASKBudgetPolicy{
		MaxTokens:       visible,
		VisibleBudget:   visible,
		ReasoningBudget: reasoning,
		DetailLevel:     detail,
		Clamped:         clamped,
	}
}

// ResolveMaxTokens is the convenience entry point for request assembly:
// it returns only the effective max_tokens for the given caps/class.
func (r *ASKBudgetResolver) ResolveMaxTokens(caps provider.ProviderCapabilities, class ModelClass, complexity TaskComplexity) int {
	return r.Resolve(caps, class, complexity).MaxTokens
}
