package durable

import "github.com/PizenLabs/izen/internal/core/domain"

// TaskStatus is the lifecycle state of a task.
type TaskStatus string

const (
	TaskCreated     TaskStatus = "CREATED"
	TaskRunning     TaskStatus = "RUNNING"
	TaskPaused      TaskStatus = "PAUSED"
	TaskInterrupted TaskStatus = "INTERRUPTED"
	TaskCompleted   TaskStatus = "COMPLETED"
	TaskFailed      TaskStatus = "FAILED"
	// TaskRePlan is entered when cursor reconciliation observes a
	// third-party mutation (CurrentTreeDigest matches neither the
	// precondition nor the postcondition digest).
	TaskRePlan TaskStatus = "RE_PLAN"
)

// Terminal reports whether the status is a final lifecycle state. A non
// terminal task is unfinished work: it is what a restart must surface
// instead of silently re-prompting the human (§9 state reconstruction).
func (s TaskStatus) Terminal() bool {
	switch s {
	case TaskCompleted, TaskFailed:
		return true
	default:
		return false
	}
}

// CursorPhase tracks where an operation sits in its lifecycle.
type CursorPhase string

const (
	PhasePreExecution        CursorPhase = "PRE_EXECUTION"
	PhaseExecuting           CursorPhase = "EXECUTING"
	PhaseCommitted           CursorPhase = "COMMITTED"
	PhaseVerificationPending CursorPhase = "VERIFICATION_PENDING"
)

// CursorStatus is the dispatch status of an operation.
type CursorStatus string

const (
	CursorPending    CursorStatus = "PENDING"
	CursorDispatched CursorStatus = "DISPATCHED"
	CursorCommitted  CursorStatus = "COMMITTED"
	CursorConflict   CursorStatus = "CONFLICT"
)

// ExecutionCursor wraps every side-effecting capability (file writes,
// edits, shell commands) so retries are idempotent.
type ExecutionCursor struct {
	TaskID              string       `json:"taskId"`
	StepID              string       `json:"stepId"`
	OperationID         string       `json:"operationId"`
	Phase               CursorPhase  `json:"phase"`
	PreconditionDigest  string       `json:"preconditionDigest"`
	PostconditionDigest string       `json:"postconditionDigest"`
	Status              CursorStatus `json:"status"`
}

// TaskState is the materialized view persisted in snapshot.json.
type RecoveryPhase string

const (
	RecoveryRequired    RecoveryPhase = "RECOVERY_REQUIRED"
	BaselineEstablished RecoveryPhase = "BASELINE_ESTABLISHED"
)

type RecoveryContext struct {
	Phase                RecoveryPhase `json:"phase,omitempty"`
	StepID               string        `json:"stepId,omitempty"`
	Target               string        `json:"target,omitempty"`
	Reason               string        `json:"reason,omitempty"`
	Strategy             string        `json:"strategy,omitempty"`
	StateFingerprint     string        `json:"stateFingerprint,omitempty"`
	ObservedTokens       int           `json:"observedTokens,omitempty"`
	StreamBytes          int           `json:"streamBytes,omitempty"`
	LastCleanLine        int           `json:"lastCleanLine,omitempty"`
	LastCleanByteOffset  int           `json:"lastCleanByteOffset,omitempty"`
	LastCleanTokenOffset int           `json:"lastCleanTokenOffset,omitempty"`
	ReuseTarget          string        `json:"reuseTarget,omitempty"`
	ReuseSymbols         []string      `json:"reuseSymbols,omitempty"`
	BaselineScope        []string      `json:"baselineScope,omitempty"`
	// ConsecutiveZeroDeltas counts consecutive OUTPUT_CEILING recovery
	// attempts that produced zero workspace mutations (Phase 6.4.1
	// Zero-Delta Recovery Limit Invariant). It is incremented by the
	// scheduler's PostStepEvaluation on every StepOutcomePartial with
	// OUTPUT_CEILING reason and zero patches, and reset to 0 on any
	// successful nonzero workspace commit. When it reaches
	// zeroDeltaHaltThreshold the continuation loop aborts with
	// ErrRecoveryHalted instead of looping indefinitely.
	ConsecutiveZeroDeltas int `json:"consecutiveZeroDeltas,omitempty"`
}

type TaskState struct {
	ScopeProvenance   domain.ScopeProvenance `json:"scopeProvenance"`
	ID                string                 `json:"id"`
	Intent            string                 `json:"intent"`
	ActiveTargetScope []string               `json:"activeTargetScope"`
	CurrentStepID     string                 `json:"currentStepId"`
	RecoveryContext   RecoveryContext        `json:"recoveryContext,omitempty"`
	Cursor            *ExecutionCursor       `json:"cursor"`
	LastCheckpointID  string                 `json:"lastCheckpointId"`
	Status            TaskStatus             `json:"status"`
	// TokenUsage is the always-flushed cumulative telemetry for the task
	// (Phase 6.4.4 Always-Flush Telemetry Invariant). Every consumed prompt
	// or completion token is committed here via CommitUsage immediately —
	// on success, failure, or timeout — so a timed-out stream retains all
	// partial tokens and the UI footer (↑X ↓Y) never shows stale counts.
	TokenUsage TaskTokenUsage `json:"tokenUsage,omitempty"`
}

// TaskTokenUsage is the durable cumulative token accounting for one task.
// PromptTokens/CompletionTokens accumulate monotonically across turns;
// Estimated is true when the last commit carried a character-count fallback
// rather than authoritative provider usage.
type TaskTokenUsage struct {
	PromptTokens     int  `json:"promptTokens,omitempty"`
	CompletionTokens int  `json:"completionTokens,omitempty"`
	TotalTokens      int  `json:"totalTokens,omitempty"`
	Estimated        bool `json:"estimated,omitempty"`
	Turns            int  `json:"turns,omitempty"`
}

// CommitUsage always-flushes consumed tokens into the durable totals. It is
// additive and never zeroes: zero/negative inputs are ignored, and a fresh
// turn's live counts accumulate onto the session totals. The estimated flag
// latches true when any commit was estimated and clears only when an
// authoritative commit arrives.
func (s *TaskState) CommitUsage(prompt, completion int, estimated bool) {
	if s == nil {
		return
	}
	if prompt < 0 {
		prompt = 0
	}
	if completion < 0 {
		completion = 0
	}
	if prompt == 0 && completion == 0 {
		return
	}
	s.TokenUsage.PromptTokens += prompt
	s.TokenUsage.CompletionTokens += completion
	s.TokenUsage.TotalTokens = s.TokenUsage.PromptTokens + s.TokenUsage.CompletionTokens
	if estimated {
		s.TokenUsage.Estimated = true
	} else if prompt > 0 || completion > 0 {
		// An authoritative commit clears the estimated latch: provider truth
		// replaced the fallback.
		s.TokenUsage.Estimated = false
	}
	s.TokenUsage.Turns++
}

// ResetTurnUsage clears per-turn live state so a cancelled request never
// leaks stale counts into the next command. Session totals in TokenUsage
// are preserved — only the caller's live mirrors reset.
func ResetTurnUsage(prompt, completion *int) {
	if prompt != nil {
		*prompt = 0
	}
	if completion != nil {
		*completion = 0
	}
}

// EventType is the closed set of ledger event types.
type EventType string

const (
	EventTaskCreated        EventType = "TASK_CREATED"
	EventCheckpointCreated  EventType = "CHECKPOINT_CREATED"
	EventCursorDispatched   EventType = "CURSOR_DISPATCHED"
	EventExecutionCommitted EventType = "EXECUTION_COMMITTED"
	EventVerificationResult EventType = "VERIFICATION_RESULT"
	EventStaleLockReclaimed EventType = "STALE_LOCK_RECLAIMED"
	EventTargetConflict     EventType = "TARGET_CONFLICT"
	// Phase 2 (ephemeral worker) events: classified provider failures,
	// worker handoffs, and human-boundary pauses. FAILURE_CLASSIFIED is
	// always appended before any recovery transition is attempted.
	EventFailureClassified EventType = "FAILURE_CLASSIFIED"
	EventWorkerHandoff     EventType = "WORKER_HANDOFF"
	EventTaskPaused        EventType = "TASK_PAUSED"
	// Phase 3 (adaptive context engine) events: evidence-pressure signals
	// gating context-tier expansion, verified negative knowledge, and
	// context-tier transitions. Expansion requires a recorded
	// EVIDENCE_PRESSURE event; self-reported model confidence never
	// expands context on its own.
	EventEvidencePressure       EventType = "EVIDENCE_PRESSURE"
	EventNegativeKnowledge      EventType = "NEGATIVE_KNOWLEDGE_RECORDED"
	EventNegativeKnowledgeStale EventType = "NEGATIVE_KNOWLEDGE_STALED"
	EventContextTierAdvanced    EventType = "CONTEXT_TIER_ADVANCED"
	// Phase 4 (scope guard & multi-workspace continuity) events: hard
	// scope rejections at the guard boundary, structural ambiguity
	// routing, authorization decisions, and workspace policy switches.
	// None of these mutate task identity; they are audit lineage only.
	EventScopeViolationRejected EventType = "SCOPE_VIOLATION_REJECTED"
	EventStructuralAmbiguity    EventType = "STRUCTURAL_AMBIGUITY"
	EventProposalAuthorized     EventType = "PROPOSAL_AUTHORIZED"
	EventWorkspaceSwitched      EventType = "WORKSPACE_SWITCHED"
	// EventPhaseTransition records a logical orchestration phase hop
	// (domain/orchestration.Phase) against a durable task. It is audit
	// lineage only: task identity, cursor, checkpoint, evidence and
	// negative knowledge are preserved verbatim (STEP 2 parity).
	EventPhaseTransition EventType = "PHASE_TRANSITION"
)

// IsTruthBoundary reports whether the event type requires an explicit
// fsync before the append is considered durable.
func (e EventType) IsTruthBoundary() bool {
	switch e {
	case EventCheckpointCreated, EventExecutionCommitted, EventVerificationResult:
		return true
	default:
		return false
	}
}

// SequenceFaultKind classifies a ledger sequence defect observed while
// replaying ledger.ndjson.
type SequenceFaultKind string

const (
	// SequenceFaultGap: a sequenced event appeared with a value higher than
	// expected, so at least one event is missing from the journal.
	SequenceFaultGap SequenceFaultKind = "GAP"
	// SequenceFaultNonMonotonic: a sequenced event appeared with a value
	// that does not exceed the highest value already seen. Ordering can no
	// longer be trusted to follow the append order.
	SequenceFaultNonMonotonic SequenceFaultKind = "NON_MONOTONIC"
)

// SequenceFault records one sequence defect observed during replay. A
// corrupt journal is never silently accepted: the fault is surfaced to the
// caller instead of being folded away.
type SequenceFault struct {
	Kind     SequenceFaultKind `json:"kind"`
	Observed uint64            `json:"observed"`
	Expected uint64            `json:"expected"`
}

// LedgerEvent is one append-only line in ledger.ndjson.
type LedgerEvent struct {
	EventID   string    `json:"eventId"`
	TaskID    string    `json:"taskId"`
	Timestamp string    `json:"timestamp"`
	EventType EventType `json:"eventType"`
	// Sequence is the store-assigned monotonic position of this event in
	// the journal. It is assigned by appendLocked under the same lock as
	// the append itself and is durable with the event, so the journal's
	// order is verifiable without trusting file order. A zero means the
	// event was written by a build that predates the sequence: it is
	// legacy, not corrupt, and never moves the counter.
	Sequence uint64         `json:"sequence"`
	Payload  map[string]any `json:"payload"`
}

// NegativeKnowledgeRecord is the durable projection of one verified
// negative-knowledge entry. The full hypothesis lifecycle lives in
// internal/runtime/adaptive; the store keeps only what replay needs:
// identity, scope, status and evidence references.
type NegativeKnowledgeRecord struct {
	ID           string   `json:"id"`
	Hypothesis   string   `json:"hypothesis"`
	WhyRejected  string   `json:"whyRejected"`
	EvidenceRefs []string `json:"evidenceRefs,omitempty"`
	TargetScope  []string `json:"targetScope,omitempty"`
	Status       string   `json:"status"`
}

// Phase 3 status constants mirror adaptive.StatusActive/Stale without
// importing it (durable is the bottom of the dependency chain).
const (
	NegativeStatusActive = "ACTIVE"
	NegativeStatusStale  = "STALE"
)

// LockMetadata is the JSON payload stored in .izen/runtime/lock.
type LockMetadata struct {
	PID        int    `json:"pid"`
	CreatedAt  string `json:"created_at"`
	LeaseTTLMs int64  `json:"lease_ttl_ms"`
	// UpdatedAt is the last heartbeat in RFC3339; empty means no
	// heartbeat was ever written.
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ReconcileDecision is the deterministic outcome of comparing the
// current worktree digest against a pending cursor's digests.
type ReconcileDecision string

const (
	// DecisionAlreadyCommitted: CurrentTreeDigest == PostconditionDigest.
	// Advance to COMMITTED -> VERIFICATION_PENDING; DO NOT RE-EXECUTE.
	DecisionAlreadyCommitted ReconcileDecision = "ALREADY_COMMITTED"
	// DecisionSafeRetry: CurrentTreeDigest == PreconditionDigest.
	// The side effect never committed; set PENDING and retry.
	DecisionSafeRetry ReconcileDecision = "SAFE_RETRY"
	// DecisionConflict: digest matches neither; third-party mutation or
	// partial write. Set CONFLICT, emit TARGET_CONFLICT, go RE_PLAN.
	DecisionConflict ReconcileDecision = "CONFLICT"
	// DecisionUnknown is the honest result when NO durable cursor evidence
	// exists for a task: the process died before a cursor was dispatched, so
	// neither the pre-state nor the post-state can be compared. It is
	// deliberately NOT SafeRetry — absence of evidence is not proof that no
	// mutation occurred — and NOT Conflict — absence is not a confirmed
	// mismatch, either. Note that Reconcile NEVER returns it; only the
	// read-only inspection of surviving cursors does.
	DecisionUnknown ReconcileDecision = "UNKNOWN"
)

// CursorInspection is the READ-ONLY reconciliation of one task's durable
// mutation cursor against the live workspace digest. It appends no event and
// transitions no task, so a fresh runtime can learn the state of an
// interrupted mutation without resuming or retrying it. Decision is computed
// by the SAME durable.Reconcile rule the runtime uses everywhere else, except
// that a durable commit marker (CursorCommitted) is itself authoritative:
//
//   - committed + recorded postcondition digest matching the workspace ->
//     ALREADY_COMMITTED;
//   - committed + recorded postcondition digest NOT matching -> CONFLICT
//     (a third party changed the workspace after the operation committed);
//   - committed with no recorded digest (legacy/scopeguard commit) ->
//     ALREADY_COMMITTED from the marker alone;
//   - dispatched -> Reconcile(cursor, currentDigest) (SAFE_RETRY / CONFLICT);
//   - no cursor at all -> UNKNOWN.
type CursorInspection struct {
	TaskID              string            `json:"taskId"`
	OperationID         string            `json:"operationId,omitempty"`
	StepID              string            `json:"stepId,omitempty"`
	Phase               CursorPhase       `json:"phase,omitempty"`
	Status              CursorStatus      `json:"status,omitempty"`
	PreconditionDigest  string            `json:"preconditionDigest,omitempty"`
	PostconditionDigest string            `json:"postconditionDigest,omitempty"`
	Committed           bool              `json:"committed"`
	CurrentDigest       string            `json:"currentDigest,omitempty"`
	Decision            ReconcileDecision `json:"decision"`
}
