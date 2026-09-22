package stepadmission

import (
	"fmt"
	"sort"

	"github.com/PizenLabs/izen/internal/mutationstrategy"
)

// RefineCandidate produces a smaller bounded candidate step from an
// evidence-backed candidate that exceeded its budget.
//
// Properties (spec §13):
//   - Domain-neutral: no ReactStep/BackendStep/GoStep branching.
//   - Evidence-backed: refined targets are a subset of the original
//     evidence-backed targets; never invented (ADMIT-14).
//   - Scope-preserving: refined targets ⊆ original targets ⊆ allowed scope;
//     never expands scope (ADMIT-05).
//   - Dependency-aware: DependsOn is preserved (subset, never invented).
//   - Finite: RefinementDepth increments and is bounded by MaxRefinementDepth.
//   - Preserves intent: Intent is copied unchanged (ADMIT-04).
//   - Single-step refinement (§15): returns one smallest-sufficient refined
//     candidate, not many micro-steps.
//
// It is pure: no filesystem I/O, no execution, no scheduling, no grant
// or capability creation (ADMIT-07..09).
func RefineCandidate(candidate CandidateStep, capability ModelCapabilityProfile, budget mutationstrategy.StepBudget) *CandidateStep {
	if len(candidate.Targets) == 0 {
		return nil
	}
	// Single-target case that already exceeds budget cannot be split
	// without inventing targets. Signal caller to BLOCK rather than
	// fabricate a synthetic sub-target (ADMIT-14).
	if len(candidate.Targets) == 1 {
		est := candidate.Estimate
		if !est.Valid() {
			est = EstimateForCandidate(candidate)
		}
		if est.Expected > budget.MaxOutputTokens && budget.MaxOutputTokens > 0 {
			// Cannot split a single evidence-backed file further without
			// inventing an unsupported target.
			return nil
		}
		// Single target already fits? Caller wouldn't have called refine,
		// but if they did, no smaller evidence-backed step exists.
		return nil
	}

	// Sort for determinism (evidence-backed ordering).
	sorted := append([]string(nil), candidate.Targets...)
	sort.Strings(sorted)

	// Find the largest prefix/subset that fits the budget with a smallest-
	// sufficient strategy (spec §15): keep as many evidence-backed targets
	// as possible while bringing Expected ≤ budget.
	// We try sizes from len-1 down to 1 and pick the first that fits (largest).
	bestN := 0
	for n := len(sorted) - 1; n >= 1; n-- {
		subset := sorted[:n]
		// For determinism, keep evidence refs aligned proportionally.
		// Refinement must not invent evidence: subset of original evidence
		// that corresponds to the retained targets is used when possible.
		subsetEvidence := evidenceSubsetForTargets(candidate.EvidenceRefs, candidate.Targets, subset)
		probe := CandidateStep{
			Kind:         candidate.Kind,
			Targets:      subset,
			EvidenceRefs: subsetEvidence,
			DependsOn:    subsetDepends(candidate.DependsOn, subset),
		}
		est := EstimateForCandidate(probe)
		if budget.MaxOutputTokens <= 0 || est.Expected <= budget.MaxOutputTokens {
			bestN = n
			break
		}
	}
	if bestN == 0 {
		// No subset fits, but we still must not invent targets. The smallest
		// evidence-backed step is a single file. Return it so the caller can
		// produce a REFINE with a smaller step (even if it still exceeds) and
		// let the next AdmitStep handle blocking. This preserves the
		// "smallest sufficient refinement" intent without micro-stepping into
		// many fragments.
		bestN = 1
	}

	if bestN == 0 || bestN >= len(sorted) {
		return nil
	}

	refinedTargets := sorted[:bestN]
	refinedEvidence := evidenceSubsetForTargets(candidate.EvidenceRefs, candidate.Targets, refinedTargets)
	refinedDeps := subsetDepends(candidate.DependsOn, refinedTargets)

	// Refined estimate must be recomputed from the subset to ensure it is
	// actually smaller (spec §12).
	probeForEstimate := CandidateStep{
		Kind:         candidate.Kind,
		Targets:      refinedTargets,
		EvidenceRefs: refinedEvidence,
		DependsOn:    refinedDeps,
	}
	finalEst := EstimateForCandidate(probeForEstimate)

	refined := CandidateStep{
		ID:              fmt.Sprintf("%s-r%d", candidate.ID, candidate.RefinementDepth+1),
		Kind:            candidate.Kind, // never mutates kind (ADMIT-12,13)
		Targets:         refinedTargets,
		EvidenceRefs:    refinedEvidence,
		DependsOn:       refinedDeps,
		Rationale:       fmt.Sprintf("refined from %s: bounded to %d of %d evidence-backed targets (budget %d)", candidate.ID, bestN, len(sorted), budget.MaxOutputTokens),
		Intent:          candidate.Intent, // preserve task intent (§14)
		StateDigest:     candidate.StateDigest,
		Estimate:        finalEst,
		RefinementDepth: candidate.RefinementDepth + 1,
	}
	return &refined
}

func evidenceSubsetForTargets(allEvidence, allTargets, subset []string) []string {
	if len(allEvidence) == 0 {
		return nil
	}
	// Evidence-backed but not inventing: retain evidence that was already
	// associated with the retained targets proportionally. Since we don't
	// have a path→evidence index in the generic candidate, retain the first
	// proportional slice deterministically.
	// If evidence count aligns with target count, slice to subset size.
	if len(allEvidence) >= len(allTargets) && len(subset) <= len(allEvidence) {
		ratio := float64(len(allEvidence)) / float64(len(allTargets))
		n := int(float64(len(subset)) * ratio)
		if n < 1 {
			n = 1
		}
		if n > len(allEvidence) {
			n = len(allEvidence)
		}
		out := append([]string(nil), allEvidence[:n]...)
		sort.Strings(out)
		return out
	}
	// Otherwise keep evidence that is still plausible but subset-sized.
	// Return existing evidence truncated to subset size to avoid invention.
	if len(allEvidence) > len(subset) {
		out := append([]string(nil), allEvidence[:len(subset)]...)
		sort.Strings(out)
		return out
	}
	out := append([]string(nil), allEvidence...)
	sort.Strings(out)
	return out
}

func subsetDepends(deps, subset []string) []string {
	if len(deps) == 0 {
		return nil
	}
	// Dependency-aware: preserve existing relationships without inventing new
	// ones. Since DependsOn typically references step IDs, not paths, we
	// preserve the original list verbatim (no path-based dropping).
	_ = subset
	return append([]string(nil), deps...)
}
