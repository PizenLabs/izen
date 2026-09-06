package executor

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/authorization"
	"github.com/PizenLabs/izen/internal/core/domain/checkpoint"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/core/domain/occ"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// RuntimeExecutor is the single Control Plane coordinator.
// It enforces the 6-clause formula binding: every mutation must invoke
// CapabilityGuard.Evaluate before dispatching to Substrate.
type RuntimeExecutor struct {
	guard     authorization.CapabilityGuard
	substrate *substrate.Substrate
	occ       *occ.OCCGate
	ckpt      checkpoint.CheckpointCoordinator
	version   occ.StateVersion
	tracker   *BudgetTracker
}

// NewRuntimeExecutor creates an executor with the given dependencies.
func NewRuntimeExecutor(
	guard authorization.CapabilityGuard,
	sub *substrate.Substrate,
	occGate *occ.OCCGate,
	ckpt checkpoint.CheckpointCoordinator,
	budget domain.ResourceBudget,
) *RuntimeExecutor {
	if occGate == nil {
		occGate = &occ.OCCGate{}
	}
	return &RuntimeExecutor{
		guard:     guard,
		substrate: sub,
		occ:       occGate,
		ckpt:      ckpt,
		tracker:   NewBudgetTracker(budget),
	}
}

// OCCGate returns the underlying OCC gate.
func (e *RuntimeExecutor) OCCGate() *occ.OCCGate { return e.occ }

// Substrate returns the substrate.
func (e *RuntimeExecutor) Substrate() *substrate.Substrate { return e.substrate }

// Execute implements the 6-step pipeline:
// 1. Validate OCCGate sequence version
// 2. Construct AuthorizationInput
// 3. Call guard.Evaluate
// 4. Trigger checkpoint if needed
// 5. Delegate to substrate.ExecuteUnit
// 6. Record observation and advance version
func (e *RuntimeExecutor) Execute(ctx context.Context, intent domain.ExecutionIntent) (domain.ExecutionObservation, error) {
	// Step 1: Validate OCCGate sequence version for current state.
	if e.occ != nil {
		if _, err := e.occ.ValidateAndAdvance(&e.version, intent.ExpectedVersion); err != nil {
			obs := domain.ExecutionObservation{
				UnitID:          intent.Unit.UnitID,
				ProposalOutcome: domain.ProposalRejected,
				DependencyFresh: domain.FreshnessInvalidated,
				FailureSignals: []domain.FailureSignal{
					{Class: domain.FailureScope, Message: err.Error()},
				},
			}
			return obs, fmt.Errorf("%w: %w", occ.ErrStaleDependency, err)
		}
	}

	// Step 2: Construct AuthorizationInput from intent context.
	authInput := authorization.AuthorizationInput{
		Objective:    intent.Objective,
		Scope:        intent.Objective.TargetScope,
		Artifact:     authorization.ArtifactRef{ID: intent.Artifact.ID, Kind: intent.Artifact.Kind, State: intent.Artifact.State, Hash: intent.Artifact.Hash},
		CheckpointID: intent.CheckpointID,
		SourceState:  intent.SourceState,
		Budget:       intent.Budget,
		Capabilities: intent.Capabilities,
		Approval:     authorization.ApprovalToken{HumanApproved: intent.HumanApproved, BudgetIsPreApproval: intent.BudgetIsPreApproval},
		Proposal: authorization.ProposalRef{
			TargetFiles: intent.Unit.ObjectiveSlice.Targets,
			DiffLines:   0,
			Files:       len(intent.Unit.ObjectiveSlice.Targets),
		},
		HasCheckpoint: intent.HasCheckpoint,
	}

	// Step 3: Call guard.Evaluate
	if e.guard != nil {
		decision := e.guard.Evaluate(ctx, authInput)
		if !decision.Permitted {
			// Return the exact Clause error via mapped sentinel.
			obs := domain.ExecutionObservation{
				UnitID:          intent.Unit.UnitID,
				ProposalOutcome: domain.ProposalRejected,
				FailureSignals:  []domain.FailureSignal{{Class: domain.FailureUnknown, Message: decision.Reason}},
			}
			// Map failed clause to sentinel
			var clauseErr error
			switch decision.FailedClause {
			case authorization.ClauseIntent:
				clauseErr = authorization.ErrInvalidIntent
			case authorization.ClauseScope:
				clauseErr = authorization.ErrScopeViolation
			case authorization.ClausePlan:
				clauseErr = authorization.ErrNoAuthorizedPlan
			case authorization.ClauseCheckpoint:
				clauseErr = authorization.ErrNoCheckpoint
			case authorization.ClauseSourceHash:
				clauseErr = authorization.ErrStaleDependency
			case authorization.ClauseBudget:
				clauseErr = authorization.ErrBudgetExceeded
			case authorization.ClauseCapability:
				clauseErr = authorization.ErrCapabilityDenied
			case authorization.ClauseApproval:
				clauseErr = authorization.ErrApprovalRequired
			default:
				clauseErr = fmt.Errorf("authorization denied: %s", decision.Reason)
			}
			return obs, fmt.Errorf("%w: %s", clauseErr, decision.Reason)
		}
	}

	// Step 4: Trigger checkpoint before build if intent targets StateBuilding or workspace mutations.
	if e.ckpt != nil {
		if intent.WorkflowState == domain.StateBuilding || len(intent.Unit.ObjectiveSlice.Targets) > 0 {
			if _, err := e.ckpt.CreateBeforeBuild(ctx, intent.Unit.FrameID); err != nil {
				obs := domain.ExecutionObservation{
					UnitID:         intent.Unit.UnitID,
					FailureSignals: []domain.FailureSignal{{Class: domain.FailureEnvironment, Message: err.Error()}},
				}
				return obs, fmt.Errorf("checkpoint: %w", err)
			}
		}
	}

	// Apply budget tracking context wrapping.
	trackedCtx := ctx
	var cancel context.CancelFunc
	if e.tracker != nil {
		trackedCtx, cancel = e.tracker.WrapContext(ctx)
		if cancel != nil {
			defer cancel()
		}
	}

	// Step 5: Delegate to substrate.ExecuteUnit
	var result domain.MutationResult
	var execErr error
	if e.substrate != nil {
		result, execErr = e.substrate.ExecuteUnit(trackedCtx, intent.Unit)
		if execErr != nil {
			// Check for budget exceeded context cancellation
			if trackedCtx.Err() != nil && e.tracker != nil && e.tracker.Overflowed() {
				obs := domain.ExecutionObservation{
					UnitID:          intent.Unit.UnitID,
					ProposalOutcome: domain.ProposalDraftFrozen,
					FailureSignals:  []domain.FailureSignal{{Class: domain.FailureEnvironment, Message: authorization.ErrBudgetExceeded.Error()}},
				}
				return obs, fmt.Errorf("%w: %w", authorization.ErrBudgetExceeded, execErr)
			}
			obs := domain.ExecutionObservation{
				UnitID:         intent.Unit.UnitID,
				FailureSignals: []domain.FailureSignal{{Class: domain.FailureCode, Message: execErr.Error()}},
			}
			return obs, execErr
		}
	} else {
		result = domain.MutationResult{Applied: false, Targets: intent.Unit.ObjectiveSlice.Targets}
	}

	// Budget tracker records diff/files
	if e.tracker != nil {
		e.tracker.RecordFiles(len(result.Targets))
		e.tracker.RecordDiffLines(result.DiffLines)
		if e.tracker.CheckExceeded() {
			obs := domain.ExecutionObservation{
				UnitID:          intent.Unit.UnitID,
				ProposalOutcome: domain.ProposalDraftFrozen,
				MutationResult:  &result,
				FailureSignals:  []domain.FailureSignal{{Class: domain.FailureEnvironment, Message: authorization.ErrBudgetExceeded.Error()}},
			}
			return obs, authorization.ErrBudgetExceeded
		}
	}

	// Step 6: Evidence collection & transactional rollback (Phase 5).
	// Aggregate stdout/exit codes/test logs into EvidenceVector and derive terminal state.
	// If verification fails, trigger CheckpointCoordinator.Rollback before returning Failed.
	requiredLevel := evidence.L3_UnitTests
	if intent.Unit.Verification.RequiredLevel > domain.LevelNone {
		requiredLevel = evidence.EvidenceLevel(intent.Unit.Verification.RequiredLevel)
	}
	vec := BuildEvidenceVector(result, "", 0, "", intent.HumanApproved)
	evState := e.EvaluateEvidenceAndRollback(trackedCtx, vec, requiredLevel, intent.CheckpointID, domain.RollbackLocal)
	if evState == evidence.VerdictFailed {
		obs := domain.ExecutionObservation{
			UnitID:          intent.Unit.UnitID,
			ProposalOutcome: domain.ProposalRejected,
			MutationResult:  &result,
			OutputStatus:    domain.OutputComplete,
			BudgetUsage:     e.tracker.Usage(),
			DependencyFresh: domain.FreshnessValid,
			FailureSignals:  []domain.FailureSignal{{Class: domain.FailureTest, Message: "evidence verification failed: required level not met"}},
		}
		return obs, fmt.Errorf("evidence: verification failed at required level %s", requiredLevel.String())
	}

	// Step 7: Record ExecutionObservation and advance state version via OCCGate (already advanced in step 1).
	obs := domain.ExecutionObservation{
		UnitID:          intent.Unit.UnitID,
		ProposalOutcome: domain.ProposalAccepted,
		MutationResult:  &result,
		OutputStatus:    domain.OutputComplete,
		BudgetUsage:     e.tracker.Usage(),
		DependencyFresh: domain.FreshnessValid,
	}

	return obs, nil
}
