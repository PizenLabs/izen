package executor

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/authorization"
	"github.com/PizenLabs/izen/internal/core/domain/checkpoint"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/core/domain/occ"
	"github.com/PizenLabs/izen/internal/runtime/scope"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// RuntimeExecutor is the single Control Plane coordinator.
// It enforces the 6-clause formula binding: every mutation must invoke
// CapabilityGuard.Evaluate before dispatching to Substrate.
//
// Execution-time confinement: when bound to a workspace-root FD handle
// (WithScopeRoot), every target is verified at USE time against the open
// descriptor after authorization and before Substrate dispatch. A symlink
// swapped in between resolution and execution that points outside the
// root fails closed with ErrWorkspaceEscape and reaches neither
// checkpoint mutation nor Substrate.
type RuntimeExecutor struct {
	guard     authorization.CapabilityGuard
	substrate *substrate.Substrate
	occ       *occ.OCCGate
	ckpt      checkpoint.CheckpointCoordinator
	version   occ.StateVersion
	tracker   *BudgetTracker
	// scopeRoot optionally anchors execution-time confinement to an open
	// root FD for the executor lifetime.
	scopeRoot *scope.Root
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

// WithScopeRoot anchors execution-time confinement to an open
// workspace-root FD handle. The caller retains ownership (Close); the
// executor never closes a handle it did not open. When bound, every
// target is verified at USE time after authorization and before
// Substrate dispatch; escapes fail closed with ErrWorkspaceEscape.
func (e *RuntimeExecutor) WithScopeRoot(r *scope.Root) *RuntimeExecutor {
	if e == nil {
		return e
	}
	e.scopeRoot = r
	return e
}

// verifyTargetsAtUse enforces execution-time confinement for every
// mutation target against the open root FD. It runs AFTER
// guard.Evaluate and BEFORE any checkpoint/substrate side effect so a
// TOCTOU symlink swap fails closed with zero filesystem mutations.
// A nil scope falls back to the substrate root string with a
// short-lived handle; a fully unrooted executor skips verification for
// backward compatibility.
func (e *RuntimeExecutor) verifyTargetsAtUse(targets []string) error {
	root := e.scopeRoot
	if root != nil {
		for _, t := range targets {
			if err := root.Verify(t); err != nil {
				return err
			}
		}
		return nil
	}
	var rootPath string
	if e != nil && e.substrate != nil {
		rootPath = e.substrate.Root()
	}
	if rootPath == "" {
		return nil
	}
	r, err := scope.Open(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for _, t := range targets {
		if err := r.Verify(t); err != nil {
			return err
		}
	}
	return nil
}

// Substrate returns the substrate.
func (e *RuntimeExecutor) Substrate() *substrate.Substrate { return e.substrate }

// Execute implements the 6-step pipeline with a synchronous deterministic
// fast-path gate at Step 0:
//  0. Fast-path static gate (capabilities → targets → references → preflight),
//     bounded O(N) on target depth, early exit before any checkpoint/substrate.
//  1. Validate OCCGate sequence version
//  2. Construct AuthorizationInput
//  3. Call guard.Evaluate (sole 8-clause authority, before substrate.ExecuteUnit)
//  4. Trigger checkpoint if needed
//  5. Delegate to substrate.ExecuteUnit
//  6. Record observation and advance version
func (e *RuntimeExecutor) Execute(ctx context.Context, intent domain.ExecutionIntent) (domain.ExecutionObservation, error) {
	// Step -1: Execution-time confinement probe. Verify every target at
	// USE time against the workspace-root FD handle before any other
	// work (including the static fast path). A symlink swapped in
	// between planning-time resolution and execution fails closed here
	// with ErrWorkspaceEscape and zero filesystem mutations. This probe
	// is read-only verification: it grants no authority and bypasses no
	// gate — the full fast-path and 8-clause authorization still run
	// below, and the Step 3.5 gate re-verifies after authorization to
	// close the authorize→dispatch window.
	if targets := intent.Unit.ObjectiveSlice.Targets; len(targets) > 0 {
		if verr := e.verifyTargetsAtUse(targets); verr != nil {
			obs := domain.ExecutionObservation{
				UnitID:          intent.Unit.UnitID,
				ProposalOutcome: domain.ProposalRejected,
				FailureSignals:  []domain.FailureSignal{{Class: domain.FailureScope, Message: verr.Error()}},
			}
			return obs, verr
		}
	}

	// Step 0: Synchronous deterministic fast-path gate. Drops unauthorized or
	// unsafe static requests before OCC, checkpoint, or substrate work. The
	// final verdict still rests with guard.Evaluate at Step 3 (StageAuthorize);
	// this stage only denies early on static evidence. LLM-derived bytes are
	// treated as untrusted: any authority override directive denies with
	// ErrCapabilityDenied at the execution boundary.
	if fastRes := e.fastPathPreflight(ctx, intent); !fastRes.Permitted {
		obs := domain.ExecutionObservation{
			UnitID:          intent.Unit.UnitID,
			ProposalOutcome: domain.ProposalRejected,
			FailureSignals:  []domain.FailureSignal{{Class: domain.FailureUnknown, Message: fastRes.Reason}},
		}
		return obs, fmt.Errorf("%w: %s", fastPathClauseErr(fastRes.FailedClause, fastRes.Reason), fastRes.Reason)
	}

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

	// Step 3.5: Execution-time confinement. Re-verify every target at
	// USE time against the workspace-root FD handle, after authorization
	// and before any checkpoint/substrate side effect. A symlink swapped
	// in between resolution and execution that escapes the root fails
	// closed here with ErrWorkspaceEscape and zero filesystem mutations.
	if targets := intent.Unit.ObjectiveSlice.Targets; len(targets) > 0 {
		if verr := e.verifyTargetsAtUse(targets); verr != nil {
			obs := domain.ExecutionObservation{
				UnitID:          intent.Unit.UnitID,
				ProposalOutcome: domain.ProposalRejected,
				FailureSignals:  []domain.FailureSignal{{Class: domain.FailureScope, Message: verr.Error()}},
			}
			return obs, verr
		}
	}

	// Step 3.6: Side-effect authorization matrix. Reversible filesystem
	// mutations are governed by capability tokens (already bound by the
	// 8-clause verdict above); irreversible actions — external process
	// execution requested through the unit capability boundary —
	// unconditionally require explicit human approval/token verification
	// on top of the capability token. Modes grant nothing here: this
	// decision reads only the explicit grant and the approval token.
	if intent.Unit.CapabilityBoundary.Has(domain.CapExecRestricted) && !intent.HumanApproved {
		obs := domain.ExecutionObservation{
			UnitID:          intent.Unit.UnitID,
			ProposalOutcome: domain.ProposalRejected,
			FailureSignals:  []domain.FailureSignal{{Class: domain.FailureUnknown, Message: authorization.ErrApprovalRequired.Error()}},
		}
		return obs, fmt.Errorf("%w: irreversible shell execution requires explicit human approval", authorization.ErrApprovalRequired)
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
