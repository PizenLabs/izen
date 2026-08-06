package authorization

import (
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain/policy"
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
	policy     *policy.PolicyEngine
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

// WithPolicyEngine binds the unified PolicyEngine. Every mutation that passes
// the operational gates (lifecycle, scope, freshness, budget, checkpoint) is
// additionally adjudicated by the PolicyEngine — the single owner of the "is
// this action permitted?" question. A nil engine leaves the historical
// behavior unchanged.
func (e *AuthorizationEngine) WithPolicyEngine(p *policy.PolicyEngine) *AuthorizationEngine {
	e.policy = p
	return e
}

// PolicyEngine returns the bound PolicyEngine, or nil when none is wired.
func (e *AuthorizationEngine) PolicyEngine() *policy.PolicyEngine { return e.policy }

// delegatePolicy consults the unified PolicyEngine for every mutation target.
// A DENY verdict maps to StepPolicy; a REQUIRE_APPROVAL verdict without human
// approval maps to StepArtifactApproval. A nil engine is a no-op.
func (e *AuthorizationEngine) delegatePolicy(
	targetFiles []string,
	b *budget.MutationBudget,
	humanApproved bool,
) error {
	if e.policy == nil {
		return nil
	}
	var remainingTokens int
	if b != nil {
		remainingTokens = b.RemainingTokens()
	}
	ctx := policy.PolicyContext{
		ActiveMode:      "build",
		RemainingTokens: remainingTokens,
		IsHumanApproved: humanApproved,
	}
	for _, path := range targetFiles {
		v := e.policy.Evaluate(policy.Action{Kind: policy.ActionFileWrite, Target: path}, ctx)
		switch {
		case v.Allowed == policy.VerdictDeny:
			return &AuthorizationDenied{Step: StepPolicy, Message: v.Reason}
		case v.RequiresApproval() && !humanApproved:
			return &AuthorizationDenied{Step: StepArtifactApproval, Message: v.Reason}
		}
	}
	return nil
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
	if !b.IsMultiStepPlan() {
		// Single-step plan: consume budget delta immediately.
		if err := b.Consume(proposal.EstimatedDelta); err != nil {
			return nil, &AuthorizationDenied{
				Step:    StepBudgetSufficiency,
				Message: err.Error(),
			}
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

	// Unified PolicyEngine: the single owner of the permission question. Every
	// mutation that passed the operational gates above is still adjudicated by
	// the bound PolicyEngine, which re-checks mode, capability and risk policy.
	if err := e.delegatePolicy(proposal.TargetFiles, b, humanApproved); err != nil {
		return nil, err
	}

	// Multi-step plans get non-single-use authorization so steps 1..N
	// can all pass through the guardrail. The budget's mutation counter
	// enforces the actual cap — no risk of runaway execution.
	singleUse := !b.IsMultiStepPlan()

	return &MutationAuthorization{
		ID:            NewAuthorizationID(),
		ProposalHash:  proposal.Hash(),
		CheckpointRef: ref,
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		SingleUse:     singleUse,
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

	// Unified PolicyEngine consultation for the build execution path.
	if err := e.delegatePolicy(targetFiles, mutBudget, humanApproved); err != nil {
		return nil, err
	}

	// Multi-step plans get non-single-use authorization.
	singleUse := !mutBudget.IsMultiStepPlan()

	auth := &MutationAuthorization{
		ID:            NewAuthorizationID(),
		ProposalHash:  "",
		CheckpointRef: ref,
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		SingleUse:     singleUse,
		IssuedAt:      time.Now(),
	}

	if mutBudget != nil && !mutBudget.IsMultiStepPlan() {
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
