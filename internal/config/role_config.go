package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

// Role operational parameters.
//
// A role's fallback chain answers "which model next". These parameters answer
// "when, and how many times" — and they are per role, because the right answer
// differs by the work: a plan turn can afford two 30-second retries on a
// provider that is merely rate limiting, while a commit turn cannot afford to
// wait at all, and a role whose model refuses on a 400 because the prompt
// exceeded its context window needs a DIFFERENT model rather than a retry of
// the same one.
//
// # WHY MaxRetries ZERO MEANS "UNSET"
//
// The documented default is 2, and a config that omits the field must get 2.
// With a plain int there is no way to tell "the user wrote 0" from "the user
// wrote nothing", so 0 resolves to the default and 1..MaxRoleMaxRetries are the
// explicit choices. That costs one configuration ("never retry"), and it buys
// something worth more: a hand-edited config that omits the field and a config
// the Model Registry wrote from the effective value agree byte for byte, so
// opening the editor on a role the user has never opened does not silently
// rewrite their file with a default they never chose.
//
// A role that genuinely wants no retry expresses it the way it already
// expresses "do not switch models at all": an empty chain. There is no third
// state to confuse.
//
// # WHY THE TRIGGERS ARE TRI-STATE
//
// Each trigger is a *bool, and nil means "the documented default" (429 and 5xx
// on, context-length off). A plain bool cannot express this: the default set is
// two-on-one-off, so a user who turns the 5xx trigger OFF writes {false,false,
// false} — which is indistinguishable from an entry that was never written, and
// which would come back to life as two-on-one-off the next time the config is
// read. A flag that resurrects itself is worse than a flag that is missing, so
// the pointer is load-bearing rather than a convenience.
const (
	// DefaultRoleMaxRetries is the fallback attempt budget for a role that
	// declares none.
	DefaultRoleMaxRetries = 2
	// MaxRoleMaxRetries bounds the selectable budget. A chain has a finite
	// number of entries, and a budget past that is a number that reads as
	// meaningful in the config and is not.
	MaxRoleMaxRetries = 10
	// DefaultRoleTimeoutSeconds is 0, meaning "no per-call deadline beyond the
	// provider profile's own". It is the honest default: inventing a deadline
	// here would silently cut off slow-but-healthy long-context models.
	DefaultRoleTimeoutSeconds = 0
	// MaxRoleTimeoutSeconds bounds the selectable per-call deadline.
	MaxRoleTimeoutSeconds = 3600
)

// RoleTriggerConfig declares which provider responses advance a role's
// fallback chain. All three fields are tri-state: nil is the documented
// default, non-nil is the user's explicit choice.
type RoleTriggerConfig struct {
	// RateLimit advances the chain on HTTP 429.
	RateLimit *bool `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	// ServerError advances the chain on HTTP 5xx.
	ServerError *bool `yaml:"server_error,omitempty" json:"server_error,omitempty"`
	// ContextLength advances the chain when the provider refuses because the
	// prompt exceeded the model's context window.
	ContextLength *bool `yaml:"context_length,omitempty" json:"context_length,omitempty"`
}

// DefaultRoleTriggerConfig returns the documented default trigger set: the two
// network-transient refusals the fallback chain has always fired on, and not
// the context-window refusal, which is a different class of failure (a larger
// model is the fix, and a blind retry of the same model is not).
func DefaultRoleTriggerConfig() RoleTriggerConfig {
	return RoleTriggerConfig{
		RateLimit:     boolPtr(true),
		ServerError:   boolPtr(true),
		ContextLength: boolPtr(false),
	}
}

// RoleParamsConfig is one role's operational parameter block, inlined into
// RoleFallbackConfig so the YAML stays flat under the role's own key:
//
//	roles:
//	  plan:
//	    model: "openrouter/anthropic/claude-sonnet-4"
//	    fallbacks: ["ollama/llama3.2"]
//	    max_retries: 3
//	    timeout_seconds: 45
//	    fallback_triggers:
//	      rate_limit: true
//	      server_error: true
//	      context_length: false
//
// Flat is the right shape here because the alternative — a nested
// `params:` block — makes a hand edit that changes one number a two-line diff
// with a key the user has to learn.
type RoleParamsConfig struct {
	// MaxRetries is the number of fallback attempts made on a triggering
	// failure. 0 (or absent) means DefaultRoleMaxRetries; see the section note.
	MaxRetries int `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`
	// TimeoutSeconds is the per-model call deadline in seconds before the
	// chain advances. 0 (or absent) means the provider profile's own.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	// Triggers declares which refusals advance the chain.
	Triggers RoleTriggerConfig `yaml:"fallback_triggers,omitempty" json:"fallback_triggers,omitempty"`
}

// DefaultRoleParamsConfig returns the documented default parameter block.
func DefaultRoleParamsConfig() RoleParamsConfig {
	return RoleParamsConfig{
		MaxRetries:     DefaultRoleMaxRetries,
		TimeoutSeconds: DefaultRoleTimeoutSeconds,
		Triggers:       DefaultRoleTriggerConfig(),
	}
}

// EffectiveMaxRetries resolves the attempt budget, clamping out-of-range values
// rather than rejecting them: a config a user hand-edited to 9999 should get a
// bounded budget and a visible clamp, not a startup failure.
func (p RoleParamsConfig) EffectiveMaxRetries() int {
	if p.MaxRetries <= 0 {
		return DefaultRoleMaxRetries
	}
	if p.MaxRetries > MaxRoleMaxRetries {
		return MaxRoleMaxRetries
	}
	return p.MaxRetries
}

// EffectiveTimeoutSeconds resolves the per-call deadline, clamping to
// [0, MaxRoleTimeoutSeconds]. 0 is returned unchanged and means "no override".
func (p RoleParamsConfig) EffectiveTimeoutSeconds() int {
	if p.TimeoutSeconds < 0 {
		return 0
	}
	if p.TimeoutSeconds > MaxRoleTimeoutSeconds {
		return MaxRoleTimeoutSeconds
	}
	return p.TimeoutSeconds
}

// EffectiveTriggers resolves every trigger to a definite bool, substituting the
// documented default for each one the config leaves nil.
func (p RoleParamsConfig) EffectiveTriggers() (rateLimit, serverError, contextLength bool) {
	def := DefaultRoleTriggerConfig()
	rateLimit = resolveTrigger(p.Triggers.RateLimit, *def.RateLimit)
	serverError = resolveTrigger(p.Triggers.ServerError, *def.ServerError)
	contextLength = resolveTrigger(p.Triggers.ContextLength, *def.ContextLength)
	return rateLimit, serverError, contextLength
}

// IsZero reports whether the block carries no explicit information at all. It
// is what lets a role entry be deleted rather than left behind as a husk
// declaring only defaults.
func (p RoleParamsConfig) IsZero() bool {
	return p.MaxRetries == 0 && p.TimeoutSeconds == 0 && p.IsTriggersUnset()
}

// IsTriggersUnset reports whether every trigger is nil.
func (p RoleParamsConfig) IsTriggersUnset() bool {
	return p.Triggers.RateLimit == nil && p.Triggers.ServerError == nil && p.Triggers.ContextLength == nil
}

// Explicit returns the block with every field stated: the budget and deadline
// written as their resolved values and every trigger written as its resolved
// bool. A user who opens the editor and presses Enter on an untouched role gets
// this, so what lands in the file is what the editor showed them.
func (p RoleParamsConfig) Explicit() RoleParamsConfig {
	rate, server, ctx := p.EffectiveTriggers()
	return RoleParamsConfig{
		MaxRetries:     p.EffectiveMaxRetries(),
		TimeoutSeconds: p.EffectiveTimeoutSeconds(),
		Triggers: RoleTriggerConfig{
			RateLimit:     &rate,
			ServerError:   &server,
			ContextLength: &ctx,
		},
	}
}

func resolveTrigger(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

func boolPtr(v bool) *bool { return &v }

// ── THE ROLE TREE, AS ONE REPLACEMENT ─────────────────────────────────────────

// RoleTreeUpdate is the COMPLETE post-edit state of one role's tree node: its
// primary model, its ordered fallback chain, and its operational parameters.
//
// It is a replacement rather than a delta for the same reason
// SetRoleFallbackChain is one: "drop hop 2 of 3" and "make the 5xx trigger off
// while leaving max_retries alone" are not expressible as a delta, and a caller
// that tried would be reconstructing the tree from a message that is missing
// most of it.
type RoleTreeUpdate struct {
	// Role is the config key the update applies to.
	Role string
	// Primary is the role's declared primary model, "" for "inherit the active
	// model".
	Primary string
	// Chain is the ordered fallback chain, which replaces the stored one
	// wholesale. An empty slice clears the chain.
	Chain []string
	// Params is the role's operational parameter block, which replaces the
	// stored one wholesale.
	Params RoleParamsConfig
}

// ApplyRoleTree writes u into cfg and persists cfg to ~/.izen/config.yml in ONE
// atomic write, so the three halves of a tree node — primary, chain,
// parameters — are never observable apart from each other.
//
// # ORDER: SERIALIZE, MUTATE, WRITE, ROLL BACK
//
// The sequence is deliberate and each step exists because of the failure of the
// one before it:
//
//  1. Serialize FIRST. yaml.Marshal runs against a scratch copy, before cfg is
//     touched at all. A struct that cannot be marshalled is a programming
//     error, and discovering it here means memory never held a value the file
//     cannot express — so there is nothing to roll back in the common case.
//  2. Mutate cfg, keeping a restore closure built from the PRE-edit entry.
//     Reading the entry back after mutating it would restore the edit that
//     just failed to persist, which is a rollback to the wrong thing.
//  3. Write atomically (temp file, fsync, rename), so a reader — including the
//     next process — sees the whole new config or the whole old one.
//  4. On a write failure, restore cfg and return the error. The in-memory
//     config must still describe what is on disk, because m.cfg is what every
//     config-derived reader in this process believes. A chain that fires once,
//     mysteriously, and is not in the file is the worst outcome available.
func ApplyRoleTree(cfg *Config, u RoleTreeUpdate) error {
	if cfg == nil {
		return fmt.Errorf("cannot apply role tree to nil config")
	}
	roleKey := strings.TrimSpace(u.Role)
	if roleKey == "" {
		return fmt.Errorf("role key required")
	}
	// Serialize against a scratch copy so a marshal failure is detected with
	// cfg still pristine.
	probe := *cfg
	probe.Roles = cloneRoleMap(cfg.Roles)
	applyRoleTreeEntry(&probe, u)
	if _, err := yaml.Marshal(&probe); err != nil {
		return fmt.Errorf("serialize role tree for %q: %w", roleKey, err)
	}

	restore := applyRoleTreeEntry(cfg, u)
	if err := SaveAtomic(cfg); err != nil {
		restore()
		return err
	}
	return nil
}

// applyRoleTreeEntry mutates cfg's entry for u.Role and returns a closure that
// restores the exact prior state (including the absence of the entry).
func applyRoleTreeEntry(cfg *Config, u RoleTreeUpdate) (restore func()) {
	key := strings.TrimSpace(u.Role)
	previous, existed := cfg.Roles[key]
	restore = func() {
		if existed {
			if cfg.Roles == nil {
				cfg.Roles = make(map[string]RoleFallbackConfig)
			}
			cfg.Roles[key] = previous
			return
		}
		delete(cfg.Roles, key)
	}

	chain := normalizeChain(u.Chain)
	primary := strings.TrimSpace(u.Primary)
	if primary == "" && len(chain) == 0 && u.Params.IsZero() {
		// Nothing to say about this role. Leaving `roles: {plan: {}}` behind
		// would be read by SetRoleFallbackChain's own husk rule as a
		// configured role, and by RoleChainFor as a role that claims turns it
		// has no answer for.
		if cfg.Roles != nil {
			delete(cfg.Roles, key)
		}
		return restore
	}
	if cfg.Roles == nil {
		cfg.Roles = make(map[string]RoleFallbackConfig)
	}
	entry := cfg.Roles[key]
	entry.Model = primary
	if len(chain) == 0 {
		entry.Fallback = ""
		entry.Fallbacks = nil
	} else {
		entry.Fallback = chain[0]
		entry.Fallbacks = append([]string(nil), chain...)
	}
	entry.Params = u.Params
	cfg.Roles[key] = entry
	return restore
}

// normalizeChain is the defensive copy on the way in: trimmed, non-empty, and
// de-duplicated case-insensitively with the first occurrence winning.
func normalizeChain(chain []string) []string {
	if len(chain) == 0 {
		return nil
	}
	out := make([]string, 0, len(chain))
	seen := make(map[string]struct{}, len(chain))
	for _, v := range chain {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		k := strings.ToLower(v)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneRoleMap(in map[string]RoleFallbackConfig) map[string]RoleFallbackConfig {
	if in == nil {
		return nil
	}
	out := make(map[string]RoleFallbackConfig, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SaveAtomic persists cfg to ~/.izen/config.yml through a temporary file in the
// same directory, an fsync, and a rename(2): a reader of the destination sees
// the whole old config or the whole new one, never a half-written file.
//
// It serializes BEFORE creating anything, so an unmarshalable config cannot
// leave a temporary file behind on a path that has nothing wrong with it.
//
// # WHY THE SYMLINK IS RESOLVED
//
// ~/.izen/config.yml is a symlink for a real and common reason: dotfile
// managers link it into a repository. os.Rename replaces the NAME, so an
// unresolved path would quietly convert a user's symlink into an ordinary file
// in ~/.izen while the dotfiles repo kept an old copy — a divergence the user
// discovers at the next reboot. Resolving first means the rename lands on the
// file the symlink actually points at, and the link survives.
func SaveAtomic(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("cannot save nil config")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	path := configPath()
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil && resolved != "" {
		path = resolved
	}
	return atomicio.WriteFileAtomic(path, data, 0o644)
}

// ── TREE-LEVEL READ/WRITE HELPERS ─────────────────────────────────────────────
//
// # WHERE CHAIN MUTATION LIVES, AND WHY IT IS NOT HERE
//
// There is deliberately no ReorderRoleFallback / RemoveRoleFallbackAt /
// AppendRoleFallback on Config. The chain is edited in the Model Registry's
// ROLES tree, which STAGES every change in the widget and sends the complete
// post-edit chain up on the confirming Enter — so by the time anything reaches
// this package, the order is already decided, and a second implementation of
// "move hop 0 to position 2" here would be a second place for the two to
// disagree about what a delete does to the rows that follow it.
//
// This file owns the two things the widget genuinely cannot: the on-disk SHAPE
// of a role node, and the ATOMIC write that lands all three halves of it.

// RoleParamsFor returns a role's stored parameter block, or the zero block when
// the role is unconfigured (so Effective* still answers with the defaults).
func (c *Config) RoleParamsFor(role string) RoleParamsConfig {
	if c == nil {
		return RoleParamsConfig{}
	}
	entry, ok := c.Roles[strings.TrimSpace(role)]
	if !ok {
		return RoleParamsConfig{}
	}
	return entry.Params
}

// SetRoleParams writes a role's parameter block in memory. Persistence is the
// CALLER's job (ApplyRoleTree), for the same staging reason every other config
// mutation on this surface has.
func (c *Config) SetRoleParams(role string, p RoleParamsConfig) {
	if c == nil {
		return
	}
	key := strings.TrimSpace(role)
	if key == "" {
		return
	}
	if p.IsZero() {
		if c.Roles != nil {
			if entry, ok := c.Roles[key]; ok {
				entry.Params = RoleParamsConfig{}
				c.Roles[key] = entry
			}
		}
		return
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleFallbackConfig)
	}
	entry := c.Roles[key]
	entry.Params = p
	c.Roles[key] = entry
}

// SetRolePrimary writes a role's declared primary model in memory. An empty
// model means "inherit whatever model is active" and is stored as an absent
// field rather than an empty string, so the YAML does not grow a
// `model: ""` line that reads as a configuration pointing at nothing.
func (c *Config) SetRolePrimary(role, model string) {
	if c == nil {
		return
	}
	key := strings.TrimSpace(role)
	if key == "" {
		return
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleFallbackConfig)
	}
	entry := c.Roles[key]
	entry.Model = strings.TrimSpace(model)
	c.Roles[key] = entry
}
