package ephemeral

import (
	"fmt"
	"strings"
)

// RecoveryAction is the deterministic outcome of assessing an interrupted
// task: continue the current step (RESUME), rebuild the plan (RE_PLAN),
// or yield to the human boundary (ESCALATE).
type RecoveryAction string

const (
	ActionResume   RecoveryAction = "RESUME"
	ActionRePlan   RecoveryAction = "RE_PLAN"
	ActionEscalate RecoveryAction = "ESCALATE"
)

// AssessmentInput is the full precondition set for one recovery decision.
// All fields are explicit so the decision is a pure function of its input.
type AssessmentInput struct {
	// Reason is the already-classified failure (ledger-first invariant:
	// assessment never runs on an unclassified error).
	Reason FailureReason
	// PlanValid, TargetValid, EvidenceValid and CurrentStepClear are the
	// four validity gates; SemanticDriftObserved is the drift veto.
	PlanValid             bool
	TargetValid           bool
	EvidenceValid         bool
	CurrentStepClear      bool
	SemanticDriftObserved bool
	// ConsecutiveFailures counts failures of the current step under
	// identical conditions (same step ID + same precondition digest).
	ConsecutiveFailures int
	// VerificationInvalidated is true when verification explicitly
	// invalidated an underlying plan hypothesis.
	VerificationInvalidated bool
	// RecoveryAttemptsLeft is the remaining bounded budget; reaching 0
	// forces ESCALATE regardless of every other signal.
	RecoveryAttemptsLeft int
	// Capsule is the current bounded state; returned (possibly
	// re-compacted) on RESUME so the caller handoff is one step.
	Capsule TaskCapsule
}

// AssessmentResult is the deterministic decision plus its audit trail.
type AssessmentResult struct {
	Action         RecoveryAction
	Reason         string
	TargetWorker   string
	UpdatedCapsule *TaskCapsule
}

// RecoveryAssessment answers RESUME vs RE_PLAN vs ESCALATE deterministically.
// The zero value is ready to use.
type RecoveryAssessment struct{}

// replanThreshold is the consecutive-identical-failure count that forces
// RE_PLAN (spec: current step failed >= 2 consecutive attempts under
// identical conditions).
const replanThreshold = 2

// Assess applies the decision rules in priority order:
//
//  1. Budget exhausted (RecoveryAttemptsLeft <= 0) -> ESCALATE.
//  2. Cursor TARGET_CONFLICT (Reason == TARGET_CONFLICT) -> RE_PLAN.
//  3. Verification invalidated a plan hypothesis -> RE_PLAN.
//  4. ConsecutiveFailures >= 2 under identical conditions -> RE_PLAN.
//  5. Semantic drift observed or reason is SEMANTIC_DRIFT -> RE_PLAN.
//  6. USER_CANCELLED -> ESCALATE (control returns to the human boundary).
//  7. All gates (PlanValid && TargetValid && EvidenceValid &&
//     CurrentStepClear && !drift) hold -> RESUME.
//  8. Otherwise -> RE_PLAN (structural uncertainty is never resumed).
func (RecoveryAssessment) Assess(in AssessmentInput, targetWorker string) (AssessmentResult, error) {
	if !in.Reason.Valid() {
		return AssessmentResult{}, fmt.Errorf("ephemeral: assess requires classified reason")
	}
	if strings.TrimSpace(in.Capsule.TaskID) == "" {
		return AssessmentResult{}, fmt.Errorf("ephemeral: assess requires capsule task id")
	}
	if in.RecoveryAttemptsLeft <= 0 {
		return AssessmentResult{
			Action: ActionEscalate,
			Reason: "recovery budget exhausted; yielding to human boundary",
		}, nil
	}
	if in.Reason == FailureUserCancelled {
		return AssessmentResult{
			Action: ActionEscalate,
			Reason: "user cancelled; control returns to human boundary",
		}, nil
	}
	if in.Reason == FailureTargetConflict {
		return AssessmentResult{
			Action: ActionRePlan,
			Reason: "execution cursor reports TARGET_CONFLICT (external tree modification)",
		}, nil
	}
	if in.VerificationInvalidated {
		return AssessmentResult{
			Action: ActionRePlan,
			Reason: "verification explicitly invalidated an underlying plan hypothesis",
		}, nil
	}
	if in.ConsecutiveFailures >= replanThreshold {
		return AssessmentResult{
			Action: ActionRePlan,
			Reason: fmt.Sprintf("current step failed %d consecutive attempts under identical conditions", in.ConsecutiveFailures),
		}, nil
	}
	if in.SemanticDriftObserved || in.Reason == FailureSemanticDrift {
		return AssessmentResult{
			Action: ActionRePlan,
			Reason: "semantic drift detected; resume would continue a diverged plan",
		}, nil
	}
	if in.PlanValid && in.TargetValid && in.EvidenceValid && in.CurrentStepClear {
		updated := in.Capsule
		// TOKEN_LIMIT resume carries a re-compacted capsule so the
		// replacement worker starts within its context window.
		if in.Reason == FailureTokenLimit {
			updated = in.Capsule.Recompact(DefaultRecompactEvidenceKeep)
		}
		return AssessmentResult{
			Action:         ActionResume,
			Reason:         fmt.Sprintf("bounded RESUME after %s; all validity gates hold", in.Reason),
			TargetWorker:   targetWorker,
			UpdatedCapsule: &updated,
		}, nil
	}
	var missing []string
	if !in.PlanValid {
		missing = append(missing, "plan invalid")
	}
	if !in.TargetValid {
		missing = append(missing, "target invalid")
	}
	if !in.EvidenceValid {
		missing = append(missing, "evidence invalid")
	}
	if !in.CurrentStepClear {
		missing = append(missing, "current step not clear")
	}
	return AssessmentResult{
		Action: ActionRePlan,
		Reason: "resume preconditions unmet (" + strings.Join(missing, ", ") + "); structural re-plan required",
	}, nil
}
