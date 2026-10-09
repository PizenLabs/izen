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

// AuthorizationScope names the capability family a grant authorizes. It exists
// because ONE single-use token shared by two capability families (a file write
// and a shell-command verification step) is consumed by whichever guard runs
// first, starving the other. The scope makes the CONSUMPTION BOUNDARY explicit:
// the token is consumed where its documented meaning says it is.
type AuthorizationScope string

const (
	// ScopeMutationOperation: one grant authorizes ONE whole mutation operation
	// — the file write AND every verification step its apply gate runs. It is
	// consumed exactly once, when the operation reaches a terminal state, so a
	// multi-step verifier cannot starve itself on the first command.
	ScopeMutationOperation AuthorizationScope = "mutation_operation"
	// ScopeCapabilityInvocation (the zero value, and the historical behaviour)
	// authorizes ONE guarded capability invocation and is consumed by that
	// invocation's own guard.
	ScopeCapabilityInvocation AuthorizationScope = "capability_invocation"
)

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
	// Scope names the capability family this grant authorizes and therefore
	// WHERE its single-use consumption occurs. The zero value is
	// ScopeCapabilityInvocation — the historical per-invocation boundary — so
	// existing callers are unchanged. A grant minted for a whole mutation
	// operation sets ScopeMutationOperation and is consumed once, at the end of
	// that operation.
	Scope AuthorizationScope
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
	// CandidateDigest is the CONTENT fingerprint of the candidate this token was
	// issued for: a digest over the artifact bytes, the compiled diffs and the
	// target identity.
	//
	// CandidateID alone answers "is this the same computation?" — which is not
	// the same question as "is this the same CHANGE?". If the held artifact were
	// ever replaced in place under a stable identity, an authorization would
	// survive the substitution and write bytes a human never saw. Binding the
	// content makes that structurally impossible: the mutation boundary recomputes
	// the digest and refuses a token whose candidate no longer matches, which is
	// the "authorization invalidated: candidate changed" outcome.
	//
	// Empty means "not content-bound" and preserves the historical behaviour for
	// callers that hold no candidate content.
	CandidateDigest string
}

// Binds reports whether this token is bound to a specific candidate identity.
// It is the predicate the mutation boundary uses to decide whether a lineage
// comparison is meaningful at all.
func (a *MutationAuthorization) Binds() bool {
	return a != nil && a.CandidateID != ""
}

// Authorizes reports whether this token may be used to apply `candidateID`
// carrying `digest`.
//
// The identity is the primary key: a different candidate is a different
// computation. The digest is the content key: the same identity with different
// content is a changed candidate and equally unapproved. Both comparisons are
// fail-closed — a bound token that cannot be matched is refused, never ignored.
func (a *MutationAuthorization) Authorizes(candidateID, digest string) error {
	if !a.Binds() {
		return nil
	}
	if a.CandidateID != candidateID {
		return fmt.Errorf("%w: authorization %s is bound to candidate %q, refusing %q",
			ErrAuthorizationCandidateMismatch, a.ID, a.CandidateID, candidateID)
	}
	if a.CandidateDigest != "" && digest != "" && a.CandidateDigest != digest {
		return fmt.Errorf("%w: authorization %s was issued for candidate content %s but the held candidate is now %s — the authorization is invalidated and a new mutation review is required",
			ErrAuthorizationCandidateChanged, a.ID, shortDigest(a.CandidateDigest), shortDigest(digest))
	}
	return nil
}

// shortDigest renders a fingerprint compactly for a human-readable refusal.
// A full sha256 would dominate the message; the first 12 hex chars are enough to
// tell two digests apart.
func shortDigest(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}

func (a *MutationAuthorization) IsExpired() bool {
	return !a.ExpiresAt.IsZero() && time.Now().After(a.ExpiresAt)
}

// ConsumesPerInvocation reports whether a single-use grant is consumed by EACH
// guarded capability invocation. A mutation-operation grant (the whole
// apply+verify operation) is deliberately NOT consumed by an individual shell
// command: its consumption boundary is the operation, not the command. A
// non-single-use grant is never consumed at all.
func (a *MutationAuthorization) ConsumesPerInvocation() bool {
	if a == nil || !a.SingleUse {
		return false
	}
	return a.Scope != ScopeMutationOperation
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

// ErrAuthorizationCandidateChanged is returned when the authorization's candidate
// identity still matches but its CONTENT no longer does. It is a distinct failure
// from a lineage mismatch: the run is the same run, the proposal a human reviewed
// is simply not the proposal the runtime would apply. The remedy is a new
// mutation review, never a silent re-authorization.
var ErrAuthorizationCandidateChanged = errors.New("authorization: candidate changed since review")

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
