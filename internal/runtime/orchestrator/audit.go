package orchestrator

import (
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
)

// ErrAuditPersistenceFailed is the explicit error cause reported when
// .izen/audit/events.ndjson fails to flush synchronously during session
// finalization. Audit persistence failure structurally invalidates execution
// success: the terminal state MUST NOT evaluate to StateVerified /
// VerdictPass. Callers MUST test with errors.Is and MUST NOT swallow it.
var ErrAuditPersistenceFailed = errors.New("orchestrator: audit persistence failed")

// AuditFlusher is the synchronous durability seam of the audit substrate
// (internal/events/audit AuditLogger and internal/audit Logger both satisfy
// it via Flush() error). The orchestrator calls Flush once per terminal
// session finalization and binds its error into the Truthful State Transition
// evaluation.
type AuditFlusher interface {
	Flush() error
}

// WithAuditFlusher wires the synchronous audit durability seam into the
// orchestrator. A nil flusher disables audit gating (legacy/harness mode).
// Returns the orchestrator for chaining.
func (o *Orchestrator) WithAuditFlusher(flusher AuditFlusher) *Orchestrator {
	if o == nil {
		return nil
	}
	o.audit = flusher
	return o
}

// AuditFlusherOf returns the wired audit flusher, if any.
func (o *Orchestrator) AuditFlusherOf() AuditFlusher {
	if o == nil {
		return nil
	}
	return o.audit
}

// EvaluateTerminalState maps (mutation outcome, audit error) onto the Truthful
// State Transition product. It enforces the two invariants:
//
//	Audit Persistence Failure Invariant: a non-nil auditErr forces
//	Completed=false with Workflow=StateFailed and Verdict=VerdictFail
//	(EvidenceState VerdictFailed) regardless of whether the mutation
//	succeeded — success is structurally invalidated and the returned error
//	wraps ErrAuditPersistenceFailed.
//
//	Mutation Non-Rollback Isolation: the filesystem mutation is NEVER reverted
//	here; the caller keeps Committed=true on disk while the terminal marks
//	evidence integrity as compromised (Reason carries the audit cause).
func EvaluateTerminalState(committed bool, auditErr error) (evidence.TerminalState, evidence.EvidenceState) {
	if auditErr != nil {
		return evidence.TerminalState{
			Workflow:  domain.StateFailed,
			Verdict:   evidence.VerdictFail,
			Completed: false,
			Reason:    fmt.Sprintf("audit persistence failed: evidence integrity compromised: %v", auditErr),
		}, evidence.VerdictFailed
	}
	if committed {
		return evidence.TerminalState{
			Workflow:  domain.StateVerified,
			Verdict:   evidence.VerdictPass,
			Completed: true,
		}, evidence.VerdictPassed
	}
	return evidence.TerminalState{
		Workflow:  domain.StateFailed,
		Verdict:   evidence.VerdictFail,
		Completed: false,
		Reason:    "execution did not commit",
	}, evidence.VerdictFailed
}

// finalizeAudit performs the blocking, synchronous audit flush that every
// terminal session finalization MUST execute. On flush failure it degrades the
// result's terminal product (never rolling back the disk mutation) and
// returns an error wrapping ErrAuditPersistenceFailed. A nil flusher is a
// no-op that derives the terminal from the commit outcome alone.
//
// It MUST be called on every terminal path (commit, inspect, cancel) and its
// returned error MUST be propagated — never swallowed or merely logged.
func (o *Orchestrator) finalizeAudit(result *ExecutionResult, committed bool) error {
	var flushErr error
	if o != nil && o.audit != nil {
		if err := o.audit.Flush(); err != nil {
			flushErr = fmt.Errorf("%w: audit flush: %w", ErrAuditPersistenceFailed, err)
		}
	}
	terminal, verdict := EvaluateTerminalState(committed, flushErr)
	if result != nil {
		result.Terminal = terminal
		result.Verdict = verdict
		result.Completed = terminal.Completed
		if flushErr != nil {
			result.AuditError = flushErr
			result.EvidenceCompromised = true
		}
	}
	return flushErr
}
