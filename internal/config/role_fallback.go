package config

import (
	"strings"
)

// Role fallback chain (explicit, user-configurable).
//
// Izen never reverts a model implicitly. When a primary model fails for a
// network-transient reason (timeout, HTTP 429, HTTP 5xx) the runtime MAY retry
// the turn on an explicitly configured fallback model, and the switch is always
// reported in the trace view. Wire-policy requirements are NOT a fallback
// trigger: they are satisfied transparently by Dynamic Contract Promotion
// (the provider adapter promotes the interaction contract and binds read-only
// tools before dispatch).
//
// Config shape (in ~/.izen/config.yml, the same store as bindings/models):
//
//	roles:
//	  plan:
//	    model:    "openrouter/thinkingmachines/inkling-small:free"
//	    fallback: "openrouter/anthropic/claude-3.5-sonnet"
//	  default:
//	    fallback: "openrouter/anthropic/claude-3.5-sonnet"
//
// Model values are either bare model IDs (resolved against the active
// provider) or provider-qualified "provider/model" slugs.
type RoleFallbackConfig struct {
	// Model is the role's primary model. Empty means "inherit whatever model is
	// active" — only Fallback participates in the chain.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// Fallback is the explicit fallback model for the role. Empty disables the
	// chain for that role (no implicit reversion is ever invented).
	Fallback string `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

// RoleChain is the resolved (provider, model) pair a role's fallback switches
// to, plus the primary it replaces.
type RoleChain struct {
	// Role is the config key the chain was resolved from.
	Role string
	// Primary is the model the chain replaces (the role's declared primary when
	// declared, else the active model at resolution time).
	Primary string
	// Provider is the resolved execution provider for Fallback.
	Provider string
	// Model is the resolved fallback model ID (never provider-qualified).
	Model string
	// Label is the human-facing "provider/model" label used in trace events.
	Label string
}

// LabelOrEmpty returns the "provider/model" label (or just the model when the
// provider is unknown).
func (r RoleChain) LabelOrEmpty() string {
	if r.Label != "" {
		return r.Label
	}
	if r.Provider != "" && r.Model != "" {
		return r.Provider + "/" + r.Model
	}
	return r.Model
}

// SplitProviderModel splits a "provider/model" slug into its two components.
// A bare model ID resolves to an empty provider, which callers interpret as
// "use the provider of the model that failed".
//
// Disambiguation is by provider name, not by slash count: an OpenRouter model
// ID already contains a slash ("thinkingmachines/inkling-small:free"), so
// counting separators would misread "openrouter/anthropic/claude-3.5-sonnet".
// A leading segment is treated as a provider only when it names a provider
// IZEN can actually execute.
func SplitProviderModel(value string) (provider, model string) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", ""
	}
	idx := strings.Index(v, "/")
	if idx <= 0 {
		return "", v
	}
	head := strings.TrimSpace(v[:idx])
	tail := strings.TrimSpace(v[idx+1:])
	if tail == "" {
		return "", v
	}
	if ValidateProviderName(strings.ToLower(head)) {
		return strings.ToLower(head), tail
	}
	return "", v
}

// RoleChainFor resolves the fallback chain for a turn.
//
// Resolution order (first match wins):
//  1. The turn role's own entry, when its declared primary matches activeModel
//     (exact, case-insensitive) or it declares no primary at all.
//  2. Any other configured role whose declared primary matches activeModel
//     (scanned in the deterministic order: the turn role first, then the
//     alphabetical order of the remaining keys) — the chain belongs to the
//     model that failed, not to the surface that invoked it.
//
// activeProvider is the provider of the currently active binding and supplies
// the provider for a bare (unqualified) fallback model ID. The returned
// RoleChain is only meaningful when ok is true, i.e. when a non-empty fallback
// exists that differs from the primary.
func (c *Config) RoleChainFor(role, activeModel, activeProvider string) (RoleChain, bool) {
	if c == nil || len(c.Roles) == 0 {
		return RoleChain{}, false
	}
	primary := strings.TrimSpace(activeModel)
	for _, key := range c.roleChainScanOrder(role) {
		entry, ok := c.Roles[key]
		if !ok {
			continue
		}
		primaryProvider, primaryModel := SplitProviderModel(entry.Model)
		// A role with a different declared primary never claims this turn.
		if primaryModel != "" && primary != "" && !strings.EqualFold(primaryModel, primary) {
			continue
		}
		fbProvider, fbModel := SplitProviderModel(entry.Fallback)
		fbModel = strings.TrimSpace(fbModel)
		if fbModel == "" {
			continue
		}
		if fbProvider == "" {
			// A bare fallback model inherits the role's provider, else the
			// provider of the model that failed.
			if primaryProvider != "" {
				fbProvider = primaryProvider
			} else {
				fbProvider = strings.TrimSpace(activeProvider)
			}
		}
		// A fallback identical to the failed primary is a no-op loop: refuse it.
		if strings.EqualFold(fbModel, primary) {
			continue
		}
		chain := RoleChain{
			Role:     key,
			Primary:  primary,
			Provider: fbProvider,
			Model:    fbModel,
		}
		chain.Label = chain.LabelOrEmpty()
		return chain, true
	}
	return RoleChain{}, false
}

// roleChainScanOrder returns the deterministic role iteration order: the turn
// role first, then the remaining configured roles in lexical key order.
func (c *Config) roleChainScanOrder(role string) []string {
	keys := make([]string, 0, len(c.Roles))
	for k := range c.Roles {
		if k == role {
			continue
		}
		keys = append(keys, k)
	}
	sortStrings(keys)
	turn := strings.TrimSpace(role)
	if _, ok := c.Roles[turn]; ok {
		keys = append([]string{turn}, keys...)
	}
	return keys
}

// sortStrings is a tiny insertion sort so this file stays dependency-free and
// deterministic (role maps are small: at most a handful of keys).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
