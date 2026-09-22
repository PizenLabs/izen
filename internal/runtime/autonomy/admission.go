package autonomy

// PHASE 8 M6 — canonical admission/continuation library reuse.
//
// The Driver is the canonical orchestration owner. It consults the pure
// policy packages below as LIBRARIES (one-way dependency: Driver → pure
// package, never the reverse):
//
//	stepadmission.AdmitStep      — pure bound-check: candidate+capability+
//	                               budget+scope+state → ADMIT|REFINE|BLOCK|
//	                               STALE|AWAITING_APPROVAL
//	continuation.DeriveNextStep  — pure transition function on observation.
//
// Neither package schedules, loops, executes, authorizes, or mutates: they
// own no runtime. Every verdict is interpreted by the Driver recovery matrix
// (recovery.go) or the decomposition gate (stageDecomposition); a verdict
// never re-enters execution on its own. This preserves the canonical shape:
//
//	execution result → classification/evidence → Driver recovery decision
//	→ re-entry into the canonical Driver path.
//
// Dependency direction is pinned by TestDriverPureReuseDirection: the pure
// packages must never import the Driver, the executor, the substrate, or
// the bus.

import (
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/continuation"
	"github.com/PizenLabs/izen/internal/stepadmission"
)

// DriverAdmissionInput carries the Driver-owned facts needed for one pure
// admission bound-check. All fields are values already owned by the Driver;
// nothing is fetched, read, or invoked.
type DriverAdmissionInput struct {
	// Targets is the requested step target set (durable scope subset).
	Targets []string
	// Intent is the objective text, preserved verbatim into the candidate
	// (refinement must never destroy it).
	Intent string
	// StateDigest is the workspace digest the step was derived from
	// (Boundary-5 lineage). Empty disables the freshness comparison.
	StateDigest string
	// CurrentFingerprint is the authoritative current digest. Empty
	// disables the freshness comparison.
	CurrentFingerprint string
	// IsStale marks drift already detected by the caller (e.g. Boundary-5).
	IsStale bool
	// MaxOutputTokens is the enforced per-step output ceiling.
	// Zero means unknown/unbounded for admission (execution preflight
	// still gates later when a real ceiling is known).
	MaxOutputTokens int
	// AllowedScope is the pre-approved scope envelope ($hot). Empty
	// disables the scope-envelope check.
	AllowedScope []string
	// RefinementDepth tracks prior refinements (bounds the chain).
	RefinementDepth int
}

// AdmitDriverStep consults stepadmission.AdmitStep as a pure bound-check
// library. The Driver owns the resulting decision: ADMIT proceeds, REFINE
// narrows to the refined subset (still human-approved downstream — never a
// silent scope change), and BLOCK/STALE/AWAITING_APPROVAL fall through to
// the explicit human re-scope boundary.
func AdmitDriverStep(in DriverAdmissionInput) stepadmission.StepAdmissionDecision {
	candidate := stepadmission.CandidateStep{
		ID:              "driver-step",
		Kind:            "MUTATE",
		Targets:         append([]string(nil), in.Targets...),
		Intent:          in.Intent,
		StateDigest:     in.StateDigest,
		RefinementDepth: in.RefinementDepth,
	}
	// The Driver always presents a derived estimate: the zero-value
	// StepSizeEstimate is Valid() by construction, so presenting it would
	// silently void the budget comparison (Expected 0 always fits).
	// Deriving here keeps the bound-check load-bearing.
	candidate.Estimate = stepadmission.EstimateForCandidate(candidate)
	capability := stepadmission.CapabilityFromOutputCeiling(in.MaxOutputTokens)
	state := stepadmission.AdmissionState{
		CurrentFingerprint: in.CurrentFingerprint,
		IsStale:            in.IsStale,
	}
	return stepadmission.AdmitStep(
		candidate,
		capability,
		stepadmission.DefaultFallbackBudget,
		in.AllowedScope,
		state,
	)
}

// DriverContinuationInput carries the Driver-owned facts needed for one pure
// continuation consultation. Observations are caller-supplied authoritative
// facts (execution/verification/state); a nil slice means "no authoritative
// evidence", which the pure function treats as a model-claim-only state.
type DriverContinuationInput struct {
	// Objective is the task intent (preserved into the task view).
	Objective string
	// Targets is the durable active scope ($hot / $prompt envelope).
	Targets []string
	// StateFingerprint is the workspace/state digest (OCC).
	StateFingerprint string
	// HasStaleState marks fingerprint drift already detected by the caller
	// (Boundary-5 workspace_drift). False unless the caller observed drift.
	HasStaleState bool
	// IsPartialOutput marks finish_reason=length truncation.
	IsPartialOutput bool
	// PreviousOutcome is the scheduler StepOutcome string for the last
	// step: pending/complete/partial/failed.
	PreviousOutcome string
	// Verified reports whether verification passed for the last execution.
	Verified bool
	// AllowedScope is the envelope continuation must not expand.
	AllowedScope []string
	// ProviderCeiling is the per-step output ceiling.
	ProviderCeiling int
	// Observations carries authoritative execution/verification evidence.
	Observations []continuation.Observation
}

// DeriveDriverContinuation consults continuation.DeriveNextStep as a pure
// transition function. The Driver recovery matrix remains the decision
// owner: today only the STALE verdict is acted on (it confirms the
// Boundary-5 workspace_drift abort through the existing drift path); every
// other verdict is advisory and progression stays with the matrix.
func DeriveDriverContinuation(in DriverContinuationInput) continuation.ContinuationDecision {
	allowed := append([]string(nil), in.AllowedScope...)
	if len(allowed) == 0 {
		allowed = append([]string(nil), in.Targets...)
	}
	return continuation.DeriveNextStep(continuation.DerivationInput{
		Task: continuation.TaskStateView{
			Intent:           in.Objective,
			ActiveScope:      append([]string(nil), in.Targets...),
			StateFingerprint: in.StateFingerprint,
			ProviderCeiling:  in.ProviderCeiling,
		},
		PreviousOutcome: in.PreviousOutcome,
		Observations:    in.Observations,
		Verified:        in.Verified,
		HasStaleState:   in.HasStaleState,
		IsPartialOutput: in.IsPartialOutput,
		AllowedScope:    allowed,
	})
}

// driverOutcomeToStepOutcome maps a Driver observation outcome onto the
// scheduler StepOutcome vocabulary consumed by the pure continuation
// function. Partial/truncation maps to "partial"; terminal failure maps to
// "failed"; everything else maps to "complete" (the caller refines with
// Verified/HasStaleState as needed).
func driverOutcomeToStepOutcome(o autonomy.ExecutionOutcome) string {
	switch o {
	case autonomy.OutcomeTruncated:
		return "partial"
	case autonomy.OutcomeFailed, autonomy.OutcomePatchGenFailed,
		autonomy.OutcomePatchFailed, autonomy.OutcomeApplyFailed,
		autonomy.OutcomeVerifyFailed, autonomy.OutcomeArtifactRejected,
		autonomy.OutcomeArtifactRetryableRejected, autonomy.OutcomeSkipped,
		autonomy.OutcomePreflightInfeasible, autonomy.OutcomeWorkspaceDrift,
		autonomy.OutcomeNoOpObjectiveUnresolved:
		return "failed"
	default:
		return "complete"
	}
}
