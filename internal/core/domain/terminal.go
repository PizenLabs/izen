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
	if t.Evidence == EvidenceVerified && t.Outcome != OutcomeCompleted {
		return false
	}
	return true
}

// String renders "COMPLETED · VERIFIED" form (ARCH:30 examples).
func (t TerminalState) String() string {
	return t.Outcome.String() + " · " + t.Evidence.String()
}
