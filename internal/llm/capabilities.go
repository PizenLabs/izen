package llm

// Capabilities is the provider-native capability view attached to the
// active Model context. It is derived dynamically from provider endpoint
// metadata (OpenRouter /models, Ollama /api/show) with heuristic fallback.
type Capabilities struct {
	Variants            []string
	MaxContextTokens    int
	MaxCompletionTokens int
	SupportsReasoning   bool
	RawExtraFields      map[string]any
}

// Capabilities returns the provider-native capability view for this model.
// Variants is nil when the model exposes no variant control.
func (m ModelInfo) Capabilities() Capabilities {
	supports := false
	if m.SupportsReasoning != nil {
		supports = *m.SupportsReasoning
	} else {
		supports = ModelSupportsEffortWithProvider(m.Provider, m.ID)
	}
	variants := m.Variants
	if len(variants) == 0 && supports {
		variants = VariantsFor(m.Provider, m.ID)
	}
	if len(variants) == 0 {
		variants = nil
	}
	ctx := m.MaxContextTokens
	if ctx == 0 {
		ctx = m.ContextWindow
	}
	comp := m.MaxCompletionTokens
	if comp == 0 {
		comp = m.MaxOutputTokens
	}
	return Capabilities{
		Variants:            variants,
		MaxContextTokens:    ctx,
		MaxCompletionTokens: comp,
		SupportsReasoning:   supports,
		RawExtraFields:      m.RawExtraFields,
	}
}

// SyncNativeBounds keeps the native MaxContextTokens/MaxCompletionTokens
// bounds in sync with the legacy ContextWindow/MaxOutputTokens aliases.
func (m *ModelInfo) SyncNativeBounds() {
	if m.MaxContextTokens == 0 {
		m.MaxContextTokens = m.ContextWindow
	}
	if m.ContextWindow == 0 {
		m.ContextWindow = m.MaxContextTokens
	}
	if m.MaxCompletionTokens == 0 {
		m.MaxCompletionTokens = m.MaxOutputTokens
	}
	if m.MaxOutputTokens == 0 {
		m.MaxOutputTokens = m.MaxCompletionTokens
	}
	if len(m.Variants) == 0 {
		if v := VariantsFor(m.Provider, m.ID); len(v) > 0 {
			m.Variants = v
		}
	}
}

// VariantBudgetRatio returns the fraction of MaxCompletionTokens consumed
// by the hidden reasoning channel for a given variant. Ratios are dynamic:
// they scale with the model's native completion bound instead of hardcoded
// token strings.
func VariantBudgetRatio(variant string) float64 {
	switch variant {
	case "minimal":
		return 0.10
	case "low":
		return 0.25
	case "medium":
		return 0.50
	case "high":
		return 0.80
	case "xhigh":
		return 0.95
	case "default", "auto", "":
		return 0
	default:
		return 0.50
	}
}

// VariantBudget computes the thinking budget for a variant against the
// model's native MaxCompletionTokens bound.
func VariantBudget(variant string, maxCompletionTokens int) int {
	if maxCompletionTokens <= 0 {
		maxCompletionTokens = 8192
	}
	r := VariantBudgetRatio(variant)
	if r <= 0 {
		return 0
	}
	return int(float64(maxCompletionTokens) * r)
}
