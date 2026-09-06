package domain

// ExecutionObservation is the immutable result of executing one ExecutionUnit.
type ExecutionObservation struct {
	UnitID           UnitID              `json:"unit_id"`
	ProposalOutcome  ProposalOutcome     `json:"proposal_outcome"`
	MutationResult   *MutationResult     `json:"mutation_result,omitempty"`
	OutputStatus     OutputStatus        `json:"output_status"`
	BudgetUsage      BudgetUsage         `json:"budget_usage"`
	DependencyFresh  DependencyFreshness `json:"dependency_freshness"`
	EvidenceDelta    EvidenceDelta       `json:"evidence_delta"`
	FailureSignals   []FailureSignal     `json:"failure_signals"`
	StateFingerprint string              `json:"state_fingerprint"` // canonical hash — ARCH:26
}

type ProposalOutcome uint8

const (
	ProposalAccepted ProposalOutcome = iota // accepted and applied or staged
	ProposalRejected
	ProposalDraftFrozen // budget breach before mutation — ARCH:11 PATCH DRAFT
)

type MutationResult struct {
	Applied    bool          `json:"applied"`
	Targets    []string      `json:"targets"`
	DiffLines  int           `json:"diff_lines"`
	RollbackID *CheckpointID `json:"rollback_id,omitempty"`
}

type OutputStatus uint8

const (
	OutputComplete OutputStatus = iota
	OutputExhausted
	OutputTruncated
)

type BudgetUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Requests     int `json:"requests"`
	Files        int `json:"files"`
	DiffLines    int `json:"diff_lines"`
	ShellCmds    int `json:"shell_cmds"`
}

type DependencyFreshness uint8

const (
	FreshnessValid DependencyFreshness = iota
	FreshnessStale
	FreshnessInvalidated
)

type EvidenceDelta struct {
	FromLevel EvidenceLevel `json:"from_level"`
	ToLevel   EvidenceLevel `json:"to_level"`
	Summary   string        `json:"summary"`
}

type ObservedFailureClass string

const (
	FailureCode        ObservedFailureClass = "CODE"
	FailureEnvironment ObservedFailureClass = "ENVIRONMENT"
	FailureTest        ObservedFailureClass = "TEST"
	FailureScope       ObservedFailureClass = "SCOPE"
	FailureUnknown     ObservedFailureClass = "UNKNOWN"
)

type FailureSignal struct {
	Class   ObservedFailureClass `json:"class"` // CODE | ENVIRONMENT | TEST | SCOPE | UNKNOWN
	Message string               `json:"message"`
	File    string               `json:"file,omitempty"`
}
