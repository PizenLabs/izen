package authorization

import (
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
)

type SourceHashVerifier interface {
	VerifySourceHash(paths []string, snapshotHash string) error
}

type CheckpointChecker interface {
	HasCheckpoint() bool
	LatestCheckpoint() (workflow.CheckpointRef, error)
}

type AuthorizationEngine struct {
	verifier   SourceHashVerifier
	checkpoint CheckpointChecker
	lifecycle  *artifact.LifecycleTransitionValidator
	getState   func() workflow.WorkflowState
}

func NewAuthorizationEngine(
	verifier SourceHashVerifier,
	checkpoint CheckpointChecker,
	getState func() workflow.WorkflowState,
) *AuthorizationEngine {
	return &AuthorizationEngine{
		verifier:   verifier,
		checkpoint: checkpoint,
		lifecycle:  artifact.NewLifecycleTransitionValidator(),
		getState:   getState,
	}
}

func (e *AuthorizationEngine) Evaluate(
	proposal *MutationProposal,
	plan artifact.Artifact,
	patch artifact.Artifact,
	caps *capability.CapabilitySet,
	b *budget.MutationBudget,
	microBudget *budget.MicroBudget,
	isMicroPlan bool,
	humanApproved bool,
) (*MutationAuthorization, error) {
	state := e.getState()
	if state != workflow.StateBuilding && state != workflow.StateRepairing {
		return nil, &AuthorizationDenied{
			Step:    StepWorkflowState,
			Message: fmt.Sprintf("expected Building or Repairing, got %s", state),
		}
	}

	validLifecycles := []artifact.LifecycleState{
		artifact.StateValidated,
		artifact.StateAuthorized,
	}
	if isMicroPlan {
		validLifecycles = append(validLifecycles, artifact.StateDraft)
	}
	if !stateInList(plan.State(), validLifecycles) {
		return nil, &AuthorizationDenied{
			Step:    StepArtifactLifecycle,
			Message: fmt.Sprintf("plan %s in state %s", plan.ID(), plan.State()),
		}
	}
	if patch != nil {
		if !stateInList(patch.State(), validLifecycles) {
			return nil, &AuthorizationDenied{
				Step:    StepArtifactLifecycle,
				Message: fmt.Sprintf("patch %s in state %s", patch.ID(), patch.State()),
			}
		}
	}

	if !humanApproved {
		preApproved := isMicroPlan && microBudget != nil &&
			microBudget.IsWithinMicroBudget(proposal.EstimatedDelta, e.checkpoint.HasCheckpoint())
		if !preApproved {
			return nil, &AuthorizationDenied{
				Step:    StepArtifactApproval,
				Message: "human approval required and not provided, and micro-plan pre-approval does not apply",
			}
		}
	}

	for _, path := range proposal.TargetFiles {
		if !caps.CanMutateFile(path) {
			return nil, &AuthorizationDenied{
				Step:    StepScopeContainment,
				Message: fmt.Sprintf("file %q not covered by capability scope", path),
			}
		}
	}

	if err := e.verifier.VerifySourceHash(proposal.TargetFiles, proposal.SourceSnapshotHash); err != nil {
		return nil, &AuthorizationDenied{
			Step:    StepDependencyFreshness,
			Message: fmt.Sprintf("source hash mismatch: %s", err),
		}
	}

	if proposal.RequiredCaps&CapFlagWrite != 0 && !caps.Has(capability.CapabilityWrite) {
		return nil, &AuthorizationDenied{
			Step:    StepCapabilityGuard,
			Message: "capability write not granted",
		}
	}
	if proposal.RequiredCaps&CapFlagPatch != 0 && !caps.Has(capability.CapabilityPatch) {
		return nil, &AuthorizationDenied{
			Step:    StepCapabilityGuard,
			Message: "capability patch not granted",
		}
	}
	if proposal.RequiredCaps&CapFlagExecute != 0 && !caps.Has(capability.CapabilityExecute) {
		return nil, &AuthorizationDenied{
			Step:    StepCapabilityGuard,
			Message: "capability execute not granted",
		}
	}

	if b.IsExhausted() {
		return nil, &AuthorizationDenied{
			Step:    StepBudgetSufficiency,
			Message: "mutation budget already exhausted",
		}
	}
	if err := b.Consume(proposal.EstimatedDelta); err != nil {
		return nil, &AuthorizationDenied{
			Step:    StepBudgetSufficiency,
			Message: err.Error(),
		}
	}

	if !e.checkpoint.HasCheckpoint() {
		return nil, &AuthorizationDenied{
			Step:    StepCheckpointVerification,
			Message: "no valid checkpoint exists",
		}
	}
	ref, err := e.checkpoint.LatestCheckpoint()
	if err != nil {
		return nil, &AuthorizationDenied{
			Step:    StepCheckpointVerification,
			Message: fmt.Sprintf("cannot retrieve checkpoint: %s", err),
		}
	}

	return &MutationAuthorization{
		ID:            NewAuthorizationID(),
		ProposalHash:  proposal.Hash(),
		CheckpointRef: ref,
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		SingleUse:     true,
		IssuedAt:      time.Now(),
	}, nil
}

// AuthorizeBuild performs execution-level authorization for the build/patch
// execution path. Unlike Evaluate (which requires full artifact lifecycle
// validation), this method is designed for the direct build execution flow:
// it validates capabilities, budget, and checkpoint state without requiring
// plan/patch artifacts. It also sets the workflow state to Building if needed.
//
// Returns a MutationAuthorization token that must be passed to
// execution.Engine.SetAuthorization() before calling Run() or Apply().
func (e *AuthorizationEngine) AuthorizeBuild(
	targetFiles []string,
	caps *capability.CapabilitySet,
	mutBudget *budget.MutationBudget,
	microBudget *budget.MicroBudget,
	isMicroPlan bool,
	humanApproved bool,
) (*MutationAuthorization, error) {
	state := e.getState()

	// Auto-transition to Building if in an allowed pre-build state.
	if state != workflow.StateBuilding && state != workflow.StateRepairing {
		return nil, &AuthorizationDenied{
			Step:    StepWorkflowState,
			Message: fmt.Sprintf("expected Building or Repairing for build execution, got %s; approve the plan via /build first", state),
		}
	}

	for _, path := range targetFiles {
		if !caps.CanMutateFile(path) {
			return nil, &AuthorizationDenied{
				Step:    StepScopeContainment,
				Message: fmt.Sprintf("file %q not covered by capability scope", path),
			}
		}
	}

	if !humanApproved {
		preApproved := isMicroPlan && microBudget != nil &&
			microBudget.IsWithinMicroBudget(budget.BudgetDelta{Files: len(targetFiles), DiffLines: 100, Tokens: 2000, Attempts: 1}, e.checkpoint.HasCheckpoint())
		if !preApproved {
			return nil, &AuthorizationDenied{
				Step:    StepArtifactApproval,
				Message: "human approval required and not provided, and micro-plan pre-approval does not apply",
			}
		}
	}

	if mutBudget != nil && mutBudget.IsExhausted() {
		return nil, &AuthorizationDenied{
			Step:    StepBudgetSufficiency,
			Message: "mutation budget already exhausted",
		}
	}

	if !e.checkpoint.HasCheckpoint() {
		return nil, &AuthorizationDenied{
			Step:    StepCheckpointVerification,
			Message: "no valid checkpoint exists — ensure a checkpoint is created before build execution",
		}
	}
	ref, err := e.checkpoint.LatestCheckpoint()
	if err != nil {
		return nil, &AuthorizationDenied{
			Step:    StepCheckpointVerification,
			Message: fmt.Sprintf("cannot retrieve checkpoint: %s", err),
		}
	}

	auth := &MutationAuthorization{
		ID:            NewAuthorizationID(),
		ProposalHash:  "",
		CheckpointRef: ref,
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		SingleUse:     true,
		IssuedAt:      time.Now(),
	}

	if mutBudget != nil {
		_ = mutBudget.Consume(budget.BudgetDelta{Files: len(targetFiles)})
	}

	return auth, nil
}

func stateInList(s artifact.LifecycleState, list []artifact.LifecycleState) bool {
	for _, v := range list {
		if s == v {
			return true
		}
	}
	return false
}
