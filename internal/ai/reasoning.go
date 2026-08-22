package ai

// ReasoningConfig is the provider-agnostic reasoning control payload. The
// decision engine resolves it (see pkg/engine/decision.EffortConfig); provider
// adapters translate it into their native API fields:
//
//   - Level        → OpenAI reasoning_effort / OpenRouter reasoning.effort
//   - BudgetTokens → Anthropic thinking.budget_tokens
//   - CoTLimit     → OpenRouter reasoning.max_tokens (SLM reasoning cap)
//
// Only the field matching the target model's native reasoning mechanism is
// populated; the others stay zero.
type ReasoningConfig struct {
	// Level is the qualitative reasoning effort (low/medium/high/xhigh).
	Level string `json:"level"`
	// BudgetTokens is the thinking token budget.
	BudgetTokens int `json:"budget_tokens"`
	// CoTLimit is the chain-of-thought token cap.
	CoTLimit int `json:"cot_limit"`
	// Disabled requests that the model perform NO hidden reasoning pass
	// (OpenRouter reasoning.enabled=false). This is the only reliable control
	// for models whose gateway ignores reasoning.max_tokens: without it such
	// models spend the ENTIRE shared output budget inside the hidden CoT
	// channel and emit zero visible artifact text. When true it supersedes
	// Level/BudgetTokens/CoTLimit.
	Disabled bool `json:"disabled,omitempty"`
}

// IsZero reports whether no reasoning control is configured.
func (c *ReasoningConfig) IsZero() bool {
	return c == nil || (!c.Disabled && c.Level == "" && c.BudgetTokens == 0 && c.CoTLimit == 0)
}

// LevelOrDefault returns the configured effort level, or "".
func (c *ReasoningConfig) LevelOrDefault() string {
	if c == nil {
		return ""
	}
	return c.Level
}

// BudgetOrDefault returns the configured thinking budget, or 0.
func (c *ReasoningConfig) BudgetOrDefault() int {
	if c == nil {
		return 0
	}
	return c.BudgetTokens
}

// CoTLimitOrDefault returns the configured reasoning cap, or 0.
func (c *ReasoningConfig) CoTLimitOrDefault() int {
	if c == nil {
		return 0
	}
	return c.CoTLimit
}
