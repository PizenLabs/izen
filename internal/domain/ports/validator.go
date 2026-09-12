package ports

import "context"

// ModelInfo is the provider-agnostic model record a ModelValidator reasons
// over. Adapters translate concrete provider records into this shape so the
// domain never imports provider packages.
type ModelInfo struct {
	ID            string
	Provider      string
	Name          string
	ContextWindow int
	Capabilities  []string
	IsThinking    bool
}

// ModelValidator is the provider-agnostic port for model capability checks.
// Concrete validators live outside internal/domain (e.g. internal/app/model)
// and are injected where role resolution needs provider-specific knowledge.
type ModelValidator interface {
	// ValidateModel reports whether the named model satisfies the capability
	// requirements for the given role.
	ValidateModel(ctx context.Context, role, modelID string) error
	// Capabilities returns the capability flags for the named model.
	Capabilities(ctx context.Context, modelID string) ([]string, error)
}
