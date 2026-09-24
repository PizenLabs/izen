// Package registry eligibility: ModelEligibility sits between catalog
// discovery (ModelDescriptor) and selection/execution. A model that appears
// in a provider catalog is DISCOVERED; only a model compatible with Izen's
// actual execution path is ELIGIBLE (selectable + executable).
//
// OpenRouter's GET /api/v1/models is a discoverability catalog: it lists
// models without guaranteeing that an arbitrary API-key client may invoke
// them. Some models are restricted to agentic harnesses (OpenRouter's Ori
// harness program) and answer ordinary POST /chat/completions calls with
// HTTP 403 ("... is only available on agentic harnesses"). The catalog
// schema exposes no machine-readable flag for this restriction (verified
// against the live API: id, canonical_slug, name, description,
// context_length, architecture, pricing, top_provider, per_request_limits,
// supported_parameters, reasoning, benchmarks, links — none carries
// harness eligibility), so proactive eligibility is an evidence-seeded
// provider-compatibility policy, not inferred metadata. Entries are added
// only with observed provider evidence (inference 403 or provider docs)
// recorded in the Reason/Evidence fields. Matching is exact on the
// normalized provider + model ID: never substring or family-prefix matching,
// so a seeded entry can never over-block sibling models.
package registry

import (
	"strings"
)

// EligibilityReason classifies why a discovered model is not executable
// through Izen's current provider execution path.
type EligibilityReason string

const (
	// ReasonAgenticHarnessOnly marks models the provider restricts to
	// agentic-harness traffic. An ordinary API-key client receives
	// HTTP 403 at inference time.
	ReasonAgenticHarnessOnly EligibilityReason = "agentic-harness-only"
)

// ModelIneligibility describes why a discovered model must not be selected
// or executed through the normal path.
type ModelIneligibility struct {
	Reason   EligibilityReason
	Detail   string
	Evidence string
}

// incompatibleModel is one evidence-seeded policy entry. Both Provider and
// ID match exactly after normalization (trim + lowercase); empty Provider
// matches any provider (reserved for cross-provider restrictions).
type incompatibleModel struct {
	Provider string
	ID       string
	Reason   EligibilityReason
	Detail   string
	Evidence string
}

// incompatibleModels is the evidence-seeded provider-compatibility policy.
// OpenRouter provides no machine-readable harness-eligibility signal in its
// catalog, so entries here require observed provider evidence (documented
// in Evidence) and exact-ID matching.
var incompatibleModels = []incompatibleModel{
	{
		Provider: "openrouter",
		ID:       "thinkingmachines/inkling:free",
		Reason:   ReasonAgenticHarnessOnly,
		Detail:   "Model unavailable for Izen's current OpenRouter execution path: restricted to agentic harnesses.",
		Evidence: "Observed HTTP 403 from POST /chat/completions: 'thinkingmachines/inkling:free is only available on agentic harnesses.' Catalog entry carries no harness-eligibility flag.",
	},
}

// normalizeModelKey trims and lowercases a provider/model identifier for
// exact policy comparison.
func normalizeModelKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// CheckExecutable reports whether provider/modelID may be selected and
// executed through Izen's current execution path. It returns nil when the
// model is eligible. Discovered-but-ineligible models yield a
// *ModelIneligibility describing the provider restriction.
func CheckExecutable(provider, modelID string) *ModelIneligibility {
	p := normalizeModelKey(provider)
	id := normalizeModelKey(modelID)
	if p == "" || id == "" {
		return nil
	}
	for _, e := range incompatibleModels {
		if e.Provider != "" && normalizeModelKey(e.Provider) != p {
			continue
		}
		if normalizeModelKey(e.ID) != id {
			continue
		}
		return &ModelIneligibility{
			Reason:   e.Reason,
			Detail:   e.Detail,
			Evidence: e.Evidence,
		}
	}
	return nil
}

// Executable reports whether a descriptor may be selected and executed.
// A descriptor with an explicit IneligibleReason is never executable;
// otherwise the policy table is consulted so hand-built descriptors are
// covered too.
func (m ModelDescriptor) Executable() bool {
	if strings.TrimSpace(m.IneligibleReason) != "" {
		return false
	}
	return CheckExecutable(m.Provider, m.ID) == nil
}

// ExecutableModels filters a catalog slice down to the executable subset,
// preserving order. The input slice is never mutated.
func ExecutableModels(models []Model) []Model {
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if m.Executable() {
			out = append(out, m)
		}
	}
	return out
}
