package domain

// ExecutionOutcome is the execution policy's terminal verdict (ARCH:30).
type ExecutionOutcome uint8

const (
	OutcomeCompleted ExecutionOutcome = iota // terminated normally per policy
	OutcomeIncomplete                        // budget/time exhausted, not failed
	OutcomeFailed                            // failure classified, recovery exhausted
	OutcomeAborted                           // cancelled, OCC abort, or scope violation rollback
)

func (o ExecutionOutcome) String() string {
	switch o {
	case OutcomeCompleted:
		return "COMPLETED"
	case OutcomeIncomplete:
		return "INCOMPLETE"
	case OutcomeFailed:
		return "FAILED"
	case OutcomeAborted:
		return "ABORTED"
	default:
		return "UNKNOWN"
	}
}

// TerminalState is the authoritative completion product (ARCH:30).
type TerminalState struct {
	Outcome  ExecutionOutcome `json:"outcome"`
	Evidence EvidenceState    `json:"evidence"`
	Vector   EvidenceVector   `json:"vector"` // full evidence for audit
	Reason   string           `json:"reason"` // human explanation
}

// Valid enforces the forbidden-combination rules (INV:11, INV:8, INV:9).
func (t TerminalState) Valid() bool {
	// INV:8 — Only COMPLETED may be VERIFIED. FAILED/INCOMPLETE/ABORTED can never be VERIFIED.
	// INV:11 — Forbidden matrices: INCOMPLETE·VERIFIED, FAILED·VERIFIED, ABORTED·VERIFIED.
	if t.Evidence == EvidenceVerified && t.Outcome != OutcomeCompleted {
		return false
	}
	// INV:9 — Resource exhaustion / failure semantics: FAILED must not be VERIFIED (already covered)
	// but also INCOMPLETE must not be VERIFIED. The above already handles all non-COMPLETED.
	// Keep explicit checks for clarity and to catch future outcome additions.
	switch t.Outcome {
	case OutcomeIncomplete:
		if t.Evidence == EvidenceVerified {
			return false
		}
	case OutcomeFailed:
		if t.Evidence == EvidenceVerified {
			return false
		}
	case OutcomeAborted:
		if t.Evidence == EvidenceVerified {
			return false
		}
	}
	return true
}

// String renders "COMPLETED · VERIFIED" form (ARCH:30 examples).
func (t TerminalState) String() string {
	return t.Outcome.String() + " · " + t.Evidence.String()
}
