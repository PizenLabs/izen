package execution

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/PizenLabs/izen/internal/engine"
)

// ── MutationSet (Phase 9A — authoritative mutation boundary) ───────────────
//
// MutationSet is the smallest aggregate that represents ONE logical user
// mutation. It owns the transaction lifetime of that mutation: begin, record,
// commit and rollback are driven through the set, and a terminal set is never
// re-entered. It is deliberately NOT an execution engine — it performs no
// mutation itself. PatchManager operates inside the boundary the set owns;
// the UI terminal handlers drive the set to a terminal state (committed on
// success, rolled_back/failed/cancelled on failure).
//
// The state vocabulary and the per-target outcome vocabulary reuse the
// existing execution.MutationOutcome / MutationEvidence records — no second
// mutation taxonomy is introduced.

// MutationState is the lifecycle state of one MutationSet.
type MutationState string

// MutationSet lifecycle states.
const (
	// MutationPending: the boundary is open, no mutation has been recorded yet.
	MutationPending MutationState = "pending"
	// MutationApplying: an apply is executing inside the boundary.
	MutationApplying MutationState = "applying"
	// MutationVerifying: the deterministic verification gate is running.
	MutationVerifying MutationState = "verifying"
	// MutationCommitted: the mutation is terminal and durable. Rollback is
	// impossible.
	MutationCommitted MutationState = "committed"
	// MutationRolledBack: the mutation was rolled back. The workspace was
	// restored to the pre-mutation snapshots.
	MutationRolledBack MutationState = "rolled_back"
	// MutationFailed: the mutation failed and was rolled back.
	MutationFailed MutationState = "failed"
	// MutationCancelled: the mutation was cancelled and was rolled back if a
	// mutation had already occurred.
	MutationCancelled MutationState = "cancelled"
)

// Terminal reports whether the state is terminal. A terminal set is never
// re-entered: Commit/Rollback become no-ops and Record refuses new targets.
func (s MutationState) Terminal() bool {
	switch s {
	case MutationCommitted, MutationRolledBack, MutationFailed, MutationCancelled:
		return true
	}
	return false
}

// ErrMutationSetTerminal is returned when Commit is invoked on a terminal set
// (e.g. a double commit). Rollback on a terminal set is a safe no-op.
var ErrMutationSetTerminal = errors.New("mutation set is terminal")

// msnCounter produces deterministic, monotonic MutationSet IDs.
var msnCounter atomic.Uint64

// MutationSet is the single logical user-mutation boundary.
type MutationSet struct {
	// ID is the unique identity of this mutation boundary.
	ID string
	// Targets are the mutation target paths, deduplicated, in first-recorded
	// order. A target may only enter via Record (the apply path).
	Targets []string
	// Outcomes are the per-target semantic mutation outcomes recorded by the
	// apply boundary. They use the existing MutationEvidence vocabulary.
	Outcomes []MutationEvidence
	// Transaction is the snapshot-record boundary this set owns. It is never
	// shared with another set.
	Transaction *engine.Transaction
	// State is the explicit lifecycle state of the set.
	State MutationState
}

// NewMutationSet opens a fresh, empty mutation boundary owning a fresh
// transaction.
func NewMutationSet() *MutationSet {
	n := msnCounter.Add(1)
	return &MutationSet{
		ID:          fmt.Sprintf("ms-%d", n),
		Transaction: engine.NewTransaction(),
		State:       MutationPending,
	}
}

// NewMutationSetWithID opens a fresh boundary with an explicit ID (test seam
// for deterministic assertions).
func NewMutationSetWithID(id string) *MutationSet {
	return &MutationSet{
		ID:          id,
		Transaction: engine.NewTransaction(),
		State:       MutationPending,
	}
}

// HasTarget reports whether path is already a member of the set.
func (ms *MutationSet) HasTarget(path string) bool {
	if ms == nil {
		return false
	}
	for _, t := range ms.Targets {
		if t == path {
			return true
		}
	}
	return false
}

// AddTarget records a mutation target into the set. A target already present
// is not duplicated. It is a no-op on a terminal set (a terminal boundary
// accepts no new mutations).
func (ms *MutationSet) AddTarget(path string) {
	if ms == nil || ms.State.Terminal() || path == "" {
		return
	}
	if ms.HasTarget(path) {
		return
	}
	ms.Targets = append(ms.Targets, path)
}

// AddOutcome appends one per-target semantic mutation outcome.
func (ms *MutationSet) AddOutcome(ev MutationEvidence) {
	if ms == nil || ms.State.Terminal() {
		return
	}
	ms.Outcomes = append(ms.Outcomes, ev)
}

// Record snapshots the pre-mutation state of a target path into the owned
// transaction and registers the path as a mutation target. It is the only way
// a file enters the boundary. A terminal set refuses new records.
func (ms *MutationSet) Record(path string) error {
	if ms == nil {
		return nil
	}
	if ms.State.Terminal() {
		return fmt.Errorf("%w: cannot record %s", ErrMutationSetTerminal, path)
	}
	if ms.Transaction == nil {
		return nil
	}
	ms.AddTarget(path)
	return ms.Transaction.Record(path)
}

// Transition advances the set's lifecycle state (pending → applying →
// verifying → terminal). Terminal states are only reached via Commit/Rollback.
func (ms *MutationSet) Transition(s MutationState) {
	if ms == nil || ms.State.Terminal() {
		return
	}
	ms.State = s
}

// Terminal reports whether the set reached a terminal state.
func (ms *MutationSet) Terminal() bool {
	return ms != nil && ms.State.Terminal()
}

// Committed reports whether the set was committed.
func (ms *MutationSet) Committed() bool { return ms != nil && ms.State == MutationCommitted }

// RolledBack reports whether the set was rolled back.
func (ms *MutationSet) RolledBack() bool {
	return ms != nil && ms.State == MutationRolledBack
}

// Commit terminates the set as a durable success. It is a no-op when the set
// is already terminal; a second commit reports ErrMutationSetTerminal and does
// not re-run. After commit, Rollback is impossible.
func (ms *MutationSet) Commit() error {
	if ms == nil {
		return nil
	}
	if ms.State.Terminal() {
		return ErrMutationSetTerminal
	}
	ms.State = MutationCommitted
	if ms.Transaction != nil {
		ms.Transaction.Commit()
	}
	return nil
}

// Rollback terminates the set by restoring every recorded snapshot. It is a
// safe no-op on a terminal set — a committed mutation can never be rolled
// back and a rolled-back mutation is never rolled back twice.
func (ms *MutationSet) Rollback() []error {
	return ms.rollbackTo(MutationRolledBack)
}

// RollbackTo terminates the set with the given failure outcome and restores
// every recorded snapshot. It is the terminal path for apply_failed /
// verify_failed / cancelled / timeout / execution_failed outcomes.
func (ms *MutationSet) RollbackTo(s MutationState) []error {
	if !s.Terminal() {
		s = MutationRolledBack
	}
	return ms.rollbackTo(s)
}

func (ms *MutationSet) rollbackTo(s MutationState) []error {
	if ms == nil {
		return nil
	}
	if ms.State.Terminal() {
		// Committed can never roll back; already-rolled-back never rolls back
		// twice. A failed/cancelled set is terminal by construction.
		return nil
	}
	ms.State = s
	if ms.Transaction != nil {
		return ms.Transaction.Rollback()
	}
	return nil
}

// OutcomeFor returns the semantic outcome recorded for a target, or
// OutcomeNoArtifact when the set has no record for it.
func (ms *MutationSet) OutcomeFor(path string) MutationOutcome {
	if ms == nil {
		return OutcomeNoArtifact
	}
	for _, ev := range ms.Outcomes {
		if ev.File == path {
			return ev.Outcome
		}
	}
	return OutcomeNoArtifact
}
