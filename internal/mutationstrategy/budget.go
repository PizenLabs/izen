package mutationstrategy

// StepBudget is the minimal planning constraint for one bounded step.
// It is NOT authorization. It constrains how much model work may be
// allocated to the step description (output, files, mutation units).
// A planner cannot enlarge a human grant by increasing this budget,
// and a model cannot enlarge a grant by requesting more budget.
// A budget exceeding the authorized envelope must be rejected
// downstream (I1: StepBudget ≠ Grant).
type StepBudget struct {
	// MaxOutputTokens is the per-step model-output planning ceiling.
	// Zero means unbounded for planning (the execution preflight still
	// gates at the boundary when a real ceiling is known).
	MaxOutputTokens int `json:"max_output_tokens"`
	// MaxFiles is the per-step file-count planning ceiling. Zero means
	// unbounded for planning.
	MaxFiles int `json:"max_files"`
	// MaxMutationUnits is an opaque planning ceiling for internal
	// structural units (reserved for later adaptive execution phases).
	MaxMutationUnits int `json:"max_mutation_units,omitempty"`
}

// Valid reports whether the budget is well-formed (non-negative).
func (b StepBudget) Valid() bool {
	return b.MaxOutputTokens >= 0 && b.MaxFiles >= 0 && b.MaxMutationUnits >= 0
}

// Fits reports whether the estimate fits within this budget. Zero
// budgets are unbounded (always fits). The check is intentionally
// on Expected, not Upper, so planning is not spuriously pessimistic;
// TOO_LARGE is raised when Expected exceeds the envelope.
func (b StepBudget) Fits(est EstimatedMutationSize) bool {
	if b.MaxOutputTokens <= 0 {
		return true
	}
	return est.Expected <= b.MaxOutputTokens
}

// ModelConstraints carries already-known provider/model capability
// metadata that MAY inform planning — never adaptive selection. It
// answers only "can this proposed step reasonably fit the known
// envelope?" If the step exceeds the envelope, the plan reports
// TOO_LARGE; it never silently switches models, increases limits,
// or splits and executes.
type ModelConstraints struct {
	// OutputCeiling is the known model output ceiling in structural
	// units (e.g. max_output_tokens as mapped into the planning
	// signal). Zero means unknown/unbounded for planning.
	OutputCeiling int `json:"output_ceiling"`
	// ContextWindow is the model context window (not used for strict
	// planning here, but carried for evidence completeness).
	ContextWindow int `json:"context_window,omitempty"`
	// CapabilityProfile is an opaque label for the provider/model
	// profile (e.g. "small", "large"). It never drives strategy
	// selection beyond the ceiling check.
	CapabilityProfile string `json:"capability_profile,omitempty"`
}

// StepBudgetFor derives the per-step budget from model constraints
// and the fallback default. It is the only place mutationstrategy
// maps capability metadata onto a StepBudget — no provider retry,
// no adaptive switching.
func StepBudgetFor(mc ModelConstraints, fallback StepBudget) StepBudget {
	if mc.OutputCeiling > 0 {
		// Reserve a small planning margin so expected==ceiling is not
		// already considered feasible when reasoning/context overhead
		// would push it over. The margin is fixed and documented, not
		// a provider magic number.
		const planningMargin = 128
		ceiling := mc.OutputCeiling - planningMargin
		if ceiling < 64 {
			ceiling = 64
		}
		fallback.MaxOutputTokens = ceiling
	}
	return fallback
}

// Envelope describes the model-output envelope requirements for one
// step's proposal. It is stable textual contract metadata, not a
// budget expansion device.
type Envelope struct {
	// MaxOutputTokens is the per-step proposal envelope the plan
	// expects the model to honor (matches StepBudget.MaxOutputTokens
	// when a provider ceiling is known, otherwise the fallback).
	MaxOutputTokens int `json:"max_output_tokens"`
	// RequiresBoundedPatch signals that the step's proposal MUST be a
	// bounded patch (SEARCH/REPLACE window) rather than a full rewrite,
	// so downstream execution can gate correctly at B2/B4.
	RequiresBoundedPatch bool `json:"requires_bounded_patch"`
	// MaxFiles is the per-step file count envelope.
	MaxFiles int `json:"max_files"`
}
