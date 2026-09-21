package stepadmission

import "time"

// StepAdmissionEvidence records bounded runtime evidence for future
// calibration (spec §20). It is recording only; Phase 5 does NOT implement
// online learning, adaptive training, reinforcement, or self-learning planner.
//
// Evidence dimensions that may be recorded per step:
//   - EstimatedSize (admission-time estimate)
//   - ActualOutputTokens / FinishReason / StepOutcome / ContinuationCount
//   - RefinementCount / VerificationResult
//
// The evidence is pure data; no learning loop consumes it in this phase.
type StepAdmissionEvidence struct {
	StepID             string           `json:"step_id"`
	Intent             string           `json:"intent,omitempty"`
	Estimated          StepSizeEstimate `json:"estimated"`
	Budget             int              `json:"budget"`
	Action             AdmissionAction  `json:"action"`
	ActualOutputTokens int              `json:"actual_output_tokens,omitempty"`
	FinishReason       string           `json:"finish_reason,omitempty"`
	StepOutcome        string           `json:"step_outcome,omitempty"`
	ContinuationCount  int              `json:"continuation_count,omitempty"`
	RefinementCount    int              `json:"refinement_count,omitempty"`
	VerificationPassed *bool            `json:"verification_passed,omitempty"`
	Timestamp          time.Time        `json:"timestamp"`
}

// RecordAdmissionEvidence builds an evidence record from a decision.
// It is pure and side-effect-free; persistence is the caller's responsibility.
func RecordAdmissionEvidence(decision StepAdmissionDecision, actualTokens int, finishReason, outcome string, continuations, refinements int, verified *bool) StepAdmissionEvidence {
	stepID := ""
	intent := ""
	if decision.Step != nil {
		stepID = decision.Step.ID
		intent = decision.Step.Intent
	}
	return StepAdmissionEvidence{
		StepID:             stepID,
		Intent:             intent,
		Estimated:          decision.Estimate,
		Budget:             decision.Budget.MaxOutputTokens,
		Action:             decision.Action,
		ActualOutputTokens: actualTokens,
		FinishReason:       finishReason,
		StepOutcome:        outcome,
		ContinuationCount:  continuations,
		RefinementCount:    refinements,
		VerificationPassed: verified,
		Timestamp:          time.Now(),
	}
}
