package registry

import (
	"sort"
	"sync"
)

// Capability is a resource a strategy may require during execution.
type Capability string

const (
	// CapabilityToolUse allows strategies to invoke workspace tooling.
	CapabilityToolUse Capability = "tool_use"
	// CapabilityCoding allows strategies to generate or modify code.
	CapabilityCoding Capability = "coding"
	// CapabilityContextLarge allows strategies to request a large context
	// budget.
	CapabilityContextLarge Capability = "context_size:large"
	// CapabilityContextSmall allows strategies to request a small context
	// budget.
	CapabilityContextSmall Capability = "context_size:small"
	// CapabilityTest allows strategies to run the workspace test suite.
	CapabilityTest Capability = "test"
)

// knownCapabilities is the canonical set used to disambiguate bare rule
// grants.
var knownCapabilities = map[Capability]struct{}{
	CapabilityToolUse:      {},
	CapabilityCoding:       {},
	CapabilityContextLarge: {},
	CapabilityContextSmall: {},
	CapabilityTest:         {},
}

// IsKnown reports whether c is one of the canonical capabilities.
func (c Capability) IsKnown() bool {
	_, ok := knownCapabilities[c]
	return ok
}

// CapabilityRegistry maps each required capability to the providers that can
// satisfy it (for example "tool_use" -> ["shell"], "coding" -> ["anthropic",
// "openai"]). It is safe for concurrent use.
type CapabilityRegistry struct {
	mu        sync.RWMutex
	providers map[Capability][]string
}

// NewCapabilityRegistry returns an empty capability registry.
func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{providers: make(map[Capability][]string)}
}

// Register binds one or more providers to a capability. An empty provider
// list is an error; duplicate providers within one call are ignored.
func (r *CapabilityRegistry) Register(c Capability, providers ...string) error {
	if len(providers) == 0 {
		return errNoProviders
	}
	seen := make(map[string]struct{}, len(providers))
	deduped := make([]string, 0, len(providers))
	for _, p := range providers {
		if p == "" {
			return errEmptyProvider
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		deduped = append(deduped, p)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[c] = append([]string(nil), deduped...)
	return nil
}

// ProvidersFor returns the providers bound to a capability in deterministic
// order, or nil when the capability has no provider.
func (r *CapabilityRegistry) ProvidersFor(c Capability) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := append([]string(nil), r.providers[c]...)
	sort.Strings(providers)
	return providers
}

// Resolve splits the required capabilities into the satisfied ones (with
// their providers) and the unmet ones that have no provider.
func (r *CapabilityRegistry) Resolve(required []Capability) (map[Capability][]string, []Capability) {
	satisfied := make(map[Capability][]string, len(required))
	var unmet []Capability
	for _, c := range required {
		providers := r.ProvidersFor(c)
		if len(providers) == 0 {
			unmet = append(unmet, c)
			continue
		}
		satisfied[c] = providers
	}
	sort.Slice(unmet, func(i, j int) bool { return unmet[i] < unmet[j] })
	return satisfied, unmet
}
