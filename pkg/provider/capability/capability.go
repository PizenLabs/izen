// Package capability implements dynamic model capability discovery for the
// Izen runtime. A ModelCapabilities describes what one model can do — whether
// it supports reasoning effort levels (and exactly which ones), its context
// window, and its maximum output token budget.
//
// The UI must never hardcode effort choices: the supported effort list is
// derived per model from provider-advertised capabilities (OpenRouter
// supported_parameters) and model-family heuristics, so the same component
// renders zero effort options for a plain chat model and the full
// auto/low/medium/high/xhigh set for an OpenAI o3 or DeepSeek-R1 reasoning
// model.
package capability

import "strings"

// EffortLevel is a supported reasoning-effort level. The vocabulary is
// provider-conformant: the base tiers ("auto", "low", "medium", "high") map
// onto the OpenAI reasoning_effort values, and the extended "xhigh" tier is
// supported by high-capacity reasoning families such as OpenAI o1/o3 and
// DeepSeek-R1.
type EffortLevel string

// Supported effort levels. The zero value (empty string) is treated as
// EffortAuto by the token math so a nil effort never yields a NaN budget.
const (
	EffortAuto   EffortLevel = "auto"
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
)

// String returns the machine-readable effort label.
func (e EffortLevel) String() string { return string(e) }

// Valid reports whether e is a member of the closed effort vocabulary.
func (e EffortLevel) Valid() bool {
	switch e {
	case EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh:
		return true
	default:
		return false
	}
}

// Token budget ceilings applied by ThinkingBudget. They are derived from the
// legacy reasoning budget contract (low=4k, medium=16k, high=32k) and extended
// for the xhigh tier.
const (
	// thinkingBudgetLowCap caps the low-effort thinking budget at 4,000.
	thinkingBudgetLowCap = 4000
	// thinkingBudgetMediumCap caps the medium-effort thinking budget at 16,000.
	thinkingBudgetMediumCap = 16000
	// thinkingBudgetHighCap caps the high-effort thinking budget at 32,000.
	thinkingBudgetHighCap = 32000
	// thinkingOverheadTokens is the fixed completion headroom added on top of
	// the thinking budget for providers that bill thinking + completion
	// together (Anthropic max_tokens = budget + overhead).
	thinkingOverheadTokens = 4096
	// defaultMaxOutputTokens is the heuristic output budget used when a model
	// advertises no maximum.
	defaultMaxOutputTokens = 8192
)

// ModelCapabilities is the dynamic capability record for one model. Every
// field is populated from provider inspection (OpenRouter/Ollama) and enriched
// with family heuristics when the provider does not advertise a value.
type ModelCapabilities struct {
	// Provider is the provider namespace the model belongs to
	// ("openrouter", "ollama", "openai", "anthropic", "deepseek", ...).
	Provider string
	// ModelID is the provider-resolved model identifier, e.g. "deepseek-r1"
	// or "openai/o3-mini".
	ModelID string
	// Name is the human-readable display name (falls back to ModelID).
	Name string
	// SupportsReasoning reports whether the model exposes a qualitative
	// reasoning control. When false the model is rendered with zero effort
	// options.
	SupportsReasoning bool
	// SupportedEfforts is the dynamically derived effort option list for this
	// model. It is nil/empty for non-reasoning models and populated from
	// provider parameters plus model-family mapping for reasoning models.
	SupportedEfforts []EffortLevel
	// ContextWindow is the maximum context window in tokens.
	ContextWindow int
	// MaxOutputTokens is the maximum number of completion/output tokens the
	// model accepts (OpenRouter top_provider.max_completion_tokens).
	MaxOutputTokens int
}

// EffortOptions returns a defensive copy of the effort levels the model
// supports. It is the single source the UI binds its effort selector to — the
// selector renders exactly this list and nothing more. An empty result means
// the model exposes no reasoning effort control.
func (c ModelCapabilities) EffortOptions() []EffortLevel {
	if len(c.SupportedEfforts) == 0 {
		return nil
	}
	return append([]EffortLevel(nil), c.SupportedEfforts...)
}

// SupportsEffort reports whether e is one of the model's supported effort
// levels.
func (c ModelCapabilities) SupportsEffort(e EffortLevel) bool {
	for _, s := range c.SupportedEfforts {
		if s == e {
			return true
		}
	}
	return false
}

// Normalize fills missing capability fields from family heuristics so a model
// record is always complete for UI rendering and budget math. It never
// downgrades an explicitly advertised value.
func (c ModelCapabilities) Normalize() ModelCapabilities {
	if c.Provider == "" {
		c.Provider = "unknown"
	}
	if c.Name == "" {
		c.Name = c.ModelID
	}
	if !c.SupportsReasoning {
		c.SupportedEfforts = nil
	} else if len(c.SupportedEfforts) == 0 {
		c.SupportedEfforts = effortLevelsForModel(c.Provider, c.ModelID, true)
	}
	if c.ContextWindow <= 0 {
		c.ContextWindow = ContextWindowFor(c.ModelID)
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = MaxOutputTokensFor(c.Provider, c.ModelID)
	}
	return c
}

// ThinkingBudget computes the reasoning/thinking token budget for a given
// effort level as a fraction of the model's maximum output budget, capped by
// the per-tier ceiling:
//
//	auto: 0 (provider default behaviour)
//	low: min(4k, 25% × MaxOutputTokens)
//	medium: min(16k, 50% × MaxOutputTokens)
//	high: min(32k, 80% × MaxOutputTokens)
//	xhigh: MaxOutputTokens (the full output budget is available to reasoning)
func (c ModelCapabilities) ThinkingBudget(effort EffortLevel) int {
	maxOut := c.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = defaultMaxOutputTokens
	}
	switch effort {
	case EffortLow:
		v := int(float64(maxOut) * 0.25)
		if v > thinkingBudgetLowCap {
			v = thinkingBudgetLowCap
		}
		if v < 1 {
			v = 1
		}
		return v
	case EffortMedium:
		v := int(float64(maxOut) * 0.5)
		if v > thinkingBudgetMediumCap {
			v = thinkingBudgetMediumCap
		}
		return v
	case EffortHigh:
		v := int(float64(maxOut) * 0.80)
		if v > thinkingBudgetHighCap {
			v = thinkingBudgetHighCap
		}
		return v
	case EffortXHigh:
		return maxOut
	default: // EffortAuto, "", unknown
		return 0
	}
}

// TotalMaxTokens computes the total output budget the model accepts at the
// given effort level: the thinking budget plus the fixed completion headroom,
// capped at the model's advertised maximum output.
func (c ModelCapabilities) TotalMaxTokens(effort EffortLevel) int {
	maxOut := c.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = defaultMaxOutputTokens
	}
	budget := c.ThinkingBudget(effort)
	if budget <= 0 {
		return maxOut
	}
	total := budget + thinkingOverheadTokens
	if total > maxOut {
		total = maxOut
	}
	return total
}

// splitVendor splits an OpenRouter-style "vendor/model" identifier into its
// vendor prefix and bare model name. A bare identifier yields vendor "" and
// the full string as the model.
func splitVendor(modelID string) (vendor, model string) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	idx := strings.Index(id, "/")
	if idx < 0 {
		return "", id
	}
	return strings.TrimSpace(id[:idx]), strings.TrimSpace(id[idx+1:])
}

// hasAnyPrefix reports whether s starts with any of the given prefixes.
func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
