package llm

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
		InputCostPerM: 0.27, OutputCostPerM: 1.10, ContextWindow: 64000,
	},
	"deepseek-reasoner": {
		ID: "deepseek-reasoner", Name: "DeepSeek R1", Provider: "deepseek",
		InputCostPerM: 0.55, OutputCostPerM: 2.19, ContextWindow: 64000,
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
