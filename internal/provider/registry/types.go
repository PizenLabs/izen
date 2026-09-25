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

// Model is the canonical model descriptor alias used by the Phase 1
// provenance-aware cache and snapshot layer. It is identical to
// ModelDescriptor so existing callers keep working unchanged.
type Model = ModelDescriptor

// ModelDescriptor describes a single provider model.
// IneligibleReason marks a discovered-but-not-executable model (see
// eligibility.go): empty means eligible for selection and execution.
// The raw catalog entry is always preserved; only the executable view
// (LoadExecutable/ExecutableModels) and the selection/execution guards
// consult this field.
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
	// IneligibleReason is set when the provider catalog lists the model
	// but it is not executable through Izen's current execution path
	// (e.g. agentic-harness-only models). Empty = eligible.
	IneligibleReason string `json:"ineligible_reason,omitempty"`
}
