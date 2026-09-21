package continuation

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// DeriveNextStep is the pure proposal function (§6). It consumes only
// truthful state, inspects the previous outcome and available evidence, and
// derives at most one bounded next-step proposal while preserving intent,
// scope, and capability constraints. It never executes, writes, authorizes,
// schedules, creates grants, expands scope, or switches execution authority.
func DeriveNextStep(in DerivationInput) ContinuationDecision {
	// State freshness (§11) — stale/OCC drift prevents unsafe continuation.
	if in.HasStaleState {
		return ContinuationDecision{
			Action:      ActionStale,
			Reason:      "state fingerprint drift: recovery context requires re-admission",
			StateDigest: digestInput(in),
		}
	}
	if drift := detectOCCDrift(in); drift != "" {
		return ContinuationDecision{
			Action:      ActionStale,
			Reason:      drift,
			StateDigest: in.Task.StateFingerprint,
		}
	}

	// No-progress protection (§12) — repeated identical state.
	if noProgress, reason := detectNoProgress(in); noProgress {
		return ContinuationDecision{
			Action:      ActionNoProgress,
			Reason:      reason,
			StateDigest: in.Task.StateFingerprint,
		}
	}

	// Partial model output (§7, §9) — finish_reason=length is NOT task failure.
	// It yields CONTINUE with a bounded next step derived from durable state,
	// never a blind retry of the same prompt.
	if in.IsPartialOutput || strings.EqualFold(in.PreviousOutcome, "partial") {
		// Partial is authoritative truncation; do not treat the task as failed.
		if targets, rationale := nextTargetsFromDurableState(in); len(targets) > 0 {
			if !withinScope(targets, in.AllowedScope) {
				return ContinuationDecision{
					Action:      ActionAwaitingApproval,
					Reason:      "next bounded step exceeds pre-approved scope envelope",
					StateDigest: in.Task.StateFingerprint,
				}
			}
			return ContinuationDecision{
				Action:   ActionContinue,
				Reason:   "partial output: deriving next bounded step from durable evidence",
				Evidence: filterAuthoritative(in.Observations),
				NextStep: &StepProposal{
					ID:              nextStepID(in),
					Kind:            nextKind(in),
					Targets:         targets,
					Rationale:       rationale,
					EvidenceRefs:    evidenceRefs(in.Observations),
					EstimatedTokens: boundedEstimate(targets, in.Task.ProviderCeiling),
					StateDigest:     in.Task.StateFingerprint,
				},
				StateDigest: in.Task.StateFingerprint,
			}
		}
		// No remaining targets but we still have a partial — treat as blocked
		// rather than failed: the bounded step made progress but needs re-bundling.
		return ContinuationDecision{
			Action:      ActionBlocked,
			Reason:      "partial output with no remaining evidence-backed targets",
			StateDigest: in.Task.StateFingerprint,
		}
	}

	// Observation-driven truth (§10, §14) — authoritative state is
	// execution evidence / verification, not ModelProposal claims.
	authoritative := hasAuthoritativeEvidence(in.Observations, in.Verified)
	modelOnlyClaim := hasOnlyModelProposal(in.Observations)

	if modelOnlyClaim && !authoritative {
		// Model claimed completion without any verification or observation:
		// do not transition state. Request a verification/observation step.
		return ContinuationDecision{
			Action:   ActionContinue,
			Reason:   "model claim without authoritative verification: requesting bounded verification/observation",
			Evidence: filterAuthoritative(in.Observations),
			NextStep: &StepProposal{
				ID:          nextStepID(in),
				Kind:        "VERIFY",
				Targets:     withinScopeOrEmpty(in.Task.ActiveScope, in.AllowedScope),
				Rationale:   "verification required before state transition",
				StateDigest: in.Task.StateFingerprint,
			},
			StateDigest: in.Task.StateFingerprint,
		}
	}

	// Task completion: all plan steps observed as verified, or previous
	// outcome is complete and verification passed with no pending work.
	if isTaskComplete(in) {
		return ContinuationDecision{
			Action:      ActionComplete,
			Reason:      "all bounded steps verified; no unresolved work remains",
			Evidence:    filterAuthoritative(in.Observations),
			StateDigest: in.Task.StateFingerprint,
		}
	}

	// Failure without recovery path
	if strings.EqualFold(in.PreviousOutcome, "failed") && !canRecover(in) {
		return ContinuationDecision{
			Action:      ActionFailed,
			Reason:      "bounded step failed without recoverable evidence",
			Evidence:    filterAuthoritative(in.Observations),
			StateDigest: in.Task.StateFingerprint,
		}
	}

	// Derive next bounded step from durable plan + evidence (§8)
	targets, rationale := nextTargetsFromDurableState(in)
	if len(targets) == 0 {
		// No evidence-backed next step determinable — blocked, not silently invented.
		return ContinuationDecision{
			Action:      ActionBlocked,
			Reason:      "no evidence-backed next step proposal determinable from durable state",
			Evidence:    filterAuthoritative(in.Observations),
			StateDigest: in.Task.StateFingerprint,
		}
	}
	if !withinScope(targets, in.AllowedScope) {
		return ContinuationDecision{
			Action:      ActionAwaitingApproval,
			Reason:      "next bounded step exceeds pre-approved scope envelope",
			StateDigest: in.Task.StateFingerprint,
		}
	}

	return ContinuationDecision{
		Action:   ActionContinue,
		Reason:   "unresolved work remains: deriving next bounded step from evidence",
		Evidence: filterAuthoritative(in.Observations),
		NextStep: &StepProposal{
			ID:              nextStepID(in),
			Kind:            nextKind(in),
			Targets:         targets,
			Rationale:       rationale,
			EvidenceRefs:    evidenceRefs(in.Observations),
			EstimatedTokens: boundedEstimate(targets, in.Task.ProviderCeiling),
			StateDigest:     in.Task.StateFingerprint,
		},
		StateDigest: in.Task.StateFingerprint,
	}
}

func isTaskComplete(in DerivationInput) bool {
	if len(in.Task.PendingSteps) == 0 && len(in.PlanSteps) > 0 {
		// All plan steps have been marked completed via durable history
		completed := map[string]bool{}
		for _, id := range in.Task.CompletedSteps {
			completed[id] = true
		}
		allDone := true
		for _, s := range in.PlanSteps {
			if !completed[s.ID] {
				allDone = false
				break
			}
		}
		if allDone && in.Verified {
			return true
		}
	}
	// Previous outcome complete + verification passed + no remaining targets
	if strings.EqualFold(in.PreviousOutcome, "complete") && in.Verified {
		if len(in.Task.PendingSteps) == 0 {
			return true
		}
	}
	return false
}

func canRecover(in DerivationInput) bool {
	// Recoverable if there is at least one authoritative observation that
	// names a concrete, evidence-backed target for the next attempt.
	for _, o := range in.Observations {
		if o.Kind == KindVerification || o.Kind == KindObservation || o.Kind == KindExecutionResult {
			if o.Detail != "" || o.Subject != "" {
				return true
			}
		}
	}
	return false
}

func nextTargetsFromDurableState(in DerivationInput) ([]string, string) {
	// Prefer evidence-backed plan steps that are not yet completed.
	completed := map[string]bool{}
	for _, id := range in.Task.CompletedSteps {
		completed[id] = true
	}
	for _, s := range in.PlanSteps {
		if completed[s.ID] {
			continue
		}
		if len(s.References) > 0 {
			refs := append([]string(nil), s.References...)
			// Return only a bounded slice (1-2 targets) so each step is bounded.
			if len(refs) > 2 {
				refs = refs[:2]
			}
			return refs, s.Rationale
		}
	}
	// Fallback: use verified observation subjects not yet completed
	seen := map[string]bool{}
	for _, id := range in.Task.CompletedSteps {
		seen[id] = true
	}
	for _, o := range in.Observations {
		if o.Kind == KindObservation || o.Kind == KindVerification || o.Kind == KindExecutionResult {
			if o.Subject != "" && !seen[o.Subject] {
				// evidence-backed subject
				if len(in.AllowedScope) > 0 && !withinScope([]string{o.Subject}, in.AllowedScope) {
					continue
				}
				return []string{o.Subject}, "evidence-backed continuation target"
			}
		}
	}
	// Last resort: first active scope item not yet completed
	for _, t := range in.Task.ActiveScope {
		if !seen[t] {
			if len(in.AllowedScope) > 0 && !withinScope([]string{t}, in.AllowedScope) {
				continue
			}
			return []string{t}, "remaining scope item"
		}
	}
	return nil, ""
}

func nextKind(in DerivationInput) string {
	// Derive from next plan step kind if available.
	completed := map[string]bool{}
	for _, id := range in.Task.CompletedSteps {
		completed[id] = true
	}
	for _, s := range in.PlanSteps {
		if !completed[s.ID] {
			return s.Kind
		}
	}
	// Fallback kind mapping from previous outcome
	switch strings.ToLower(in.PreviousOutcome) {
	case "partial":
		return "MUTATE"
	default:
		return "INVESTIGATE"
	}
}

func nextStepID(in DerivationInput) string {
	n := len(in.Task.CompletedSteps) + 1
	return fmt.Sprintf("step-%02d", n)
}

func boundedEstimate(targets []string, providerCeiling int) int {
	// Estimate is bounded per step, independent of provider task ceiling.
	// Never returns > providerCeiling when ceiling is set.
	base := 180 * len(targets)
	if base == 0 {
		base = 200
	}
	if providerCeiling > 0 && base > providerCeiling {
		return providerCeiling
	}
	return base
}

func detectOCCDrift(in DerivationInput) string {
	// Recovery reason carrying OCC / fingerprint drift is treated as staleness.
	if strings.Contains(strings.ToLower(in.Task.RecoveryReason), "fingerprint") ||
		strings.Contains(strings.ToLower(in.Task.RecoveryReason), "digest") ||
		strings.Contains(strings.ToLower(in.Task.RecoveryReason), "stale") ||
		strings.Contains(strings.ToLower(in.Task.RecoveryReason), "occ") ||
		strings.Contains(strings.ToLower(in.Task.RecoveryReason), "drift") {
		return "OCC drift: " + in.Task.RecoveryReason
	}
	return ""
}

func detectNoProgress(in DerivationInput) (bool, string) {
	h := in.Task.History
	if len(h) < 3 {
		return false, ""
	}
	// Look at last 3 entries: same fingerprint + same outcome + same evidence + zero patches
	tail := h[len(h)-3:]
	first := tail[0]
	sameFingerprint := true
	sameOutcome := true
	sameEvidence := true
	allZeroPatches := true
	for _, e := range tail {
		if e.StateFingerprint != first.StateFingerprint {
			sameFingerprint = false
		}
		if e.Outcome != first.Outcome {
			sameOutcome = false
		}
		if e.EvidenceDigest != first.EvidenceDigest {
			sameEvidence = false
		}
		if e.Patches != 0 {
			allZeroPatches = false
		}
	}
	if sameFingerprint && sameOutcome && sameEvidence && allZeroPatches {
		return true, fmt.Sprintf("no progress: repeated fingerprint %s with outcome %s and no workspace change", first.StateFingerprint, first.Outcome)
	}
	// Also detect exact step repetition without advancement
	if len(tail) == 3 && tail[0].StepID == tail[1].StepID && tail[1].StepID == tail[2].StepID {
		if tail[0].Outcome == tail[1].Outcome && tail[1].Outcome == tail[2].Outcome && allZeroPatches {
			return true, fmt.Sprintf("no progress: step %s repeated with identical outcome and no change", first.StepID)
		}
	}
	return false, ""
}

func hasAuthoritativeEvidence(obs []Observation, verified bool) bool {
	for _, o := range obs {
		if o.Kind == KindVerification || o.Kind == KindObservation || o.Kind == KindExecutionResult || o.Kind == KindStateTransition {
			return true
		}
	}
	return verified
}

func hasOnlyModelProposal(obs []Observation) bool {
	if len(obs) == 0 {
		return false
	}
	for _, o := range obs {
		if o.Kind != KindModelProposal {
			return false
		}
	}
	return true
}

func filterAuthoritative(obs []Observation) []Observation {
	var out []Observation
	for _, o := range obs {
		if o.Kind == KindVerification || o.Kind == KindObservation || o.Kind == KindExecutionResult || o.Kind == KindStateTransition {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

func evidenceRefs(obs []Observation) []string {
	refs := []string{}
	seen := map[string]bool{}
	for _, o := range obs {
		if o.EvidenceDigest != "" && !seen[o.EvidenceDigest] {
			seen[o.EvidenceDigest] = true
			refs = append(refs, o.EvidenceDigest)
		}
	}
	sort.Strings(refs)
	return refs
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
		if !allowedSet[strings.TrimSpace(t)] {
			// prefix check: allow subpaths of allowed dirs
			ok := false
			for _, a := range allowed {
				if strings.HasPrefix(t, strings.TrimSuffix(a, "/")+"/") {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
	}
	return true
}

func withinScopeOrEmpty(targets, allowed []string) []string {
	if len(allowed) == 0 {
		return append([]string(nil), targets...)
	}
	var out []string
	for _, t := range targets {
		if withinScope([]string{t}, allowed) {
			out = append(out, t)
		}
	}
	return out
}

func digestInput(in DerivationInput) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%v", in.Task.TaskID, in.Task.StateFingerprint, in.PreviousOutcome, in.PreviousReason, in.Verified)
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// Lifecycle helpers (§4) — bounded step transitions.
// These are pure state-machine helpers; they do not schedule or execute.

// Transition maps current lifecycle + outcome to next lifecycle state.
func Transition(current BoundedStepState, outcome string, verified bool) BoundedStepState {
	switch current {
	case StateProposed:
		return StateAdmitted
	case StateAdmitted:
		return StateExecuting
	case StateExecuting:
		return StateObserved
	case StateObserved:
		switch strings.ToLower(outcome) {
		case "complete":
			if verified {
				return StateVerified
			}
			return StateObserved
		case "partial":
			return StatePartial
		case "failed":
			return StateFailed
		case "blocked":
			return StateBlocked
		default:
			return StateObserved
		}
	default:
		return StateTerminate
	}
}
