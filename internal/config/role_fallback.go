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
//	    model:     "openrouter/thinkingmachines/inkling-small:free"
//	    fallback:  "openrouter/anthropic/claude-3.5-sonnet"
//	  default:
//	    fallbacks:
//	      - "ollama/llama3.2"
//	      - "openrouter/anthropic/claude-3.5-sonnet"
//
// Model values are either bare model IDs (resolved against the active
// provider) or provider-qualified "provider/model" slugs.
//
// # FALLBACK vs FALLBACKS
//
// `fallback` is a single model and `fallbacks` is an ORDERED CHAIN, and they are
// not two spellings of one field. `fallback` is the documented, hand-editable
// single-model form and it remains the FIRST element of the effective chain; a
// chain is what a user builds interactively (the Model Registry's Alt+F toggle),
// where "the model AFTER the one that just failed" is a real question that a
// single slot cannot answer.
//
// So writing a chain means writing BOTH: Fallbacks carries the order, and
// Fallback mirrors element zero — so a hand-edited config that only knows about
// `fallback` keeps working, and a reader that only knows about `fallback` sees a
// truthful first hop rather than nothing. SetRoleFallbackChain is the one writer
// that keeps the two in step.
type RoleFallbackConfig struct {
	// Model is the role's primary model. Empty means "inherit whatever model is
	// active" — only the fallback chain participates in the switch.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// Fallback is the explicit fallback model for the role, and the first element
	// of Fallbacks. Empty, with an empty Fallbacks, disables the chain for that
	// role (no implicit reversion is ever invented).
	Fallback string `yaml:"fallback,omitempty" json:"fallback,omitempty"`

	// Fallbacks is the ordered fallback chain for the role, consulted in order
	// after the primary fails for a network-transient reason. See the section
	// comment for how it relates to Fallback.
	Fallbacks []string `yaml:"fallbacks,omitempty" json:"fallbacks,omitempty"`

	// Params carries the role's OPERATIONAL parameters — attempt budget,
	// per-call deadline, and which refusals advance the chain. It is inlined
	// into the role's own YAML key rather than nested under a `params:` block
	// so that hand-editing one number is a one-line diff. See role_config.go,
	// which also owns every writer for it.
	//
	// A role entry that declares only Params is a legitimately configured
	// role: it has a primary (inherited), no chain, and a budget the user set.
	Params RoleParamsConfig `yaml:",inline" json:",inline"`
}

// Chain returns the effective ORDERED chain for this role.
//
// The two forms are MERGED rather than chosen between — Fallback first — so a
// config that declares only `fallback` and one that declares only `fallbacks`
// produce the same shape to every reader, and a config that (transiently)
// declares both cannot disagree with itself.
//
// Duplicates are dropped case-insensitively with the first occurrence winning.
// A chain that names the same model twice is a retry loop against a provider
// that has already refused it: the runtime would spend a whole request
// discovering that, and a user who meant to write it deserves to see it collapse
// on screen rather than discover it as a mysterious extra request in the trace.
func (r RoleFallbackConfig) Chain() []string {
	single := strings.TrimSpace(r.Fallback)
	if len(r.Fallbacks) == 0 {
		if single == "" {
			return nil
		}
		return []string{single}
	}
	out := make([]string, 0, len(r.Fallbacks)+1)
	seen := make(map[string]struct{}, len(r.Fallbacks)+1)
	appendModel := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		key := strings.ToLower(v)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	appendModel(single)
	for _, v := range r.Fallbacks {
		appendModel(v)
	}
	return out
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

// RoleChainFor resolves the NEXT HOP of the fallback chain for a turn.
//
// Resolution order (first match wins):
//  1. The turn role's own entry, when its declared primary matches activeModel
//     (exact, case-insensitive) or it declares no primary at all.
//  2. Any other configured role whose declared primary matches activeModel
//     (scanned in the deterministic order: the turn role first, then the
//     alphabetical order of the remaining keys) — the chain belongs to the
//     model that failed, not to the surface that invoked it.
//
// Within the matching role, the chain is walked IN ORDER and the first entry
// that resolves to a provider and differs from activeModel is returned. A
// multi-entry chain is therefore not a loop: each retry that resolves the next
// hop advances the position, because the caller passes the model that just
// failed back in as activeModel. That is what makes "1. a -> 2. b" mean
// "try a, then b" rather than "try a, and also b, forever".
//
// activeProvider is the provider of the currently active binding and supplies
// the provider for a bare (unqualified) fallback model ID. The returned
// RoleChain is only meaningful when ok is true, i.e. when a next hop exists that
// differs from the primary.
func (c *Config) RoleChainFor(role, activeModel, activeProvider string) (RoleChain, bool) {
	if c == nil || len(c.Roles) == 0 {
		return RoleChain{}, false
	}
	// The primary is normalised to a BARE model ID before any comparison, and
	// that is not cosmetic. Chain entries are stored as slugs ("ollama/llama3.2")
	// because a bare ID is resolved against whatever provider happens to be bound
	// at turn time, but the CALLER may hold either form — a request's Model field
	// is bare, a config-read or a trace string is a slug. Comparing a bare primary
	// against a slugged hop would never match, so hop 1 would look like a valid
	// target and the chain would switch a model to itself.
	_, primary := SplitProviderModel(activeModel)
	primary = strings.TrimSpace(primary)
	if primary == "" {
		primary = strings.TrimSpace(activeModel)
	}
	for _, key := range c.roleChainScanOrder(role) {
		entry, ok := c.Roles[key]
		if !ok {
			continue
		}
		primaryProvider, declaredPrimary := SplitProviderModel(entry.Model)
		// A role with a different declared primary never claims this turn.
		if declaredPrimary != "" && primary != "" && !strings.EqualFold(declaredPrimary, primary) {
			continue
		}
		for _, candidate := range entry.Chain() {
			fbProvider, fbModel := SplitProviderModel(candidate)
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
			// A hop identical to the failed model is a no-op loop: refuse it and
			// keep walking, because a chain whose SECOND entry is the model that
			// just failed should still reach its third.
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
	}
	return RoleChain{}, false
}

// RoleFallbackChain returns the effective ordered chain for a role, or nil when
// the role is unconfigured. It is the read side of the Model Registry's
// fallback editor: the widget is seeded from this and every user edit is written
// back through SetRoleFallbackChain, so the widget never derives the chain
// itself and the file on disk is never the widget's private state.
func (c *Config) RoleFallbackChain(role string) []string {
	if c == nil {
		return nil
	}
	entry, ok := c.Roles[strings.TrimSpace(role)]
	if !ok {
		return nil
	}
	chain := entry.Chain()
	if len(chain) == 0 {
		return nil
	}
	return chain
}

// SetRoleFallbackChain writes the ordered chain for a role, keeping Fallback and
// Fallbacks in step (see RoleFallbackConfig). It creates the role entry when
// needed and removes the entry entirely when the chain becomes empty, so a
// cleared chain leaves no `roles: {default: {}}` husk behind to be read as a
// configured role.
//
// Persistence is the CALLER's job: this mutates the in-memory config, and the
// caller decides when to write ~/.izen/config.yml. That separation is what
// makes the Model Registry's two-step confirm honest — the widget stages an
// edit, the user confirms, and only then does anything touch the disk.
func (c *Config) SetRoleFallbackChain(role string, chain []string) {
	if c == nil {
		return
	}
	key := strings.TrimSpace(role)
	if key == "" {
		return
	}
	cleaned := normalizeChain(chain)
	if len(cleaned) == 0 {
		if c.Roles == nil {
			return
		}
		delete(c.Roles, key)
		return
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleFallbackConfig)
	}
	entry := c.Roles[key]
	entry.Fallback = cleaned[0]
	entry.Fallbacks = append([]string(nil), cleaned...)
	c.Roles[key] = entry
}

// ToggleRoleFallback adds model to the role's chain, or removes it when it is
// already present. It reports the resulting chain and whether the model is now
// IN it.
//
// Removal preserves the order of what remains, so a chain a user has carefully
// ordered does not reshuffle because they removed its third element. Adding
// appends, because a chain is a queue of "and then what?" — prepending a
// fallback would silently reorder hops the user never touched.
func (c *Config) ToggleRoleFallback(role, model string) (chain []string, inChain bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return c.RoleFallbackChain(role), false
	}
	current := c.RoleFallbackChain(role)
	for i, existing := range current {
		if strings.EqualFold(existing, model) {
			return append(append([]string(nil), current[:i]...), current[i+1:]...), false
		}
	}
	return append(append([]string(nil), current...), model), true
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
