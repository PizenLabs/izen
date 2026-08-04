// Package strategy implements the built-in execution strategies of the Izen
// v1 runtime: DirectGenerationStrategy for LOW scope / small token budget
// tasks and IterativeStrategy for scopes that need multi-step editing or tool
// calls. Both strategies are pure Strategy plugins (see
// pkg/runtime/registry) and exchange only the primitive registry.Task and
// registry.Result contracts.
//
// The strategies perform their LLM generation through the CapabilityRegistry:
// a ProviderRouter resolves the capability's provider names and dispatches to
// the first provider with a bound generation backend.
package strategy

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// GenerationRequest is a single non-streaming LLM generation pass.
type GenerationRequest struct {
	System    string
	Prompt    string
	MaxTokens int
}

// GenerationResult is the output of a single generation pass.
type GenerationResult struct {
	Text   string
	Tokens int
}

// Generator performs LLM generation. Strategies use it to produce file
// content and ReAct actions. Implementations must be safe for concurrent
// use.
type Generator interface {
	Complete(ctx context.Context, req GenerationRequest) (GenerationResult, error)
}

// ErrNoGenerator is returned when a strategy is executed without a bound
// generator.
var ErrNoGenerator = errors.New("strategy: no generator bound")

// ErrNoProvider is returned when the capability registry exposes no provider
// with a bound generation backend.
var ErrNoProvider = errors.New("strategy: no provider bound for capability")

// ErrNoToolRunner is returned when the iterative strategy is executed
// without a tool runner.
var ErrNoToolRunner = errors.New("strategy: no tool runner bound")

// ProviderRouter routes generation through the CapabilityRegistry: it reads
// the capability's provider names from the registry and dispatches to the
// first provider that has a bound backend. It is the bridge that makes
// generation "happen via the capability registry".
type ProviderRouter struct {
	capabilities *registry.CapabilityRegistry
	capability   registry.Capability
	backends     map[string]Generator
}

// NewProviderRouter returns a router that resolves the given capability's
// providers through the registry and dispatches to bound backends.
func NewProviderRouter(capabilities *registry.CapabilityRegistry, capability registry.Capability, backends map[string]Generator) *ProviderRouter {
	cp := make(map[string]Generator, len(backends))
	for name, gen := range backends {
		cp[name] = gen
	}
	return &ProviderRouter{
		capabilities: capabilities,
		capability:   capability,
		backends:     cp,
	}
}

// Bind registers a generation backend for a provider name. It is idempotent
// (a later bind for the same name replaces the earlier one).
func (r *ProviderRouter) Bind(name string, gen Generator) {
	if gen == nil {
		return
	}
	r.backends[name] = gen
}

// Complete resolves the capability providers in registry order and returns
// the first bound backend's result.
func (r *ProviderRouter) Complete(ctx context.Context, req GenerationRequest) (GenerationResult, error) {
	if r == nil || r.capabilities == nil {
		return GenerationResult{}, ErrNoProvider
	}
	for _, name := range r.capabilities.ProvidersFor(r.capability) {
		if gen, ok := r.backends[name]; ok {
			return gen.Complete(ctx, req)
		}
	}
	return GenerationResult{}, fmt.Errorf("%w: %s", ErrNoProvider, r.capability)
}
