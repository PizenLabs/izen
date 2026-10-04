package authorization

import (
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain/capability"
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
	// READ-ONLY: the budget is consulted for its remaining capacity only. The
	// per-operation wall-clock window is armed by the authorization entry point
	// (Evaluate / AuthorizeBuildCandidate), never by a reader.
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
	if !state.Executable() {
		return nil, &AuthorizationDenied{
			Step:    StepWorkflowState,
			Message: executableStateRefusal(state),
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

	if b == nil {
		return nil, &AuthorizationDenied{
			Step:    StepBudgetSufficiency,
			Message: "no mutation budget is bound to this authorization engine",
		}
	}
	// Each authorization attempt is ONE mutation operation: arm the wall-clock
	// bound against it rather than against the age of the process.
	b.BeginOperation()
	if b.IsExhausted() || b.OperationTimeExceeded() {
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
	return e.AuthorizeBuildCandidate(targetFiles, caps, mutBudget, microBudget, isMicroPlan, humanApproved, "")
}

// AuthorizeBuildCandidate is AuthorizeBuild bound to a MutationCandidate
// identity. candidateID is stored on the issued token, and the mutation boundary
// refuses a token whose CandidateID is not the candidate it is applying — so an
// approval opened for one computation cannot carry another one's bytes across the
// mutation boundary. An empty candidateID preserves the unbound behaviour of a
// direct build execution.
func (e *AuthorizationEngine) AuthorizeBuildCandidate(
	targetFiles []string,
	caps *capability.CapabilitySet,
	mutBudget *budget.MutationBudget,
	microBudget *budget.MicroBudget,
	isMicroPlan bool,
	humanApproved bool,
	candidateID string,
) (*MutationAuthorization, error) {
	return e.AuthorizeBuildCandidateContent(targetFiles, caps, mutBudget, microBudget, isMicroPlan, humanApproved, candidateID, "")
}

// AuthorizeBuildCandidateContent is AuthorizeBuildCandidate bound to the
// candidate's CONTENT as well as its identity.
//
// candidateDigest is the fingerprint the human's mutation review was rendered
// from. Carrying it on the token makes the authorization a statement about one
// concrete change: if the held candidate is ever replaced in place, the digest no
// longer matches and the mutation boundary refuses, forcing a new review rather
// than writing bytes nobody saw.
//
// WORKFLOW STATE IS AN EXECUTABLE-POSITION GATE, NOT A COMPLETION SIGNAL.
// The check refuses any position from which no mutation may be applied, and names
// the parked position explicitly when one is what blocked it — so "awaiting human
// authorization" is never reported as an inexplicable `idle`.
func (e *AuthorizationEngine) AuthorizeBuildCandidateContent(
	targetFiles []string,
	caps *capability.CapabilitySet,
	mutBudget *budget.MutationBudget,
	microBudget *budget.MicroBudget,
	isMicroPlan bool,
	humanApproved bool,
	candidateID string,
	candidateDigest string,
) (*MutationAuthorization, error) {
	state := e.getState()

	// A mutation may only be authorized from an executable position. A run
	// parked at a human boundary has not reached one: the control plane must
	// RESUME it first, and that resume is itself an explicit event.
	if !state.Executable() {
		return nil, &AuthorizationDenied{
			Step:    StepWorkflowState,
			Message: executableStateRefusal(state),
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

	// Every authorization attempt is ONE mutation operation, so the wall-clock
	// bound is armed against this operation rather than against the age of the
	// process. See budget.MutationBudget.BeginOperation.
	mutBudget.BeginOperation()

	ref, err := e.admitBuild(targetFiles, caps, mutBudget, humanApproved)
	if err != nil {
		return nil, err
	}

	// Multi-step plans get non-single-use authorization.
	singleUse := !mutBudget.IsMultiStepPlan()

	auth := &MutationAuthorization{
		ID:              NewAuthorizationID(),
		ProposalHash:    "",
		CandidateID:     candidateID,
		CandidateDigest: candidateDigest,
		CheckpointRef:   ref,
		ExpiresAt:       time.Now().Add(5 * time.Minute),
		SingleUse:       singleUse,
		IssuedAt:        time.Now(),
	}

	if !mutBudget.IsMultiStepPlan() {
		// The spend is REPORTED, never discarded: a budget that refuses the
		// mutation must not also silently swallow the accounting.
		if consumeErr := mutBudget.Consume(budget.BudgetDelta{Files: len(targetFiles)}); consumeErr != nil {
			return nil, &AuthorizationDenied{
				Step:    StepBudgetSufficiency,
				Message: consumeErr.Error(),
			}
		}
	}

	return auth, nil
}

// executableStateRefusal names the state that blocked a mutation authorization.
// A parked run gets the actionable sentence — the control plane must RESUME it —
// because "got idle" for a run that is demonstrably waiting on a human is exactly
// the misleading diagnostic this lifecycle exists to remove.
func executableStateRefusal(state workflow.WorkflowState) string {
	if state.Parked() {
		return fmt.Sprintf("execution is parked at %s awaiting human authorization; resume the parked run before authorizing a mutation (no candidate was applied)", state)
	}
	return fmt.Sprintf("expected Building or Repairing for build execution, got %s; approve the plan via /build first", state)
}

// AdmissibleBuild answers ONE question: could a mutation over targetFiles be
// authorized at all, if the human pressed Approve? It evaluates every
// NON-HUMAN admission clause — scope containment, mutation budget sufficiency,
// checkpoint availability and the unified PolicyEngine — and consumes nothing.
//
// It exists so the approval boundary can be gated on the runtime's own authority
// BEFORE a human is asked. Human approval is the FINAL gate, not a UI
// placeholder for a proposal the runtime already knows it cannot authorize.
//
// The clause list is the same one AuthorizeBuildCandidate runs, through the same
// admitBuild helper, so the probe and the real authorization cannot disagree.
func (e *AuthorizationEngine) AdmissibleBuild(
	targetFiles []string,
	caps *capability.CapabilitySet,
	mutBudget *budget.MutationBudget,
) error {
	// Re-arm the per-operation window so the probe reads the same clock the
	// authorization would read.
	mutBudget.BeginOperation()
	_, err := e.admitBuild(targetFiles, caps, mutBudget, true)
	return err
}

// admitBuild is the shared NON-HUMAN admission clause list of the build path:
// scope containment, mutation budget sufficiency, checkpoint verification and the
// unified PolicyEngine, in that order.
//
// humanApproved selects how the PolicyEngine's approval requirement is read.
// AuthorizeBuild passes the caller's real answer; the admission probe passes true
// because the approval gate is precisely what it is about to open — the probe
// answers "is anything ELSE going to refuse this?", never "does a human still
// need to say yes?".
func (e *AuthorizationEngine) admitBuild(
	targetFiles []string,
	caps *capability.CapabilitySet,
	mutBudget *budget.MutationBudget,
	humanApproved bool,
) (workflow.CheckpointRef, error) {
	for _, path := range targetFiles {
		if caps == nil || !caps.CanMutateFile(path) {
			return checkpointRefZero, &AuthorizationDenied{
				Step:    StepScopeContainment,
				Message: fmt.Sprintf("file %q not covered by capability scope", path),
			}
		}
	}

	if mutBudget != nil && (mutBudget.IsExhausted() || mutBudget.OperationTimeExceeded()) {
		return checkpointRefZero, &AuthorizationDenied{
			Step:    StepBudgetSufficiency,
			Message: "mutation budget already exhausted",
		}
	}

	if e.checkpoint == nil || !e.checkpoint.HasCheckpoint() {
		return checkpointRefZero, &AuthorizationDenied{
			Step:    StepCheckpointVerification,
			Message: "no valid checkpoint exists — ensure a checkpoint is created before build execution",
		}
	}
	ref, err := e.checkpoint.LatestCheckpoint()
	if err != nil {
		return checkpointRefZero, &AuthorizationDenied{
			Step:    StepCheckpointVerification,
			Message: fmt.Sprintf("cannot retrieve checkpoint: %s", err),
		}
	}

	// Unified PolicyEngine consultation for the build execution path.
	if err := e.delegatePolicy(targetFiles, mutBudget, humanApproved); err != nil {
		return checkpointRefZero, err
	}
	return ref, nil
}

func stateInList(s artifact.LifecycleState, list []artifact.LifecycleState) bool {
	for _, v := range list {
		if s == v {
			return true
		}
	}
	return false
}

// checkpointRefZero is the empty checkpoint reference an admission refusal
// carries. It is never issued: a refusal returns an error, not a token.
var checkpointRefZero workflow.CheckpointRef
