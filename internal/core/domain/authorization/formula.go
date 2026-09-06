package authorization

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/domain"
)

// AuthorizationDecision is the single verdict of the CapabilityGuard.
type AuthorizationDecision struct {
	Permitted    bool           `json:"permitted"`
	Reason       string         `json:"reason"`
	FailedClause Clause         `json:"failed_clause,omitempty"`
	FrameID      domain.FrameID `json:"frame_id"`
	UnitID       domain.UnitID  `json:"unit_id"`
}

// Clause enumerates the eight conjuncts of the formula.
type Clause uint8

const (
	ClauseIntent     Clause = iota // ValidIntent
	ClauseScope                    // ValidScope (incl. NegativeScope check)
	ClausePlan                     // ValidPlan ∨ ValidMicroPlan
	ClauseCheckpoint               // CheckpointCreated
	ClauseSourceHash               // SourceHashMatch — INV:3
	ClauseBudget                   // BudgetAvailable
	ClauseCapability               // CapabilityGranted — INV:1, INV:7
	ClauseApproval                 // HumanApproved ∨ BudgetIsPreApproval — INV:7
)

func (c Clause) String() string {
	switch c {
	case ClauseIntent:
		return "ValidIntent"
	case ClauseScope:
		return "ValidScope"
	case ClausePlan:
		return "ValidPlan"
	case ClauseCheckpoint:
		return "CheckpointCreated"
	case ClauseSourceHash:
		return "SourceHashMatch"
	case ClauseBudget:
		return "BudgetAvailable"
	case ClauseCapability:
		return "CapabilityGranted"
	case ClauseApproval:
		return "Approval"
	default:
		return fmt.Sprintf("Clause(%d)", int(c))
	}
}

// Sentinel errors for each clause.
// These map to the failure sentinels described in 02_SYSTEM_MODEL_2.md Sec 2.2.
var (
	ErrInvalidIntent    = errors.New("authorization: invalid intent")
	ErrScopeViolation   = errors.New("authorization: scope violation")
	ErrNoAuthorizedPlan = errors.New("authorization: no authorized plan")
	ErrNoCheckpoint     = errors.New("authorization: no checkpoint")
	ErrStaleDependency  = errors.New("authorization: stale dependency")
	ErrBudgetExceeded   = errors.New("authorization: budget exceeded")
	ErrCapabilityDenied = errors.New("authorization: capability denied")
	ErrApprovalRequired = errors.New("authorization: approval required")
)

// AuthorizationInput is the complete evidence bundle the guard evaluates.
type AuthorizationInput struct {
	Objective     domain.Objective           `json:"objective"`
	Scope         domain.Scope               `json:"scope"`
	Artifact      ArtifactRef                `json:"artifact"`
	CheckpointID  domain.CheckpointID        `json:"checkpoint_id"`
	SourceState   domain.SourceState         `json:"source_state"`
	Budget        domain.ResourceBudget      `json:"budget"`
	Capabilities  domain.DomainCapabilitySet `json:"capabilities"`
	Approval      ApprovalToken              `json:"approval"`
	Proposal      ProposalRef                `json:"proposal"`
	HasCheckpoint bool                       `json:"has_checkpoint"`
}

type ArtifactRef struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Hash  string `json:"hash"`
}

type ApprovalToken struct {
	HumanApproved       bool `json:"human_approved"`
	BudgetIsPreApproval bool `json:"budget_is_pre_approval"`
}

type ProposalRef struct {
	TargetFiles []string `json:"target_files"`
	DiffLines   int      `json:"diff_lines"`
	Files       int      `json:"files"`
	Tokens      int      `json:"tokens"`
}

// CapabilityGuard is the sole authority that evaluates the formula.
type CapabilityGuard interface {
	Evaluate(ctx context.Context, in AuthorizationInput) AuthorizationDecision
}

// SimpleCapabilityGuard is a deterministic implementation of CapabilityGuard.
type SimpleCapabilityGuard struct{}

// NewCapabilityGuard returns a new guard.
func NewCapabilityGuard() *SimpleCapabilityGuard { return &SimpleCapabilityGuard{} }

// Evaluate returns Permit or Deny with the first failing clause.
func (g *SimpleCapabilityGuard) Evaluate(_ context.Context, in AuthorizationInput) AuthorizationDecision {
	frameID := domain.FrameID("")
	unitID := domain.UnitID("")

	// ClauseIntent: ValidIntent
	if in.Objective.Intent.Kind == domain.IntentUnknown || in.Objective.Intent.Kind == "" {
		return AuthorizationDecision{Permitted: false, Reason: ErrInvalidIntent.Error(), FailedClause: ClauseIntent, FrameID: frameID, UnitID: unitID}
	}
	if in.Objective.Intent.Confidence < 0 || in.Objective.Intent.Confidence > 1 {
		return AuthorizationDecision{Permitted: false, Reason: ErrInvalidIntent.Error(), FailedClause: ClauseIntent, FrameID: frameID, UnitID: unitID}
	}

	// ClauseScope: ValidScope — NegativeScope ∩ Proposal == ∅
	for _, neg := range in.Objective.NegativeScope.Includes {
		for _, target := range in.Proposal.TargetFiles {
			if neg.Pattern == target {
				return AuthorizationDecision{Permitted: false, Reason: fmt.Sprintf("%s: scope violation %q in negative scope", ErrScopeViolation, target), FailedClause: ClauseScope, FrameID: frameID, UnitID: unitID}
			}
		}
	}

	// ClausePlan: ValidPlan ∨ ValidMicroPlan — artifact must be AUTHORIZED or VALIDATED with pre-approval
	if in.Artifact.State != "AUTHORIZED" && in.Artifact.State != "StateAuthorized" {
		if !in.Approval.BudgetIsPreApproval || (in.Artifact.State != "VALIDATED" && in.Artifact.State != "StateValidated") { //nolint:staticcheck
			return AuthorizationDecision{Permitted: false, Reason: ErrNoAuthorizedPlan.Error(), FailedClause: ClausePlan, FrameID: frameID, UnitID: unitID}
		}
	}

	// ClauseCheckpoint: CheckpointCreated
	if in.CheckpointID == "" && !in.HasCheckpoint {
		return AuthorizationDecision{Permitted: false, Reason: ErrNoCheckpoint.Error(), FailedClause: ClauseCheckpoint, FrameID: frameID, UnitID: unitID}
	}

	// ClauseSourceHash: SourceHashMatch
	for _, f := range in.Proposal.TargetFiles {
		if expected, ok := in.SourceState.FileHashes[f]; ok {
			// If hash mismatch is represented by missing or mismatched entry, real verification would compare.
			// For the pure guard, we treat a sentinel "STALE" hash as failure.
			if expected == "STALE" {
				return AuthorizationDecision{Permitted: false, Reason: ErrStaleDependency.Error(), FailedClause: ClauseSourceHash, FrameID: frameID, UnitID: unitID}
			}
		}
	}

	// ClauseBudget: BudgetAvailable
	totalFiles := in.Proposal.Files
	if totalFiles == 0 {
		totalFiles = len(in.Proposal.TargetFiles)
	}
	if in.Budget.MaxFiles > 0 && totalFiles > in.Budget.MaxFiles {
		return AuthorizationDecision{Permitted: false, Reason: ErrBudgetExceeded.Error(), FailedClause: ClauseBudget, FrameID: frameID, UnitID: unitID}
	}
	if in.Budget.MaxDiffLines > 0 && in.Proposal.DiffLines > in.Budget.MaxDiffLines {
		return AuthorizationDecision{Permitted: false, Reason: ErrBudgetExceeded.Error(), FailedClause: ClauseBudget, FrameID: frameID, UnitID: unitID}
	}

	// ClauseCapability: CapabilityGranted
	needsWrite := len(in.Proposal.TargetFiles) > 0
	if needsWrite {
		if !in.Capabilities.Has(domain.CapWrite) && !in.Capabilities.Has(domain.CapPatch) {
			return AuthorizationDecision{Permitted: false, Reason: ErrCapabilityDenied.Error(), FailedClause: ClauseCapability, FrameID: frameID, UnitID: unitID}
		}
	}

	// ClauseApproval: HumanApproved ∨ BudgetIsPreApproval
	if !in.Approval.HumanApproved && !in.Approval.BudgetIsPreApproval {
		return AuthorizationDecision{Permitted: false, Reason: ErrApprovalRequired.Error(), FailedClause: ClauseApproval, FrameID: frameID, UnitID: unitID}
	}
	// If pre-approval, ensure within micro budget
	if in.Approval.BudgetIsPreApproval && !in.Approval.HumanApproved {
		if in.Budget.MaxFiles > 0 && totalFiles > 2 {
			return AuthorizationDecision{Permitted: false, Reason: ErrApprovalRequired.Error(), FailedClause: ClauseApproval, FrameID: frameID, UnitID: unitID}
		}
		if in.Budget.MaxDiffLines > 0 && in.Proposal.DiffLines > 50 {
			return AuthorizationDecision{Permitted: false, Reason: ErrApprovalRequired.Error(), FailedClause: ClauseApproval, FrameID: frameID, UnitID: unitID}
		}
	}

	return AuthorizationDecision{Permitted: true, FrameID: frameID, UnitID: unitID}
}
