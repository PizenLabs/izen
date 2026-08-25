// Evidence-gated state projection (Phase 2 P2).
//
// The runtime seals an immutable ExecutionEvidence at every execution
// termination. This file is the AUTHORITATIVE projection gate over that
// evidence: downstream consumers (UI terminal state, queue/audit reducers)
// derive their truth exclusively through ProjectEvidence / EvidenceLedger.
//
// The gate enforces the partial-truth elimination rules mechanically:
//
//   - ONLY a COMMITTED outcome with untainted mutations projects as an
//     authoritative success. There is no partial success.
//   - FAILED / ABORTED_OCC / CANCELLED outcomes strictly BLOCK authoritative
//     projection — a blocked projection can never be rendered or queued as a
//     completed state, no matter what intermediate lifecycle events showed.
//   - Tainted mutation sets (partial applied-then-rolled-back writes,
//     unverified applies) block success even when some sibling file changed:
//     intermediate results are never treated as truth.
package presentation

import (
	"github.com/PizenLabs/izen/internal/execution"
)

// EvidenceAuthority is the verdict of the projection gate for one piece of
// execution evidence.
type EvidenceAuthority string

// Canonical authority verdicts.
const (
	// AuthorityGranted: the evidence is COMMITTED and untainted — downstream
	// projectors may derive an authoritative success state from it.
	AuthorityGranted EvidenceAuthority = "granted"
	// AuthorityBlocked: the evidence is NOT committed success truth (failed,
	// aborted, cancelled, or tainted). Projectors MUST NOT derive a success
	// or completion state from it.
	AuthorityBlocked EvidenceAuthority = "blocked"
)

// EvidenceProjection is the authoritative projection derived from one sealed
// ExecutionEvidence. It is a pure function of the evidence — nothing else in
// the runtime can influence it.
type EvidenceProjection struct {
	// ContractID identifies the execution intent this truth belongs to.
	ContractID execution.ContractID
	// AttemptID identifies the invocation attempt that terminated.
	AttemptID execution.AttemptID
	// ParentContractID is the causal parent of a recovery contract (""
	// otherwise).
	ParentContractID execution.ContractID
	// CausalAncestry is the frozen recovery chain (root → parent).
	CausalAncestry []execution.ContractID
	// ContextDigest is the Phase 1 sealed context digest the attempt ran
	// under.
	ContextDigest string
	// Outcome is the canonical terminal outcome.
	Outcome execution.ExecutionOutcome
	// Authority is the gate verdict.
	Authority EvidenceAuthority
	// Mutations is the immutable mutation-set summary of the attempt.
	Mutations execution.MutationSetSummary
	// BlockReason explains a Blocked verdict deterministically ("" when
	// granted).
	BlockReason string
}

// Granted reports whether the projection carries authoritative success truth.
func (p EvidenceProjection) Granted() bool { return p.Authority == AuthorityGranted }

// ProjectEvidence reduces ONE sealed ExecutionEvidence into its authoritative
// projection verdict. This is the SOLE sanctioned path from execution
// termination to downstream state: non-committed and tainted evidence strictly
// blocks.
func ProjectEvidence(ev *execution.ExecutionEvidence) EvidenceProjection {
	p := EvidenceProjection{
		ContractID:       ev.ContractID(),
		AttemptID:        ev.AttemptID(),
		ParentContractID: ev.ParentContractID(),
		CausalAncestry:   ev.CausalAncestry(),
		ContextDigest:    ev.ContextDigest(),
		Outcome:          ev.Outcome(),
		Mutations:        ev.Mutations(),
	}
	switch {
	case !ev.Outcome().Terminal():
		p.Authority = AuthorityBlocked
		p.BlockReason = "evidence outcome is not a terminal execution state"
	case !ev.Outcome().Committed():
		p.Authority = AuthorityBlocked
		switch ev.Outcome() {
		case execution.EvidenceCancelled:
			p.BlockReason = "execution was cancelled before committing"
		case execution.EvidenceAbortedOCC:
			p.BlockReason = "execution aborted on an optimistic-concurrency conflict"
		default:
			p.BlockReason = "execution failed; failure must never project as success"
		}
	case ev.Mutations().Tainted:
		p.Authority = AuthorityBlocked
		p.BlockReason = "committed outcome carries tainted partial mutations; intermediate state is not truth"
	default:
		p.Authority = AuthorityGranted
	}
	return p
}

// EvidenceLedger is the append-only queue-side projector of execution
// evidence. It accumulates sealed evidence per contract and exposes ONLY
// gate-checked projections: a contract's authoritative state can be read back
// solely through AuthoritativeFor, which refuses non-granted evidence.
type EvidenceLedger struct {
	byContract map[execution.ContractID]*execution.ExecutionEvidence
	order      []execution.ContractID
}

// NewEvidenceLedger returns an empty append-only evidence ledger.
func NewEvidenceLedger() *EvidenceLedger {
	return &EvidenceLedger{byContract: make(map[execution.ContractID]*execution.ExecutionEvidence)}
}

// Record appends one sealed evidence record. Records are immutable once
// stored; a later attempt under the same contract supersedes (never rewrites)
// the earlier record's attempt identity — history remains addressable via
// AttemptID on each stored record.
func (l *EvidenceLedger) Record(ev *execution.ExecutionEvidence) {
	if l == nil || ev == nil || ev.ContractID().IsZero() {
		return
	}
	if _, exists := l.byContract[ev.ContractID()]; !exists {
		l.order = append(l.order, ev.ContractID())
	}
	l.byContract[ev.ContractID()] = ev
}

// Latest returns the most recent evidence recorded for the contract.
func (l *EvidenceLedger) Latest(id execution.ContractID) *execution.ExecutionEvidence {
	if l == nil {
		return nil
	}
	return l.byContract[id]
}

// AuthoritativeFor returns the authoritative projection for a contract's most
// recent attempt. The boolean is false when no evidence exists OR the gate
// blocks the evidence — callers can never obtain a success projection for
// failed, cancelled, aborted or tainted executions.
func (l *EvidenceLedger) AuthoritativeFor(id execution.ContractID) (EvidenceProjection, bool) {
	ev := l.Latest(id)
	if ev == nil {
		return EvidenceProjection{}, false
	}
	p := ProjectEvidence(ev)
	return p, p.Granted()
}
