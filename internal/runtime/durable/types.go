package durable

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
type TaskState struct {
	ID                string           `json:"id"`
	Intent            string           `json:"intent"`
	ActiveTargetScope []string         `json:"activeTargetScope"`
	CurrentStepID     string           `json:"currentStepId"`
	Cursor            *ExecutionCursor `json:"cursor"`
	LastCheckpointID  string           `json:"lastCheckpointId"`
	Status            TaskStatus       `json:"status"`
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

// LedgerEvent is one append-only line in ledger.ndjson.
type LedgerEvent struct {
	EventID   string         `json:"eventId"`
	TaskID    string         `json:"taskId"`
	Timestamp string         `json:"timestamp"`
	EventType EventType      `json:"eventType"`
	Payload   map[string]any `json:"payload"`
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
)
