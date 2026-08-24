package execution

import (
	"strings"
	"time"
)

// ── ExecutionEvidence (Phase 2 P2 — authoritative execution record) ─────────
//
// ExecutionEvidence is the IMMUTABLE terminal record of one execution
// attempt. It is produced ONLY by the runtime (RuntimeExecutor) when an
// execution reaches termination, and it is the SOLE authoritative artifact
// downstream projectors (UI state, queue/audit consumers) may consume to
// derive terminal execution truth.
//
// Every evidence record answers exactly: WHICH contract attempt terminated,
// under WHICH causal lineage, against WHICH sealed context, with WHICH
// outcome, mutating WHAT, and WHEN. Non-COMMITTED outcomes explicitly convey
// failure; partial mutations are flagged TAINTED so no projector can ever
// mistake intermediate state for truth.

// ExecutionOutcome is the canonical terminal outcome vocabulary of one
// execution attempt. It is deliberately coarse and total: every execution
// terminates in exactly one of these states.
type ExecutionOutcome string

// Canonical execution outcomes.
const (
	// EvidenceCommitted is the ONLY success outcome: the execution reached its
	// committed terminal truth (a durable mutation was committed, or a
	// read-only/deterministic execution completed its contract without any
	// mutation surface).
	EvidenceCommitted ExecutionOutcome = "COMMITTED"
	// EvidenceFailed is the explicit failure outcome. There is no partial
	// success: any apply/verify/artifact/model failure terminates here, with
	// partial mutations flagged tainted on the mutation summary.
	EvidenceFailed ExecutionOutcome = "FAILED"
	// EvidenceAbortedOCC is the optimistic-concurrency abort outcome. It is
	// RESERVED for Phase 3 (workspace source-hash OCC verification); the
	// current noopSourceHashVerifier baseline never produces it.
	EvidenceAbortedOCC ExecutionOutcome = "ABORTED_OCC"
	// EvidenceCancelled is a clean terminal cancellation (user cancel or human
	// rejection at the approval gate). Nothing was committed.
	EvidenceCancelled ExecutionOutcome = "CANCELLED"
)

// Committed reports whether the outcome is the authoritative success.
func (o ExecutionOutcome) Committed() bool { return o == EvidenceCommitted }

// Terminal reports whether the outcome is a terminal execution state. All
// evidence outcomes are terminal by construction (evidence exists only at
// termination); the method keeps the vocabulary self-describing for
// projections that switch over it.
func (o ExecutionOutcome) Terminal() bool {
	switch o {
	case EvidenceCommitted, EvidenceFailed, EvidenceAbortedOCC, EvidenceCancelled:
		return true
	default:
		return false
	}
}

// String returns the raw outcome label.
func (o ExecutionOutcome) String() string { return string(o) }

// MutationSetSummary is the immutable mutation accounting inside one piece of
// execution evidence. It summarizes the MutationSet boundary that owned the
// transaction so downstream consumers never need the live set object.
type MutationSetSummary struct {
	// TransactionID is the MutationSet transaction identity ("" when no
	// mutation boundary was opened).
	TransactionID string `json:"transaction_id,omitempty"`
	// Targets lists the declared mutation targets of the attempt.
	Targets []string `json:"targets,omitempty"`
	// FilesMutated counts targets whose post-apply filesystem content actually
	// differs (apply executed AND changed). A rolled-back write is NOT counted:
	// the summary reflects durable workspace truth only.
	FilesMutated int `json:"files_mutated"`
	// AppliedExecuted is true when the apply stage ran at all.
	ApplyExecuted bool `json:"apply_executed"`
	// Tainted is true when the attempt left PARTIAL mutations that are not
	// durably committed: writes that were applied and then rolled back, or an
	// unverified apply. Tainted evidence can NEVER project as success.
	Tainted bool `json:"tainted"`
}

// ExecutionEvidence is the immutable authoritative terminal record of one
// execution attempt. Construction seals every field; accessors are read-only
// and defensive copies are returned for slice-valued members. There is
// intentionally NO setter and NO unsealing path: evidence is append-only
// truth, emitted once at termination.
type ExecutionEvidence struct {
	contractID    ContractID
	attempt       AttemptID
	parentID      ContractID
	ancestry      []ContractID // root → … → parent; frozen copy
	contextDigest string
	outcome       ExecutionOutcome
	mutations     MutationSetSummary
	startedAt     time.Time
	finishedAt    time.Time
}

// ContractID returns the identity of the contract this attempt belongs to.
func (e *ExecutionEvidence) ContractID() ContractID {
	if e == nil {
		return ""
	}
	return e.contractID
}

// AttemptID returns the 1-indexed invocation attempt under the contract.
func (e *ExecutionEvidence) AttemptID() AttemptID {
	if e == nil {
		return ZeroAttempt
	}
	return e.attempt
}

// ParentContractID returns the causal parent of a recovery contract (""
// otherwise).
func (e *ExecutionEvidence) ParentContractID() ContractID {
	if e == nil {
		return ""
	}
	return e.parentID
}

// CausalAncestry returns the frozen ancestor chain (root → parent, excluding
// this contract). The returned slice is a copy.
func (e *ExecutionEvidence) CausalAncestry() []ContractID {
	if e == nil {
		return nil
	}
	out := make([]ContractID, len(e.ancestry))
	copy(out, e.ancestry)
	return out
}

// ContextDigest returns the Phase 1 sealed context digest the attempt ran
// under.
func (e *ExecutionEvidence) ContextDigest() string {
	if e == nil {
		return ""
	}
	return e.contextDigest
}

// Outcome returns the canonical terminal outcome.
func (e *ExecutionEvidence) Outcome() ExecutionOutcome {
	if e == nil {
		return ""
	}
	return e.outcome
}

// Mutations returns the immutable mutation-set summary.
func (e *ExecutionEvidence) Mutations() MutationSetSummary {
	if e == nil {
		return MutationSetSummary{}
	}
	out := e.mutations
	out.Targets = append([]string(nil), e.mutations.Targets...)
	return out
}

// StartedAt / FinishedAt return the precise wall-clock window of the attempt.
func (e *ExecutionEvidence) StartedAt() time.Time {
	if e == nil {
		return time.Time{}
	}
	return e.startedAt
}

func (e *ExecutionEvidence) FinishedAt() time.Time {
	if e == nil {
		return time.Time{}
	}
	return e.finishedAt
}

// Authoritative reports whether this evidence may drive an authoritative
// success projection: ONLY a COMMITTED outcome with untainted mutations.
// Failed, aborted, cancelled and tainted evidence strictly block projection.
func (e *ExecutionEvidence) Authoritative() bool {
	return e != nil && e.outcome.Committed() && !e.mutations.Tainted
}

// SealEvidenceScalars is the complete scalar state of one sealed evidence
// record: exactly what an audit sink persists and what SealFromScalars
// accepts to reconstruct a record. It exists so downstream audit/replay
// consumers can rebuild evidence from persisted facts WITHOUT being able to
// forge new runtime truth — the runtime itself never round-trips through it.
type SealEvidenceScalars struct {
	ContractID    ContractID
	AttemptID     AttemptID
	Parent        ContractID
	Ancestry      []string
	ContextDigest string
	Outcome       string
	Mutations     MutationSetSummary
	StartedAt     time.Time
	FinishedAt    time.Time
}

// SealFromScalars deterministically reconstructs an immutable evidence record
// from its persisted scalar state (audit/replay path). The result carries the
// same immutability guarantees as runtime-sealed evidence; the outcome must be
// a member of the canonical vocabulary or the seal fails closed.
func SealFromScalars(s SealEvidenceScalars) *ExecutionEvidence {
	outcome := ExecutionOutcome(strings.ToUpper(strings.TrimSpace(s.Outcome)))
	if !outcome.Terminal() {
		return nil
	}
	e := &ExecutionEvidence{
		contractID:    s.ContractID,
		attempt:       s.AttemptID,
		parentID:      s.Parent,
		contextDigest: s.ContextDigest,
		outcome:       outcome,
		mutations:     s.Mutations,
		startedAt:     s.StartedAt,
		finishedAt:    s.FinishedAt,
	}
	for _, id := range s.Ancestry {
		if strings.TrimSpace(id) != "" {
			e.ancestry = append(e.ancestry, ContractID(id))
		}
	}
	e.mutations.Targets = append([]string(nil), s.Mutations.Targets...)
	return e
}

// sealEvidence constructs an immutable evidence record from runtime facts.
// Slice inputs are deep-copied so later caller-side mutation cannot rewrite
// history. This constructor is unexported ON PURPOSE: evidence is born only
// inside the runtime's terminal paths (finalizeResult), never assembled by
// callers.
func sealEvidence(
	contractID ContractID,
	attempt AttemptID,
	contract *ExecutionContract,
	contextDigest string,
	outcome ExecutionOutcome,
	mutations MutationSetSummary,
	startedAt, finishedAt time.Time,
) *ExecutionEvidence {
	e := &ExecutionEvidence{
		contractID:    contractID,
		attempt:       attempt,
		contextDigest: contextDigest,
		outcome:       outcome,
		mutations:     mutations,
		startedAt:     startedAt,
		finishedAt:    finishedAt,
	}
	if contract != nil {
		e.parentID = contract.ParentID()
		e.ancestry = contract.CausalAncestry()
	}
	e.mutations.Targets = append([]string(nil), mutations.Targets...)
	return e
}

// evidenceOutcomeFor maps a canonical MutationOutcome plus the request-level
// error onto the coarse terminal vocabulary. The mapping is total and
// fail-closed: anything not provably committed maps to FAILED, and anything
// cancelled/rejected maps to CANCELLED. ABORTED_OCC is reserved for Phase 3
// and never derived here.
func evidenceOutcomeFor(outcome MutationOutcome, execErr error) ExecutionOutcome {
	switch outcome {
	case OutcomeChanged, OutcomeCreated, OutcomeNoChange, OutcomeCompleted:
		if execErr != nil {
			// A result that carries a terminal error is never a soft success.
			return EvidenceFailed
		}
		return EvidenceCommitted
	case OutcomeNoArtifact:
		// A deterministic zero-mutation completion commits its (empty)
		// contract truthfully; a no-artifact FAILURE carries the error.
		if execErr != nil {
			return EvidenceFailed
		}
		return EvidenceCommitted
	case OutcomeCancelled, OutcomeRejected:
		return EvidenceCancelled
	default:
		return EvidenceFailed
	}
}

// summarizeMutationSet folds the per-file mutation evidence into the immutable
// set summary, applying the taint rules:
//
//   - FilesMutated counts only durable changes (apply executed AND filesystem
//     changed AND the aggregate outcome is committed). Rolled-back writes do
//     not count as durable truth.
//   - Tainted is raised whenever a FAILED attempt had actually applied
//     mutations (partial writes exist that are not committed truth), or an
//     apply ran but verification did not pass.
func summarizeMutationSet(txID string, targets []string, outcomes []MutationEvidence, aggregate MutationOutcome, verificationRan, verificationPassed bool) MutationSetSummary {
	s := MutationSetSummary{
		TransactionID: txID,
		Targets:       append([]string(nil), targets...),
	}
	for _, ev := range outcomes {
		if ev.ApplyExecuted {
			s.ApplyExecuted = true
		}
		durable := ev.ApplyExecutedChanged() && aggregateCommittedFamily(aggregate)
		if durable {
			s.FilesMutated++
		}
	}
	committed := aggregateCommittedFamily(aggregate)
	if !committed && s.ApplyExecuted {
		// Partial mutations existed but the attempt failed: the workspace may
		// hold rolled-back intermediates. Flag it — never a clean failure.
		s.Tainted = true
	}
	if s.ApplyExecuted && verificationRan && !verificationPassed {
		// An apply that never passed its gate is uncommitted truth even if the
		// aggregate claims otherwise. A gate that did not run (Skipped) is
		// not-applicable — never a taint signal by itself.
		s.Tainted = true
	}
	return s
}

// aggregateCommittedFamily reports whether the aggregate mutation outcome
// represents durably committed mutation truth.
func aggregateCommittedFamily(o MutationOutcome) bool {
	switch o {
	case OutcomeChanged, OutcomeCreated, OutcomeNoChange:
		return true
	default:
		return false
	}
}
