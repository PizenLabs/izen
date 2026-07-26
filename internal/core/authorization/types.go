package authorization

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/workflow"
)

type AuthorizationID string

func NewAuthorizationID() AuthorizationID {
	return AuthorizationID("authz_" + generateULID())
}

type MutationProposal struct {
	IntentID           artifact.ArtifactID
	PlanID             artifact.ArtifactID
	PatchID            artifact.ArtifactID
	TargetFiles        []string
	Diffs              map[string]string
	RequiredCaps       CapabilityFlags
	EstimatedDelta     budget.BudgetDelta
	SourceSnapshotHash string
	CreatedAt          time.Time
}

func (p *MutationProposal) Hash() string {
	h := sha256.New()
	h.Write([]byte(p.IntentID))
	h.Write([]byte(p.PlanID))
	for _, f := range p.TargetFiles {
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

type MutationAuthorization struct {
	ID            AuthorizationID
	ProposalHash  string
	CheckpointRef workflow.CheckpointRef
	ExpiresAt     time.Time
	SingleUse     bool
	IssuedAt      time.Time
}

func (a *MutationAuthorization) IsExpired() bool {
	return !a.ExpiresAt.IsZero() && time.Now().After(a.ExpiresAt)
}

type DeniedStep int

const (
	StepWorkflowState DeniedStep = iota + 1
	StepArtifactLifecycle
	StepArtifactApproval
	StepScopeContainment
	StepDependencyFreshness
	StepCapabilityGuard
	StepBudgetSufficiency
	StepCheckpointVerification
)

func (s DeniedStep) String() string {
	switch s {
	case StepWorkflowState:
		return "workflow-state"
	case StepArtifactLifecycle:
		return "artifact-lifecycle"
	case StepArtifactApproval:
		return "artifact-approval"
	case StepScopeContainment:
		return "scope-containment"
	case StepDependencyFreshness:
		return "dependency-freshness"
	case StepCapabilityGuard:
		return "capability-guard"
	case StepBudgetSufficiency:
		return "budget-sufficiency"
	case StepCheckpointVerification:
		return "checkpoint-verification"
	default:
		return fmt.Sprintf("denied-step(%d)", int(s))
	}
}

type AuthorizationDenied struct {
	Step    DeniedStep
	Message string
}

func (e *AuthorizationDenied) Error() string {
	return fmt.Sprintf("authorization: %s: %s", e.Step, e.Message)
}

type CapabilityFlags int

const (
	CapFlagWrite CapabilityFlags = 1 << iota
	CapFlagPatch
	CapFlagExecute
	CapFlagTest
	CapFlagCheckpoint
	CapFlagRollback
)

const ulidEncoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func generateULID() string {
	now := uint64(time.Now().UnixMilli())
	var rnd [10]byte
	_, _ = rand.Read(rnd[:])
	r0 := (uint64(rnd[0]) << 8) | uint64(rnd[1])
	hi := (now << 16) | r0
	lo := binary.BigEndian.Uint64(rnd[2:10])
	var dst [26]byte
	for i := range dst {
		idx := byte(hi >> 59)
		dst[i] = ulidEncoding[idx]
		hi = (hi << 5) | (lo >> 59)
		lo <<= 5
	}
	return string(dst[:])
}
