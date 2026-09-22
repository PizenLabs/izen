package stepadmission

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/mutationstrategy"
)

// AdmitStep is the pure admission function (spec §10).
//
//	candidate + capability + budget + scope + state → decision
//
// It is pure: no execution, no scheduling, no authorization mutation,
// no filesystem I/O, no model invocation. It answers only: "Is this
// candidate step appropriately bounded?"
func AdmitStep(
	candidate CandidateStep,
	capability ModelCapabilityProfile,
	budget mutationstrategy.StepBudget,
	allowedScope []string,
	state AdmissionState,
) StepAdmissionDecision {
	// Derive effective budget from capability (provider ceiling bounds the
	// current step, not the task). Falls back to supplied budget when
	// capability is unknown.
	effective := EffectiveBudget(capability, budget)

	// Use existing estimate if valid, otherwise compute via estimator.
	est := candidate.Estimate
	if !est.Valid() {
		est = EstimateForCandidate(candidate)
	}

	// Evidence carried forward for the decision.
	ev := append([]string(nil), candidate.EvidenceRefs...)
	sort.Strings(ev)

	copied := candidate
	copied.Estimate = est

	decision := StepAdmissionDecision{
		Estimate: est,
		Budget:   effective,
		Step:     &copied,
		Evidence: ev,
	}

	// State freshness (§25) — stale digest prevents unsafe admission.
	if state.IsStale {
		decision.Action = ActionStale
		decision.Reason = "state fingerprint drift: admission requires fresh state"
		return decision
	}
	if candidate.StateDigest != "" && state.CurrentFingerprint != "" && candidate.StateDigest != state.CurrentFingerprint {
		decision.Action = ActionStale
		decision.Reason = fmt.Sprintf("state fingerprint mismatch: candidate %q vs current %q", candidate.StateDigest, state.CurrentFingerprint)
		return decision
	}

	// Basic validity.
	if !candidate.Valid() {
		decision.Action = ActionBlock
		decision.Reason = "candidate step is not well-formed"
		return decision
	}

	// Scope validity — refinement must not expand scope (§13).
	// If candidate targets exceed allowed envelope, signal awaiting approval
	// rather than silently expanding it (§23).
	if len(allowedScope) > 0 && len(candidate.Targets) > 0 {
		if !withinScope(candidate.Targets, allowedScope) {
			decision.Action = ActionAwaitingApproval
			decision.Reason = "candidate targets exceed pre-approved scope envelope"
			return decision
		}
	}

	// Capability availability — zero ceilings are treated as unbounded for
	// planning (same as mutationstrategy); a missing capability does not
	// by itself block admission unless the estimate genuinely exceeds a
	// known ceiling.
	if effective.MaxOutputTokens <= 0 {
		decision.Action = ActionAdmit
		decision.Reason = "admitted: no bounded ceiling constrains this step"
		return decision
	}

	// Budget comparison — the core admission check (§11).
	if est.Expected <= effective.MaxOutputTokens {
		decision.Action = ActionAdmit
		decision.Reason = fmt.Sprintf("admitted: expected %d ≤ budget %d", est.Expected, effective.MaxOutputTokens)
		return decision
	}

	// Too large — attempt single refinement (§12, §15 single-step refinement).
	if candidate.RefinementDepth >= MaxRefinementDepth {
		decision.Action = ActionBlock
		decision.Reason = fmt.Sprintf("blocked: refinement depth %d exhausted; expected %d > budget %d", candidate.RefinementDepth, est.Expected, effective.MaxOutputTokens)
		return decision
	}

	refined := RefineCandidate(candidate, capability, effective)
	if refined == nil {
		decision.Action = ActionBlock
		decision.Reason = fmt.Sprintf("blocked: cannot refine further; expected %d > budget %d and no smaller evidence-backed subset exists", est.Expected, effective.MaxOutputTokens)
		return decision
	}

	// Verify refined candidate is actually smaller (spec §12 refinement must
	// produce a smaller bounded step).
	refinedEst := refined.Estimate
	if !refinedEst.Valid() {
		refinedEst = EstimateForCandidate(*refined)
		refined.Estimate = refinedEst
	}
	if refinedEst.Expected >= est.Expected {
		decision.Action = ActionBlock
		decision.Reason = fmt.Sprintf("blocked: refinement did not reduce requirement (%d → %d)", est.Expected, refinedEst.Expected)
		return decision
	}
	_ = refinedEst.Expected > effective.MaxOutputTokens // still return REFINE; next admission will block if still over

	// Scope of refined step must still be within envelope.
	if len(allowedScope) > 0 && !withinScope(refined.Targets, allowedScope) {
		decision.Action = ActionAwaitingApproval
		decision.Reason = "refined candidate exceeds pre-approved scope envelope"
		return decision
	}

	// Intent preservation is guaranteed by RefineCandidate (copies Intent unchanged).
	// Additional check here for defense.
	if candidate.Intent != "" && refined.Intent != candidate.Intent {
		refined.Intent = candidate.Intent
	}

	decision.Action = ActionRefine
	decision.Reason = fmt.Sprintf("refine: expected %d > budget %d; proposing smaller bounded step expected %d", est.Expected, effective.MaxOutputTokens, refinedEst.Expected)
	decision.RefinedStep = refined
	return decision
}

func withinScope(targets, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	allowedSet := map[string]bool{}
	for _, a := range allowed {
		allowedSet[strings.TrimSpace(a)] = true
	}
	for _, t := range targets {
		tt := strings.TrimSpace(t)
		if allowedSet[tt] {
			continue
		}
		ok := false
		for _, a := range allowed {
			prefix := strings.TrimSuffix(strings.TrimSpace(a), "/") + "/"
			if strings.HasPrefix(tt, prefix) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// CandidateFromMutationStep builds a domain-neutral CandidateStep from an
// existing MutationStep, reusing the mutationstrategy semantics (spec §16).
// This is the integration point that proves mutation refinement does not
// create a second mutation decomposition mechanism.
func CandidateFromMutationStep(ms mutationstrategy.MutationStep, intent, stateDigest string, depth int) CandidateStep {
	est := EstimateFromMutationSize(ms.Estimate.Lower, ms.Estimate.Expected, ms.Estimate.Upper, ms.Estimate.Confidence)
	// Map operation kind to problem kind (MUTATE)
	kind := "MUTATE"
	refs := append([]string(nil), ms.SurfaceRefs...)
	evidence := append([]string(nil), ms.Evidence...)
	deps := append([]string(nil), ms.DependsOn...)
	sort.Strings(refs)
	sort.Strings(evidence)
	return CandidateStep{
		ID:              ms.ID,
		Kind:            kind,
		Targets:         refs,
		EvidenceRefs:    evidence,
		DependsOn:       deps,
		Rationale:       ms.Rationale,
		Intent:          intent,
		StateDigest:     stateDigest,
		Estimate:        est,
		RefinementDepth: depth,
	}
}

// CandidateFromProblemStep builds a CandidateStep from a ProblemStep-style
// projection (domain-neutral). Used for INVESTIGATE/ANALYZE/VERIFY/OBSERVE
// steps (spec §17).
func CandidateFromProblemStep(id, kind string, refs, evidence []string, intent, stateDigest string, depth int) CandidateStep {
	rcopy := append([]string(nil), refs...)
	ecopy := append([]string(nil), evidence...)
	sort.Strings(rcopy)
	sort.Strings(ecopy)
	return CandidateStep{
		ID:              id,
		Kind:            kind,
		Targets:         rcopy,
		EvidenceRefs:    ecopy,
		Rationale:       kind + " evidence-backed step",
		Intent:          intent,
		StateDigest:     stateDigest,
		RefinementDepth: depth,
	}
}
