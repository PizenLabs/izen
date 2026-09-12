package role

// ModelCapability is the provider-agnostic feature flag for a model.
// It mirrors the provider registry capability vocabulary ("tools",
// "vision", "thinking") without importing any provider package so the
// domain layer stays zero-I/O and zero-external-driver.
type ModelCapability string

const (
	CapTools    ModelCapability = "tools"
	CapVision   ModelCapability = "vision"
	CapThinking ModelCapability = "thinking"
)

// ModelDescriptor is the provider-agnostic model record the role engine
// reasons over. Adapters outside internal/domain translate concrete
// provider records into this shape before calling ResolveRoleModel.
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
