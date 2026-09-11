package ephemeral

import (
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// DefaultMaxRecoveryAttempts bounds auto-recovery per task when the caller
// does not specify a budget.
const DefaultMaxRecoveryAttempts = 3

// DefaultMaxEvidence bounds the capsule evidence list.
const DefaultMaxEvidence = 10

// FailureContext is the complete input for one bounded recovery transition.
// The ProviderErr is classified first; every other field feeds assessment,
// capsule derivation and routing. It deliberately carries NO conversation
// history or prompt transcripts.
type FailureContext struct {
	TaskID string
	Step   StepDefinition
	// ConditionFingerprint identifies "identical conditions" for the
	// consecutive-failure rule (typically step ID + precondition digest).
	ConditionFingerprint string
	ProviderErr          ProviderError

	CurrentWorker WorkerDescriptor

	PlanValid               bool
	TargetValid             bool
	EvidenceValid           bool
	CurrentStepClear        bool
	SemanticDriftObserved   bool
	VerificationInvalidated bool

	Objective      string
	Constraints    []string
	CompletedSteps []string
	PendingSteps   []string
	Evidence       []EvidenceSummary
	AllowedTools   []string
	CheckpointID   string
}

// RecoveryOutcome is the deterministic result of one recovery transition.
type RecoveryOutcome struct {
	Reason FailureReason
	Action RecoveryAction
	// Contract is set only on RESUME: the sole context the replacement
	// worker receives (transcript-free by construction).
	Contract *ResumeContract
	// TargetWorker is the routed replacement (RESUME only).
	TargetWorker WorkerDescriptor
	// AssessmentReason is the audit trail from RecoveryAssessment.
	AssessmentReason string
	// Paused is true when the task was transitioned to PAUSED.
	Paused bool
	// BudgetLeft is the remaining recovery budget after this transition.
	BudgetLeft int
}

// RecoveryEngine orchestrates ledger-first bounded recovery: classify ->
// record -> assess -> route -> handoff, with every transition decrementing
// the per-task budget and exhaustion yielding to the human boundary.
type RecoveryEngine struct {
	mu          sync.Mutex
	store       *durable.TaskStore
	router      *WorkerRouter
	maxAttempts int
	maxEvidence int

	budgetLeft  map[string]int
	consecutive map[string]int
	lastCond    map[string]string
}

// NewRecoveryEngine binds the engine to a durable store and a worker
// router. Non-positive maxAttempts/maxEvidence fall back to defaults.
func NewRecoveryEngine(store *durable.TaskStore, router *WorkerRouter, maxAttempts, maxEvidence int) *RecoveryEngine {
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxRecoveryAttempts
	}
	if maxEvidence <= 0 {
		maxEvidence = DefaultMaxEvidence
	}
	return &RecoveryEngine{
		store:       store,
		router:      router,
		maxAttempts: maxAttempts,
		maxEvidence: maxEvidence,
		budgetLeft:  make(map[string]int),
		consecutive: make(map[string]int),
		lastCond:    make(map[string]string),
	}
}

// HandleFailure executes one bounded recovery transition. It enforces the
// ledger-first invariant: the classified FAILURE_CLASSIFIED event is
// recorded before any assessment or routing is attempted; if the record
// fails, no recovery is attempted and the error is returned.
func (e *RecoveryEngine) HandleFailure(fc FailureContext) (RecoveryOutcome, error) {
	if e == nil || e.store == nil {
		return RecoveryOutcome{}, fmt.Errorf("ephemeral: nil recovery engine store")
	}
	if strings.TrimSpace(fc.TaskID) == "" {
		return RecoveryOutcome{}, fmt.Errorf("ephemeral: empty task id")
	}
	if strings.TrimSpace(fc.Step.ID) == "" {
		return RecoveryOutcome{}, fmt.Errorf("ephemeral: empty step id")
	}

	// 1. Classify into the canonical taxonomy.
	reason := FailureClassifier{}.Classify(fc.ProviderErr)

	// 2. Ledger-first: record BEFORE any recovery attempt.
	detail := fc.ProviderErr.FinishReason
	if fc.ProviderErr.Err != nil {
		if detail != "" {
			detail += ": " + fc.ProviderErr.Err.Error()
		} else {
			detail = fc.ProviderErr.Err.Error()
		}
	}
	if err := e.store.RecordFailure(fc.TaskID, string(reason), fc.ProviderErr.OperationID, detail); err != nil {
		return RecoveryOutcome{}, fmt.Errorf("ephemeral: record failure: %w", err)
	}

	e.mu.Lock()
	left, ok := e.budgetLeft[fc.TaskID]
	if !ok {
		left = e.maxAttempts
	}
	cond := fc.TaskID + "\x00" + fc.Step.ID + "\x00" + fc.ConditionFingerprint
	if prev, seen := e.lastCond[fc.TaskID]; seen && prev == cond {
		e.consecutive[fc.TaskID]++
	} else {
		e.consecutive[fc.TaskID] = 1
		e.lastCond[fc.TaskID] = cond
	}
	consecutive := e.consecutive[fc.TaskID]
	e.mu.Unlock()

	state, found := e.store.State(fc.TaskID)
	checkpoint := fc.CheckpointID
	if checkpoint == "" && found {
		checkpoint = state.LastCheckpointID
	}

	capsule := DeriveCapsule(CapsuleSource{
		State:          state,
		Objective:      fc.Objective,
		Constraints:    fc.Constraints,
		CompletedSteps: fc.CompletedSteps,
		Current:        fc.Step,
		PendingSteps:   fc.PendingSteps,
		Evidence:       fc.Evidence,
		Budget:         BudgetState{RecoveryAttemptsLeft: left, MaxRecoveryAttempts: e.maxAttempts},
		AllowedTools:   fc.AllowedTools,
	}, e.maxEvidence)

	// 3. Budget exhausted at entry: ESCALATE + PAUSED, no routing.
	if left <= 0 {
		if err := e.store.PauseTask(fc.TaskID, "recovery budget exhausted"); err != nil {
			return RecoveryOutcome{}, fmt.Errorf("ephemeral: pause task: %w", err)
		}
		return RecoveryOutcome{
			Reason: reason, Action: ActionEscalate,
			AssessmentReason: "recovery budget exhausted; yielding to human boundary",
			Paused:           true, BudgetLeft: 0,
		}, nil
	}

	// 4. Assess.
	assessment, err := RecoveryAssessment{}.Assess(AssessmentInput{
		Reason:                  reason,
		PlanValid:               fc.PlanValid,
		TargetValid:             fc.TargetValid,
		EvidenceValid:           fc.EvidenceValid,
		CurrentStepClear:        fc.CurrentStepClear,
		SemanticDriftObserved:   fc.SemanticDriftObserved,
		ConsecutiveFailures:     consecutive,
		VerificationInvalidated: fc.VerificationInvalidated,
		RecoveryAttemptsLeft:    left,
		Capsule:                 capsule,
	}, "")
	if err != nil {
		return RecoveryOutcome{}, err
	}

	// Every non-escalation transition consumes budget. Escalation halts
	// auto-recovery: the task pauses so loops cannot continue.
	e.mu.Lock()
	e.budgetLeft[fc.TaskID] = left - 1
	after := e.budgetLeft[fc.TaskID]
	e.mu.Unlock()

	switch assessment.Action {
	case ActionEscalate:
		if err := e.store.PauseTask(fc.TaskID, assessment.Reason); err != nil {
			return RecoveryOutcome{}, fmt.Errorf("ephemeral: pause task: %w", err)
		}
		return RecoveryOutcome{
			Reason: reason, Action: ActionEscalate,
			AssessmentReason: assessment.Reason, Paused: true, BudgetLeft: after,
		}, nil
	case ActionRePlan:
		return RecoveryOutcome{
			Reason: reason, Action: ActionRePlan,
			AssessmentReason: assessment.Reason, BudgetLeft: after,
		}, nil
	default: // ActionResume
	}

	// 5. Route a replacement worker. Routing failures escalate (never retry
	// unbounded): the budget for this transition is already consumed.
	resumeCapsule := capsule
	if assessment.UpdatedCapsule != nil {
		resumeCapsule = *assessment.UpdatedCapsule
	}
	resumeCapsule.RemainingBudget = BudgetState{RecoveryAttemptsLeft: after, MaxRecoveryAttempts: e.maxAttempts}

	routeReq := RouteRequest{
		Reason:               reason,
		FailedWorkerID:       fc.CurrentWorker.ID,
		FailedProvider:       fc.CurrentWorker.Provider,
		Step:                 fc.Step,
		CurrentContextTokens: fc.CurrentWorker.ContextTokens,
		BudgetLeft:           after,
	}
	// The final budgeted attempt (after == 0) still deserves its handoff:
	// exhaustion is enforced at the NEXT HandleFailure entry. The router
	// itself rejects zero budgets, so grant it a 1-floor view here while
	// the persisted capsule carries the true zero.
	if routeReq.BudgetLeft <= 0 {
		routeReq.BudgetLeft = 1
	}
	if e.router == nil {
		return RecoveryOutcome{}, fmt.Errorf("ephemeral: no worker router configured")
	}
	routed, err := e.router.Route(routeReq)
	if err != nil {
		if perr := e.store.PauseTask(fc.TaskID, "no eligible worker: "+err.Error()); perr != nil {
			return RecoveryOutcome{}, fmt.Errorf("ephemeral: pause task: %w", perr)
		}
		return RecoveryOutcome{
			Reason: reason, Action: ActionEscalate,
			AssessmentReason: "no eligible replacement worker; yielding to human boundary",
			Paused:           true, BudgetLeft: after,
		}, nil
	}

	// 6. Build the transcript-free contract and record the handoff.
	// State-driven failover: TaskID / CheckpointID / lineage preserved.
	if checkpoint == "" {
		checkpoint = "cp-" + fc.TaskID + "-resume"
	}
	contract, err := BuildResumeContract(resumeCapsule, checkpoint, reason, fc.AllowedTools)
	if err != nil {
		return RecoveryOutcome{}, err
	}
	if err := e.store.RecordHandoff(fc.TaskID, fc.CurrentWorker.ID, routed.Worker.ID, checkpoint, string(reason)); err != nil {
		return RecoveryOutcome{}, fmt.Errorf("ephemeral: record handoff: %w", err)
	}
	return RecoveryOutcome{
		Reason: reason, Action: ActionResume,
		Contract: &contract, TargetWorker: routed.Worker,
		AssessmentReason: assessment.Reason + "; " + routed.Reason,
		BudgetLeft:       after,
	}, nil
}

// BudgetLeft returns the remaining recovery budget for a task
// (maxAttempts before any transition).
func (e *RecoveryEngine) BudgetLeft(taskID string) int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if left, ok := e.budgetLeft[taskID]; ok {
		return left
	}
	return e.maxAttempts
}

// ConsecutiveFailures returns the current identical-condition streak.
func (e *RecoveryEngine) ConsecutiveFailures(taskID string) int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.consecutive[taskID]
}
