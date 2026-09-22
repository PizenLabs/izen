package continuation

import "time"

// ContinuationAction is the terminal or continuation semantic decided by
// DeriveNextStep. It is task-level, distinct from the scheduler's
// StepOutcome (pending/complete/partial/failed) which describes one bounded
// execution step's outcome.
type ContinuationAction string

const (
	ActionComplete         ContinuationAction = "COMPLETE"
	ActionContinue         ContinuationAction = "CONTINUE"
	ActionBlocked          ContinuationAction = "BLOCKED"
	ActionFailed           ContinuationAction = "FAILED"
	ActionStale            ContinuationAction = "STALE"
	ActionAwaitingApproval ContinuationAction = "AWAITING_APPROVAL"
	ActionNoProgress       ContinuationAction = "NO_PROGRESS"
)

// ObservationKind distinguishes authoritative state sources (spec §10).
// Only EXECUTION_RESULT / OBSERVATION / VERIFICATION / STATE_TRANSITION are
// truthful; MODEL_PROPOSAL is an untrusted claim and never drives a transition
// on its own.
type ObservationKind string

const (
	KindModelProposal   ObservationKind = "MODEL_PROPOSAL"
	KindExecutionResult ObservationKind = "EXECUTION_RESULT"
	KindObservation     ObservationKind = "OBSERVATION"
	KindVerification    ObservationKind = "VERIFICATION"
	KindStateTransition ObservationKind = "STATE_TRANSITION"
)

// Observation is one durable, evidence-backed fact about what actually
// happened. It is not a transcript line.
type Observation struct {
	Kind             ObservationKind `json:"kind"`
	Subject          string          `json:"subject,omitempty"`
	Detail           string          `json:"detail,omitempty"`
	EvidenceDigest   string          `json:"evidence_digest,omitempty"`
	StateFingerprint string          `json:"state_fingerprint,omitempty"`
	Timestamp        time.Time       `json:"timestamp,omitempty"`
}

// StepProposal is the bounded next-step proposal derived by continuation.
// It carries no authorization grant, no capability, and no execution handle.
// The scheduler remains free to admit or reject it via the existing
// authorization boundary.
type StepProposal struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"` // problem.StepKind string value (domain-neutral)
	Targets         []string `json:"targets,omitempty"`
	Rationale       string   `json:"rationale,omitempty"`
	EvidenceRefs    []string `json:"evidence_refs,omitempty"`
	EstimatedTokens int      `json:"estimated_tokens,omitempty"`
	StateDigest     string   `json:"state_digest,omitempty"`
}

// ContinuationDecision is the minimal domain-neutral contract (§5).
// Action describes whether and how to proceed; NextStep is present only
// when Action == CONTINUE.
type ContinuationDecision struct {
	Action      ContinuationAction `json:"action"`
	Reason      string             `json:"reason,omitempty"`
	Evidence    []Observation      `json:"evidence,omitempty"`
	NextStep    *StepProposal      `json:"next_step,omitempty"`
	StateDigest string             `json:"state_digest,omitempty"`
}

// BoundedStepState is the lifecycle (§4) for one problem-solving step.
// Existing StepOutcome names are reused where they already carry the same
// meaning (VERIFIED ~ Complete, PARTIAL ~ StepOutcomePartial).
type BoundedStepState string

const (
	StateProposed  BoundedStepState = "PROPOSED"
	StateAdmitted  BoundedStepState = "ADMITTED"
	StateExecuting BoundedStepState = "EXECUTING"
	StateObserved  BoundedStepState = "OBSERVED"
	StateVerified  BoundedStepState = "VERIFIED"
	StatePartial   BoundedStepState = "PARTIAL"
	StateBlocked   BoundedStepState = "BLOCKED"
	StateFailed    BoundedStepState = "FAILED"
	StateContinue  BoundedStepState = "CONTINUE"
	StateTerminate BoundedStepState = "TERMINATE"
)

// TaskStateView is the durable, transcript-free task state continuation
// reads. It mirrors the fields the scheduler and durable store persist:
// digests, evidence, fingerprints, recovery context, and scope — never
// conversation history or provider KV state.
type TaskStateView struct {
	TaskID              string
	Intent              string
	ActiveScope         []string // durable ActiveTargetScope ($hot / $prompt envelope)
	StateFingerprint    string   // workspace/state digest (OCC)
	RecoveryPhase       string
	RecoveryReason      string
	ObservedTokens      int
	CompletedSteps      []string // durable completed step IDs
	PendingSteps        []string // plan steps still pending
	EvidenceDigest      string   // evidence vector digest
	CurrentStepID       string
	History             []StepHistoryEntry // bounded history for no-progress detection
	ProviderCeiling     int                // per-step output ceiling (provider constraint)
	RequestedBudget     int                // caller-requested step budget
	RemainingTaskBudget int                // task-level remaining budget
}

// StepHistoryEntry records one prior bounded step for no-progress detection.
type StepHistoryEntry struct {
	StepID           string
	Outcome          string // scheduler StepOutcome string value
	StateFingerprint string
	EvidenceDigest   string
	Patches          int
	Timestamp        time.Time
}

// DerivationInput is the complete truthful state DeriveNextStep consumes.
// No field is a transcript, hidden KV, or model memory.
type DerivationInput struct {
	Task            TaskStateView
	PlanKind        string         // current ProblemSolvingPlan kind focus, if any
	PlanSteps       []PlanStepView // domain-neutral plan steps (problem.ProblemStep projection)
	PreviousOutcome string         // scheduler StepOutcome string: pending/complete/partial/failed
	PreviousReason  string         // reason for partial (e.g. OUTPUT_CEILING)
	Observations    []Observation  // durable observations (execution/verification/state)
	Verified        bool           // verification passed for last execution
	HasStaleState   bool           // OCC / fingerprint drift detected by caller
	IsPartialOutput bool           // finish_reason=length truncation
	ProposedTargets []string       // what the last model claimed it wants to touch (treated as hypothesis)
	AllowedScope    []string       // envelope ($hot) — continuation must not expand it
	MaxSteps        int            // task/step budget cap (0 = use default)
}

// PlanStepView is a domain-neutral projection of problem.ProblemStep.
// Keeping it stringly-typed avoids importing problem in generated docs but
// the derive logic accepts the real problem.ProblemStep via helper.
type PlanStepView struct {
	ID         string
	Kind       string
	References []string
	Rationale  string
}
