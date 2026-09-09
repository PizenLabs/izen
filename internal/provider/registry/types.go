// Package registry implements the Phase 1 async model registry: a
// thread-safe, non-blocking in-memory catalog backed by a local JSON cache
// (~/.izen/cache/models.json) with zero-I/O RAM filtering for TUI lookups.
package registry

// ModelCapability is a model feature flag.
type ModelCapability string

const (
	CapTools    ModelCapability = "tools"
	CapVision   ModelCapability = "vision"
	CapThinking ModelCapability = "thinking"
)

// ModelDescriptor describes a single provider model.
type ModelDescriptor struct {
	ID              string            `json:"id"`
	Provider        string            `json:"provider"`
	Name            string            `json:"name"`
	ContextWindow   int               `json:"context_window"`
	MaxOutputTokens int               `json:"max_output_tokens"`
	Capabilities    []ModelCapability `json:"capabilities"`
	IsThinking      bool              `json:"is_thinking"`
	InputCostPerM   float64           `json:"input_cost_per_m"`
	OutputCostPerM  float64           `json:"output_cost_per_m"`
}
