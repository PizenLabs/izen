package provider

// Budget bound constants for the Capability & Budget Engine.
//
// These are sane DEFAULTS for fallback paths only — they are not
// architectural absolute limits. Invariant 3 requires that opaque-reasoning
// margins stay caller-supplied; the scheduler always passes its margin
// explicitly to EffectiveStepBudget.
const (
	// ConstrainedOutputThreshold mirrors the provider-level constrained
	// ceiling: a model advertising at or below this output cap is treated
	// as constrained (free-tier style).
	ConstrainedOutputThreshold = 1024
	// MinStepBudget is the floor for a derived step budget when at least one
	// positive bound exists. It keeps a degenerate (e.g. margin >= budget)
	// derivation schedulable instead of zero.
	MinStepBudget = 128
	// DefaultRequestedStepBudget is the fallback when no positive bound
	// exists at all (all operands unbounded/unknown).
	DefaultRequestedStepBudget = 2048
)
