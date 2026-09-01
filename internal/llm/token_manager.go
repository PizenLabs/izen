package llm

import (
	"strings"
	"time"
)

// TokenManager calculates effort-to-token allocations and enforces provider
// payload contracts per spec 2C.
type TokenManager struct{}

// NewTokenManager returns a stateless manager (no config needed).
func NewTokenManager() *TokenManager { return &TokenManager{} }

// ThinkingBudget computes the thinking budget per effort level per spec:
//
//	auto: 0 (default provider behavior)
//	low: min(4000, 25% * MaxOutputTokens)
//	medium: min(16000, 50% * MaxOutputTokens)
//	high: min(32000, 80% * MaxOutputTokens)
func (tm *TokenManager) ThinkingBudget(effort string, maxOutputTokens int) int {
	if maxOutputTokens <= 0 {
		maxOutputTokens = 8192 // heuristic fallback
	}
	switch strings.ToLower(effort) {
	case "low":
		v := int(float64(maxOutputTokens) * 0.25)
		if v > 4000 {
			v = 4000
		}
		if v < 1 {
			v = 1
		}
		return v
	case "medium":
		v := int(float64(maxOutputTokens) * 0.5)
		if v > 16000 {
			v = 16000
		}
		return v
	case "high":
		v := int(float64(maxOutputTokens) * 0.80)
		if v > 32000 {
			v = 32000
		}
		return v
	case "auto", "":
		return 0
	default:
		return 0
	}
}

// MaxOutputForModel returns the model's max output tokens, consulting
// dynamic discovery (ModelInfo) or heuristic fallback. If m is nil, uses
// heuristic based on provider/modelID.
func (tm *TokenManager) MaxOutputForModel(m *ModelInfo, provider, modelID string) int {
	if m != nil && m.MaxOutputTokens > 0 {
		return m.MaxOutputTokens
	}
	// Try cached dynamic lookup
	if cached, ok := lookupCachedMaxOutput(provider, modelID); ok {
		return cached
	}
	// Heuristic fallback
	return heuristicMaxOutputTokens(provider, modelID)
}

func lookupCachedMaxOutput(provider, modelID string) (int, bool) {
	cached, ok := loadCachedIfFresh(24 * time.Hour)
	if !ok {
		return 0, false
	}
	for _, m := range cached {
		if strings.EqualFold(m.Provider, provider) && strings.EqualFold(m.ID, modelID) && m.MaxOutputTokens > 0 {
			return m.MaxOutputTokens, true
		}
		if m.Provider == "openrouter" && strings.EqualFold(m.ID, provider+"/"+modelID) && m.MaxOutputTokens > 0 {
			return m.MaxOutputTokens, true
		}
		if strings.EqualFold(m.ID, modelID) && m.MaxOutputTokens > 0 {
			return m.MaxOutputTokens, true
		}
	}
	return 0, false
}

// TokenInfo holds display values for UI header.
type TokenInfo struct {
	ThinkingBudget int
	TotalMax       int
}

// InfoFor returns TokenInfo for a given model/provider/effort.
// TotalMax is provider-specific: Anthropic => ThinkingBudget+4096, OpenAI => MaxOutputTokens (max_completion_tokens)
func (tm *TokenManager) InfoFor(provider, modelID, effort string) TokenInfo {
	maxOut := heuristicMaxOutputTokens(provider, modelID)
	// Try dynamic cache for maxOut
	if m, ok := findCachedModel(provider, modelID); ok && m.MaxOutputTokens > 0 {
		maxOut = m.MaxOutputTokens
	}
	budget := tm.ThinkingBudget(effort, maxOut)
	total := maxOut
	provLower := strings.ToLower(provider)
	// For openrouter, infer vendor
	if provLower == "openrouter" {
		lowerID := strings.ToLower(modelID)
		if idx := strings.Index(lowerID, "/"); idx >= 0 {
			provLower = lowerID[:idx]
		}
	}
	switch provLower {
	case "anthropic":
		if budget > 0 {
			total = budget + 4096
			// Ensure total does not exceed maxOut if maxOut is larger? Spec says max_tokens = ThinkingBudget+4096
			// Keep as computed.
		} else {
			total = maxOut
		}
	case "openai":
		// OpenAI uses max_completion_tokens = maxOut (total). Thinking budget is internal but not added.
		total = maxOut
	default:
		// For deepseek etc., total is maxOut
		if budget > 0 {
			total = budget + 4096
			if total > maxOut && maxOut > 0 {
				// Keep total at least maxOut
				if total < maxOut {
					total = maxOut
				}
			}
		}
	}
	return TokenInfo{ThinkingBudget: budget, TotalMax: total}
}

func findCachedModel(provider, modelID string) (*ModelInfo, bool) {
	cached, ok := loadCachedIfFresh(24 * time.Hour)
	if !ok {
		// Try load without TTL check (allow stale)
		cached, ok = loadCachedIfFresh(100 * 365 * 24 * time.Hour)
		if !ok {
			return nil, false
		}
	}
	for _, m := range cached {
		if strings.EqualFold(m.Provider, provider) && strings.EqualFold(m.ID, modelID) {
			return &m, true
		}
		if m.Provider == "openrouter" && strings.EqualFold(m.ID, provider+"/"+modelID) {
			return &m, true
		}
		if strings.EqualFold(m.ID, modelID) {
			return &m, true
		}
	}
	return nil, false
}

// ProviderPayload holds the enforced payload fields for dispatch.
type ProviderPayload struct {
	ReasoningEffort     string
	ThinkingBudget      int
	MaxTokens           int // for Anthropic max_tokens
	MaxCompletionTokens int // for OpenAI
}

// BuildPayload enforces provider contract: OpenAI => reasoning_effort + max_completion_tokens,
// Anthropic => max_tokens = thinkingBudget+4096.
func (tm *TokenManager) BuildPayload(provider, modelID, effort string) ProviderPayload {
	// Normalize openrouter vendor
	provLower := strings.ToLower(provider)
	modelLower := strings.ToLower(modelID)
	if provLower == "openrouter" && strings.Contains(modelLower, "/") {
		parts := strings.SplitN(modelLower, "/", 2)
		provLower = parts[0]
	}
	maxOut := heuristicMaxOutputTokens(provider, modelID)
	if m, ok := findCachedModel(provider, modelID); ok && m.MaxOutputTokens > 0 {
		maxOut = m.MaxOutputTokens
	}
	budget := tm.ThinkingBudget(effort, maxOut)
	pp := ProviderPayload{}
	switch provLower {
	case "openai":
		// Only reasoning models get effort; others get empty
		if ModelSupportsEffortWithProvider(provider, modelID) {
			// effort auto maps to medium per spec default
			eff := strings.ToLower(effort)
			if eff == "auto" || eff == "" {
				eff = "medium"
			}
			pp.ReasoningEffort = eff
		}
		pp.MaxCompletionTokens = maxOut
		// Also set MaxTokens for generic OpenRouter field
		pp.MaxTokens = maxOut
	case "anthropic":
		pp.ThinkingBudget = budget
		if budget > 0 {
			pp.MaxTokens = budget + 4096
		} else {
			pp.MaxTokens = maxOut
		}
	default:
		pp.ThinkingBudget = budget
		if budget > 0 {
			pp.MaxTokens = budget + 4096
		} else {
			pp.MaxTokens = maxOut
		}
		pp.ReasoningEffort = strings.ToLower(effort)
		if pp.ReasoningEffort == "auto" {
			pp.ReasoningEffort = ""
		}
	}
	return pp
}

// FormatTokenCount formats tokens for UI display: 4000→"4k", 16000→"16k".
func FormatTokenCount(n int) string {
	return FormatContextWindow(n)
}
