package authorization

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
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
	// CandidateID is the MutationCandidate identity this token was issued FOR,
	// when the caller knows it. It is the lineage binding that closes the last
	// gap in
	//
	//	ComputationID → ArtifactCandidate → MutationProposal → Authorization → Mutation
	//
	// Without it, an authorization is a bare PERMISSION: it names a target set
	// and nothing else, so an approval obtained for one candidate would also read
	// as an approval for a different (or superseded) one. With it, the mutation
	// boundary refuses a token whose CandidateID is not the candidate being
	// applied, so a human gate opened for computation A can never carry
	// computation B's bytes into the workspace.
	//
	// Empty means "unbound": the caller had no candidate identity to bind (a
	// direct build execution), and the historical behaviour is preserved.
	CandidateID string
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
	StepPolicy
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
	case StepPolicy:
		return "policy"
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

// AuthorizationCandidateMismatch is returned by a mutation boundary that was
// handed an authorization bound to a DIFFERENT candidate than the one being
// applied. It is a lineage failure, not a permission failure: the human gate was
// opened for one artifact and a different one is being written.
var ErrAuthorizationCandidateMismatch = errors.New("authorization: candidate identity mismatch")

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
