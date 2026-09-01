package llm

import (
	"fmt"
	"strings"
	"time"
)

type ModelMetadata struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Provider           string  `json:"provider"`
	InputCostPerM      float64 `json:"input_cost_per_m"`
	OutputCostPerM     float64 `json:"output_cost_per_m"`
	CacheWriteCostPerM float64 `json:"cache_write_cost_per_m"`
	CacheReadCostPerM  float64 `json:"cache_read_cost_per_m"`
	ContextWindow      int     `json:"context_window"`
}

var modelCatalog = map[string]ModelMetadata{
	// Anthropic — Claude 4 (2025-05-14)
	"claude-sonnet-4-20250514": {
		ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4", Provider: "anthropic",
		InputCostPerM: 3, OutputCostPerM: 15, CacheWriteCostPerM: 3.75, CacheReadCostPerM: 0.30, ContextWindow: 200000,
	},
	"claude-4-20250514": {
		ID: "claude-4-20250514", Name: "Claude 4", Provider: "anthropic",
		InputCostPerM: 3, OutputCostPerM: 15, CacheWriteCostPerM: 3.75, CacheReadCostPerM: 0.30, ContextWindow: 200000,
	},
	"claude-opus-4-20250514": {
		ID: "claude-opus-4-20250514", Name: "Claude Opus 4", Provider: "anthropic",
		InputCostPerM: 15, OutputCostPerM: 75, CacheWriteCostPerM: 18.75, CacheReadCostPerM: 1.50, ContextWindow: 200000,
	},
	// Anthropic — Claude 3.5
	"claude-3-5-sonnet-20241022": {
		ID: "claude-3-5-sonnet-20241022", Name: "Claude 3.5 Sonnet", Provider: "anthropic",
		InputCostPerM: 3, OutputCostPerM: 15, CacheWriteCostPerM: 3.75, CacheReadCostPerM: 0.30, ContextWindow: 200000,
	},
	"claude-3-5-haiku-20241022": {
		ID: "claude-3-5-haiku-20241022", Name: "Claude 3.5 Haiku", Provider: "anthropic",
		InputCostPerM: 0.80, OutputCostPerM: 4, CacheWriteCostPerM: 1, CacheReadCostPerM: 0.08, ContextWindow: 200000,
	},
	// Anthropic — Claude 3
	"claude-3-opus-20240229": {
		ID: "claude-3-opus-20240229", Name: "Claude 3 Opus", Provider: "anthropic",
		InputCostPerM: 15, OutputCostPerM: 75, CacheWriteCostPerM: 18.75, CacheReadCostPerM: 1.50, ContextWindow: 200000,
	},
	"claude-3-sonnet-20240229": {
		ID: "claude-3-sonnet-20240229", Name: "Claude 3 Sonnet", Provider: "anthropic",
		InputCostPerM: 3, OutputCostPerM: 15, CacheWriteCostPerM: 3.75, CacheReadCostPerM: 0.30, ContextWindow: 200000,
	},
	"claude-3-haiku-20240307": {
		ID: "claude-3-haiku-20240307", Name: "Claude 3 Haiku", Provider: "anthropic",
		InputCostPerM: 0.25, OutputCostPerM: 1.25, CacheWriteCostPerM: 0.30, CacheReadCostPerM: 0.03, ContextWindow: 200000,
	},
	// OpenAI
	"gpt-4o": {
		ID: "gpt-4o", Name: "GPT-4o", Provider: "openai",
		InputCostPerM: 2.50, OutputCostPerM: 10, ContextWindow: 128000,
	},
	"gpt-4o-mini": {
		ID: "gpt-4o-mini", Name: "GPT-4o mini", Provider: "openai",
		InputCostPerM: 0.15, OutputCostPerM: 0.60, ContextWindow: 128000,
	},
	"gpt-4-turbo": {
		ID: "gpt-4-turbo", Name: "GPT-4 Turbo", Provider: "openai",
		InputCostPerM: 10, OutputCostPerM: 30, ContextWindow: 128000,
	},
	"gpt-4": {
		ID: "gpt-4", Name: "GPT-4", Provider: "openai",
		InputCostPerM: 30, OutputCostPerM: 60, ContextWindow: 8192,
	},
	"gpt-3.5-turbo": {
		ID: "gpt-3.5-turbo", Name: "GPT-3.5 Turbo", Provider: "openai",
		InputCostPerM: 0.50, OutputCostPerM: 1.50, ContextWindow: 16385,
	},
	"o1": {
		ID: "o1", Name: "o1", Provider: "openai",
		InputCostPerM: 15, OutputCostPerM: 60, ContextWindow: 200000,
	},
	"o1-mini": {
		ID: "o1-mini", Name: "o1-mini", Provider: "openai",
		InputCostPerM: 1.10, OutputCostPerM: 4.40, ContextWindow: 128000,
	},
	"o3-mini": {
		ID: "o3-mini", Name: "o3-mini", Provider: "openai",
		InputCostPerM: 1.10, OutputCostPerM: 4.40, ContextWindow: 200000,
	},
	// DeepSeek
	"deepseek-chat": {
		ID: "deepseek-chat", Name: "DeepSeek V3", Provider: "deepseek",
		InputCostPerM: 0.27, OutputCostPerM: 1.10, ContextWindow: 128000,
	},
	"deepseek-reasoner": {
		ID: "deepseek-reasoner", Name: "DeepSeek R1", Provider: "deepseek",
		InputCostPerM: 0.55, OutputCostPerM: 2.19, ContextWindow: 128000,
	},
	// Gemini
	"gemini-1.5-pro": {
		ID: "gemini-1.5-pro", Name: "Gemini 1.5 Pro", Provider: "gemini",
		InputCostPerM: 1.25, OutputCostPerM: 5, ContextWindow: 1000000,
	},
	"gemini-1.5-flash": {
		ID: "gemini-1.5-flash", Name: "Gemini 1.5 Flash", Provider: "gemini",
		InputCostPerM: 0.075, OutputCostPerM: 0.30, ContextWindow: 1000000,
	},
}

func GetModelMetadata(modelID string) *ModelMetadata {
	if m, ok := modelCatalog[modelID]; ok {
		return &m
	}
	for _, m := range modelCatalog {
		if m.ID == modelID {
			return &m
		}
	}
	return nil
}

// ModelSupportsEffort reports whether the model exposes a reasoning/thinking
// control (Anthropic Extended Thinking, OpenAI o1/o3, DeepSeek-R1, etc.).
// It is implemented as a strict explicit whitelist resolver based on
// (provider, modelID) — no brittle substring heuristics — so non-reasoning
// models like qwen2.5 or aion-labs/aion-3.0 never return true.
//
// For aggregators such as OpenRouter (provider == "openrouter"), the model
// ID carries the vendor prefix (e.g. "openai/o1", "anthropic/claude-3-7-sonnet-...").
// For native providers the provider argument disambiguates bare IDs (e.g.
// provider "openai" + ID "o1").
func ModelSupportsEffort(modelID string) bool {
	return ModelSupportsEffortWithProvider("", modelID)
}

// ModelSupportsEffortWithProvider is the capability resolver with fallback chain:
// Local Override -> API Endpoint Metadata (cached schema) -> Heuristic Spec Fallback.
// It first consults dynamic discovery (override and cached API metadata with
// supported_parameters), then falls back to the explicit whitelist for
// offline/heuristic cases.
func ModelSupportsEffortWithProvider(provider, modelID string) bool {
	// 1. Dynamic: Local Override
	if ov, ok := lookupOverrideSupportsReasoning(provider, modelID); ok {
		return ov
	}
	// 2. Dynamic: Cached API metadata (24h TTL)
	if cached, ok := lookupCachedSupportsReasoning(provider, modelID); ok {
		return cached
	}
	// 3. Heuristic whitelist fallback
	return heuristicSupportsReasoning(provider, modelID)
}

func lookupOverrideSupportsReasoning(provider, modelID string) (bool, bool) {
	overrides := loadOverrides()
	if len(overrides) == 0 {
		return false, false
	}
	// Try provider/id, bare id, and openrouter vendor forms
	candidates := []string{
		modelID,
		provider + "/" + modelID,
		strings.ToLower(provider + "/" + modelID),
		strings.ToLower(modelID),
	}
	// For openrouter vendor-prefixed IDs, also try bare
	lowerID := strings.ToLower(modelID)
	if idx := strings.Index(lowerID, "/"); idx >= 0 {
		candidates = append(candidates, lowerID[idx+1:])
	}
	for _, k := range candidates {
		if ov, ok := overrides[k]; ok && ov.SupportsReasoning != nil {
			return *ov.SupportsReasoning, true
		}
		if ov, ok := overrides[strings.ToLower(k)]; ok && ov.SupportsReasoning != nil {
			return *ov.SupportsReasoning, true
		}
	}
	// Also try case-insensitive map lookup
	for key, ov := range overrides {
		if ov.SupportsReasoning == nil {
			continue
		}
		if strings.EqualFold(key, modelID) || strings.EqualFold(key, provider+"/"+modelID) {
			return *ov.SupportsReasoning, true
		}
	}
	return false, false
}

func lookupCachedSupportsReasoning(provider, modelID string) (bool, bool) {
	cached, ok := loadCachedIfFresh(24 * time.Hour)
	if !ok || len(cached) == 0 {
		return false, false
	}
	// Search cached models for matching provider/id
	lowerProv := strings.ToLower(provider)
	lowerID := strings.ToLower(modelID)
	for _, m := range cached {
		// Match provider and ID, or for openrouter vendor-prefixed IDs
		if strings.EqualFold(m.Provider, provider) && strings.EqualFold(m.ID, modelID) {
			if m.SupportsReasoning != nil {
				return *m.SupportsReasoning, true
			}
		}
		// Also try matching openrouter vendor form
		if m.Provider == "openrouter" && strings.EqualFold(m.ID, modelID) && m.SupportsReasoning != nil {
			return *m.SupportsReasoning, true
		}
		// Try bare ID match when provider is openrouter and cached is vendor-prefixed
		if lowerProv == "openrouter" && strings.Contains(lowerID, "/") {
			if strings.EqualFold(m.ID, modelID) && m.SupportsReasoning != nil {
				return *m.SupportsReasoning, true
			}
		}
		// Try reverse: provider openai with ID o1 should match cached openrouter openai/o1
		if provider == "openai" && m.Provider == "openrouter" && strings.EqualFold(m.ID, "openai/"+modelID) && m.SupportsReasoning != nil {
			return *m.SupportsReasoning, true
		}
		if provider == "anthropic" && m.Provider == "openrouter" && strings.EqualFold(m.ID, "anthropic/"+modelID) && m.SupportsReasoning != nil {
			return *m.SupportsReasoning, true
		}
		if provider == "deepseek" && m.Provider == "openrouter" && strings.EqualFold(m.ID, "deepseek/"+modelID) && m.SupportsReasoning != nil {
			return *m.SupportsReasoning, true
		}
	}
	// Also check supported_parameters heuristic from cached SupportedParameters
	for _, m := range cached {
		if strings.EqualFold(m.Provider, provider) && strings.EqualFold(m.ID, modelID) {
			if len(m.SupportedParameters) > 0 {
				for _, p := range m.SupportedParameters {
					lower := strings.ToLower(p)
					if lower == "reasoning" || lower == "reasoning_effort" || lower == "thinking" {
						return true, true
					}
				}
				return false, true
			}
		}
	}
	return false, false
}

func heuristicSupportsReasoning(provider, modelID string) bool {
	prov := strings.ToLower(strings.TrimSpace(provider))
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	vendor := ""
	bare := id
	if idx := strings.Index(id, "/"); idx >= 0 {
		vendor = strings.ToLower(strings.TrimSpace(id[:idx]))
		bare = strings.ToLower(strings.TrimSpace(id[idx+1:]))
		if prov == "openrouter" {
			prov = vendor
			id = bare
		} else if vendor != "" && prov == "" {
			prov = vendor
			id = bare
		}
	} else {
		id = bare
	}
	switch prov {
	case "openai":
		if strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") {
			return true
		}
		return false
	case "anthropic":
		if strings.HasPrefix(id, "claude-3-7-sonnet") {
			return true
		}
		return false
	case "deepseek":
		if strings.HasPrefix(id, "deepseek-r1") {
			return true
		}
		return false
	case "openrouter":
		if strings.HasPrefix(id, "openai/o1") || strings.HasPrefix(id, "openai/o3") ||
			strings.HasPrefix(id, "anthropic/claude-3-7-sonnet") ||
			strings.HasPrefix(id, "deepseek/deepseek-r1") {
			return true
		}
		return false
	default:
		if strings.HasPrefix(id, "openai/o1") || strings.HasPrefix(id, "openai/o3") ||
			strings.HasPrefix(id, "anthropic/claude-3-7-sonnet") ||
			strings.HasPrefix(id, "deepseek/deepseek-r1") {
			return true
		}
		return false
	}
}

// SupportsEffort is the canonical resolver used by UI and providers.
// It first checks the ModelInfo's dynamic SupportsReasoning field (from
// registry discovery), then falls back to provider/ID heuristic.
func SupportsEffort(m ModelInfo) bool {
	if m.SupportsReasoning != nil {
		return *m.SupportsReasoning
	}
	return ModelSupportsEffortWithProvider(m.Provider, m.ID)
}

// EffortLevelsFor returns the exact valid effort option enums per provider spec.
// OpenAI (o1, o3-mini): ["low","medium","high"] default medium (auto maps to medium).
// Anthropic (Claude Extended Thinking): mapped to thinking budget tiers but exposed
// as the same qualitative levels that translate to budget_tokens via the decision engine.
func EffortLevelsFor(provider, modelID string) []string {
	if !ModelSupportsEffortWithProvider(provider, modelID) {
		return nil
	}
	prov := strings.ToLower(strings.TrimSpace(provider))
	// Normalise openrouter vendor
	if prov == "openrouter" {
		lowerID := strings.ToLower(modelID)
		if idx := strings.Index(lowerID, "/"); idx >= 0 {
			prov = strings.ToLower(strings.TrimSpace(lowerID[:idx]))
		}
	}
	switch prov {
	case "openai":
		// Official OpenAI reasoning_effort values
		return []string{"low", "medium", "high"}
	case "anthropic":
		// Anthropic thinking.budget_tokens is mapped from the same qualitative levels
		// via the decision engine (low=1024, medium=4096, high=8192+). The valid API
		// tiers are the same strings passed through OpenRouter.
		return []string{"low", "medium", "high"}
	case "deepseek":
		return []string{"low", "medium", "high"}
	default:
		return []string{"low", "medium", "high"}
	}
}

// DefaultEffortFor returns the provider's default effort when the user has not
// selected one. OpenAI defaults to "medium" per spec.
func DefaultEffortFor(provider, modelID string) string {
	if !ModelSupportsEffortWithProvider(provider, modelID) {
		return ""
	}
	prov := strings.ToLower(strings.TrimSpace(provider))
	if prov == "openrouter" {
		lowerID := strings.ToLower(modelID)
		if idx := strings.Index(lowerID, "/"); idx >= 0 {
			prov = strings.ToLower(strings.TrimSpace(lowerID[:idx]))
		}
	}
	switch prov {
	case "openai", "anthropic", "deepseek":
		return "medium"
	default:
		return "medium"
	}
}

// FormatContextWindow returns a human badge like "200k", "128k", "1M".
func FormatContextWindow(n int) string {
	if n <= 0 {
		return ""
	}
	if n >= 1_000_000 {
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		// Non-round millions: show one decimal, e.g. 1.5M
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		if n%1000 == 0 {
			return fmt.Sprintf("%dk", n/1000)
		}
		// 128000 -> 128k even though divisible, handled above; 16385 -> 16k approx
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// ModelCapabilities returns capability flags for a model, e.g. ["Vision", "Tools"].
func ModelCapabilities(modelID string) []string {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if lower == "" {
		return nil
	}
	var caps []string
	// Vision heuristic: Claude, GPT-4o, Gemini, vision-tagged models.
	if strings.Contains(lower, "claude") || strings.Contains(lower, "gpt-4o") || strings.Contains(lower, "gemini") || strings.Contains(lower, "vision") || strings.Contains(lower, "llava") {
		caps = append(caps, "Vision")
	}
	// Tools: most modern models support tool calling; gemma is the main exception.
	if !strings.Contains(lower, "gemma") {
		caps = append(caps, "Tools")
	}
	return caps
}

// ContextWindowFor returns the model's maximum context window. It first
// consults the curated catalog; for provider-prefixed IDs (OpenRouter-style
// "provider/model" slugs) it falls back to a family heuristic so common
// models resolve to a sensible window without a network call. Returns 0
// when the model is unknown and no confident guess exists.
func ContextWindowFor(modelID string) int {
	if meta := GetModelMetadata(modelID); meta != nil {
		return meta.ContextWindow
	}

	lower := strings.ToLower(modelID)
	switch {
	case strings.Contains(lower, "gemini"):
		return 1_000_000
	case strings.Contains(lower, "claude"):
		return 200_000
	case strings.Contains(lower, "gpt-4"):
		return 128_000
	case strings.Contains(lower, "gpt-3.5"):
		return 16_385
	case strings.Contains(lower, "o1"), strings.Contains(lower, "o3"):
		return 200_000
	case strings.Contains(lower, "deepseek"):
		return 128_000
	case strings.Contains(lower, "north"), strings.Contains(lower, "command"):
		return 128_000
	case strings.Contains(lower, "llama"):
		return 128_000
	case strings.Contains(lower, "qwen2.5"):
		return 32_768
	default:
		return 0
	}
}
