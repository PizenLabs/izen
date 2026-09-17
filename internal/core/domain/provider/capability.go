// Package provider implements the Capability & Budget Engine for Bounded
// Execution (Phase 6.2).
//
// Invariant 1 (Per-Call Limit Insulation): per-call output token limits MUST
// NOT dictate task capability. Task scope ($hot declared scope, $prompt
// dynamic scope) is durable; the step budget is ephemeral.
//
// Invariant 3 (Dynamic Budget Margin): the effective step budget is derived
// dynamically as min(Requested, ProviderMaxOutput, TaskRemaining). Policy
// margins for opaque reasoning are caller-supplied parameters — they MUST NOT
// be hardcoded as architectural absolute limits.
package provider

// ProviderMetadata is the dynamic capability view of one model endpoint. It
// is populated from provider metadata (OpenRouter /models, Ollama /api/show,
// registry ModelDescriptor) with zero-means-unknown semantics.
type ProviderMetadata struct {
	// MaxOutputTokens is the provider-advertised completion ceiling. Zero
	// means unknown (no provider cap applied).
	MaxOutputTokens int
	// SupportsReasoningUsage reports whether the provider surfaces reasoning
	// token usage (e.g. OpenRouter reasoning_tokens details).
	SupportsReasoningUsage bool
	// SupportsReasoningBudget reports whether the provider exposes a numeric
	// thinking-budget control (e.g. Anthropic thinking.budget_tokens).
	SupportsReasoningBudget bool
	// ContextWindow is the maximum context window in tokens. Zero = unknown.
	ContextWindow int
	// Provider namespaces the endpoint ("openrouter", "ollama", ...).
	Provider string
	// ModelID is the provider-resolved model identifier.
	ModelID string
}

// IsConstrained reports whether the advertised output cap marks the model as
// constrained (<= 1024). Zero/negative means unknown and is NOT constrained.
func (m ProviderMetadata) IsConstrained() bool {
	return m.MaxOutputTokens > 0 && m.MaxOutputTokens <= ConstrainedOutputThreshold
}

// DetectCapability builds ProviderMetadata from raw endpoint values. It
// performs no I/O and applies no policy: it only records what the provider
// advertised. Negative inputs normalize to zero (unknown).
func DetectCapability(provider, modelID string, maxOutput, contextWindow int, reasoningUsage, reasoningBudget bool) ProviderMetadata {
	if maxOutput < 0 {
		maxOutput = 0
	}
	if contextWindow < 0 {
		contextWindow = 0
	}
	return ProviderMetadata{
		MaxOutputTokens:         maxOutput,
		SupportsReasoningUsage:  reasoningUsage,
		SupportsReasoningBudget: reasoningBudget,
		ContextWindow:           contextWindow,
		Provider:                provider,
		ModelID:                 modelID,
	}
}

// EffectiveStepBudget computes the ephemeral per-step output budget:
//
//	EffectiveStepBudget = min(Requested, ProviderMaxOutput, TaskRemaining)
//
// minus the caller-supplied reasoningMargin (opaque reasoning reserve).
// The margin is a POLICY parameter owned by the caller — this engine never
// hardcodes it (Invariant 3). Non-positive inputs mean "unbounded" for that
// operand and are skipped. The result is at least MinStepBudget when any
// positive bound exists, and Requested (or fallback) when no bound exists.
//
// Invariant 1: the returned value bounds ONE step only. It never truncates,
// narrows, or re-scopes the durable task ($prompt / $hot boundaries).
func EffectiveStepBudget(requested, providerMaxOutput, taskRemaining, reasoningMargin int) int {
	return effectiveStepBudget(requested, providerMaxOutput, taskRemaining, reasoningMargin)
}

func effectiveStepBudget(requested, providerMaxOutput, taskRemaining, reasoningMargin int) int {
	best := 0
	consider := func(v int) {
		if v <= 0 {
			return
		}
		if best == 0 || v < best {
			best = v
		}
	}
	consider(requested)
	consider(providerMaxOutput)
	consider(taskRemaining)
	if best == 0 {
		if requested > 0 {
			return requested
		}
		return DefaultRequestedStepBudget
	}
	if reasoningMargin > 0 {
		best -= reasoningMargin
	}
	if best < MinStepBudget {
		best = MinStepBudget
	}
	return best
}

// StepBudgetForTask is the scheduler-facing helper: it derives the ephemeral
// step budget from task-remaining tokens, provider metadata, requested step
// complexity, and a caller-owned reasoning margin.
func StepBudgetForTask(requestedComplexity, taskRemaining int, meta ProviderMetadata, reasoningMargin int) int {
	return EffectiveStepBudget(requestedComplexity, meta.MaxOutputTokens, taskRemaining, reasoningMargin)
}
