package registry

import "time"

// DefaultModels is a cold-start baseline ONLY: it is never used as a live
// registry source. The live registry is always populated from provider
// evidence (API endpoints, tags endpoints). Having hardcoded arrays is
// explicitly prohibited for live catalog synchronization.
func DefaultModels() []ModelDescriptor {
	return []ModelDescriptor{
		{ID: "claude-3-7-sonnet-latest", Name: "Claude 3.7 Sonnet", Provider: "anthropic", ContextWindow: 200000},
		{ID: "gpt-4o", Name: "GPT-4o", Provider: "openai", ContextWindow: 128000},
		{ID: "deepseek-reasoner", Name: "DeepSeek R1", Provider: "deepseek", ContextWindow: 64000},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Provider: "google", ContextWindow: 1000000},
	}
}

// DefaultSnapshot returns a non-empty immutable snapshot seeded from
// DefaultModels with per-provider ok summaries. Version starts at 1 so it
// is distinguishable from an empty snapshot (version 0).
func DefaultSnapshot() *ModelSnapshot {
	models := DefaultModels()
	now := time.Now()
	providers := make([]ProviderSummary, 0, len(models))
	for _, m := range models {
		providers = append(providers, ProviderSummary{
			Name:       m.Provider,
			ModelCount: 1,
			Status:     CacheStatusOK,
			UpdatedAt:  now,
		})
	}
	return &ModelSnapshot{
		Models:    models,
		Providers: providers,
		Version:   1,
		UpdatedAt: now,
	}
}
