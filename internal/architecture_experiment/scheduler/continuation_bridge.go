package scheduler

import (
	"github.com/PizenLabs/izen/internal/continuation"
)

// ContinuationIntegration traces one EXPERIMENT execution path through the
// demoted scheduler (Phase 8 M1). It is NOT the production runtime — the
// canonical production path is Driver → execution/planner → admission →
// execution.RuntimeExecutor → Driver recovery matrix. This bridge exists
// only so the experiment suite can exercise the Schedule/RunNext loop
// against a stub worker without production wiring:
//
//	Task (ProblemSolvingPlan)
//	  → bounded ExecutionStep (via Schedule)
//	  → experiment StepScheduler (NOT production admission)
//	  → stub execution (via RunNext staging)
//	  → outcome / evidence (StepResult)
//	  → continuation.DeriveNextStep (pure proposal)
//	  → next bounded StepProposal → next TaskSpec
//
// The function is minimal and observable: it takes the durable task snapshot
// and the continuation decision and materializes the next TaskSpec that the
// experiment StepScheduler will schedule. It never creates a second
// production scheduler, second executor, or authorization grant.
//
// It is intentionally a thin adapter; the authority remains with Schedule/
// AcceptStep and the existing authorization boundary (outside this file).
func ContinuationToTaskSpec(
	base TaskSpec,
	decision continuation.ContinuationDecision,
) (TaskSpec, bool) {
	if decision.Action != continuation.ActionContinue {
		return TaskSpec{}, false
	}
	if decision.NextStep == nil || len(decision.NextStep.Targets) == 0 {
		return TaskSpec{}, false
	}
	next := base
	// Preserve durable task scope; per-step targets are the continuation's
	// bounded proposal, not an expanded scope.
	next.Targets = append([]string(nil), decision.NextStep.Targets...)
	next.StepIndex = 0
	// Carry forward per-step budget: provider ceiling bounds the step, not the task.
	if decision.NextStep.EstimatedTokens > 0 {
		if next.RequestedStepBudget > 0 && decision.NextStep.EstimatedTokens > next.RequestedStepBudget {
			next.RequestedStepBudget = decision.NextStep.EstimatedTokens
		}
	}
	// Evidence: continuation's authoritative evidence becomes the next slice's
	// LatestEvidence for the scheduler's ContextPlanner.
	if len(decision.Evidence) > 0 {
		ev := make([]EvidenceItem, 0, len(decision.Evidence))
		for _, o := range decision.Evidence {
			ev = append(ev, EvidenceItem{
				Kind:    string(o.Kind),
				Subject: o.Subject,
				Digest:  o.EvidenceDigest,
				Detail:  o.Detail,
			})
		}
		next.LatestEvidence = ev
	}
	return next, true
}

// DeriveContinuation is a convenience used by the experiment suite tests to
// show the full path: compose a derivation input from task snapshot +
// scheduler result + evidence and call the pure continuation layer. It has
// zero production callers (pinned by
// TestExperimentSchedulerHasNoProductionImporters).
func DeriveContinuation(
	taskSnap TaskStateSnapshot,
	spec TaskSpec,
	result StepResult,
	obs []continuation.Observation,
	allowedScope []string,
	isStale bool,
) continuation.ContinuationDecision {
	previousOutcome := string(result.Outcome)
	reason := result.Reason
	isPartial := result.Outcome == StepOutcomePartial
	verified := result.Patches > 0 // simplified: patches + verification passed means verified

	// Build plan steps projection from spec if available (domain-neutral)
	planSteps := make([]continuation.PlanStepView, 0, len(spec.Targets))
	// When plan steps are not carried in spec, use targets as fallback
	for i, t := range spec.Targets {
		planSteps = append(planSteps, continuation.PlanStepView{
			ID:         nextID(i),
			Kind:       string(spec.Type),
			References: []string{t},
			Rationale:  "evidence-backed target from durable plan",
		})
	}

	taskView := continuation.TaskStateView{
		TaskID:              spec.Objective,
		Intent:              spec.Objective,
		ActiveScope:         append([]string(nil), spec.Targets...),
		StateFingerprint:    taskSnap.StateFingerprint,
		EvidenceDigest:      taskSnap.EvidenceDigest,
		CurrentStepID:       result.Step.ID,
		CompletedSteps:      append([]string(nil), taskSnap.CompletedSteps...),
		PendingSteps:        append([]string(nil), taskSnap.PendingSteps...),
		ProviderCeiling:     0,
		RequestedBudget:     spec.RequestedStepBudget,
		RemainingTaskBudget: spec.TaskRemainingBudget,
	}
	if spec.Provider != nil {
		taskView.ProviderCeiling = spec.Provider.ResolvedOutputLimit().Value
	}
	// Populate history from spec.History
	for target, outs := range spec.History {
		for _, o := range outs {
			taskView.History = append(taskView.History, continuation.StepHistoryEntry{
				StepID:           target,
				Outcome:          string(o),
				StateFingerprint: taskView.StateFingerprint,
				EvidenceDigest:   taskView.EvidenceDigest,
				Patches:          0,
			})
		}
	}

	in := continuation.DerivationInput{
		Task:            taskView,
		PlanSteps:       planSteps,
		PreviousOutcome: previousOutcome,
		PreviousReason:  reason,
		Observations:    obs,
		Verified:        verified,
		HasStaleState:   isStale,
		IsPartialOutput: isPartial,
		AllowedScope:    append([]string(nil), allowedScope...),
	}
	return continuation.DeriveNextStep(in)
}

func nextID(i int) string {
	return "step-" + itoaSmall(i+1)
}

func itoaSmall(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	if n < 100 {
		return string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	return "01"
}
