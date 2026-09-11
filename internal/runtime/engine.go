package runtime

// Master orchestrator unifying the four runtime phases into one coherent
// execution pipeline:
//
//	durable (TaskStore / ledger.ndjson) — source of truth
//	ephemeral (RecoveryEngine) — bounded worker-failure recovery
//	adaptive (ContextPlanner) — evidence-gated context expansion
//	scopeguard (IntentGateway + RuntimeExecutor + WorkspaceSession) — authority
//
// Pipeline sequence in ExecuteProposal:
//
//  1. Reconciliation boundary — resolve pending/crash-interrupted cursors.
//  2. Authority & scope check — gateway.EvaluateProposal.
//  3. Idempotent side-effect execution — executor.ExecuteAuthorized.
//  4. Failure & feedback loop — classify, record evidence pressure +
//     negative knowledge, recover, mark stale on re-plan.
//
// No-memory-only-drift: every intermediate state is persisted to TaskState
// or appended to ledger.ndjson. The in-memory ContextPlanner is synced from
// the store on entry and any expansion is persisted via AdvanceContextTier.

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/adaptive"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
	"github.com/PizenLabs/izen/internal/runtime/scopeguard"
)

// RuntimeEngine unifies the durable, ephemeral, adaptive and scopeguard
// phases. It is bound to exactly one durable task: proposals for any other
// task ID are rejected so ledger lineage can never drift across tasks.
type RuntimeEngine struct {
	store    *durable.TaskStore
	recovery *ephemeral.RecoveryEngine
	planner  *adaptive.ContextPlanner
	gateway  *scopeguard.IntentGateway
	executor *scopeguard.RuntimeExecutor
	policy   *scopeguard.WorkspaceSession

	workDir string
	taskID  string
	scope   []string
}

// NewRuntimeEngine wires a master orchestrator for one task. workDir roots
// digest computation, scope is the AuthorizedTargetScope, ws is the initial
// workspace, budget bounds mutating execution (nil disables the budget
// tier, never widens authority), router feeds the recovery engine (an empty
// registry escalates instead of routing), and graph feeds the structural
// guard (nil is fail-closed for unlisted targets; listed targets always
// pass). The durable store is opened before return.
func NewRuntimeEngine(
	workDir, taskID string,
	scope []string,
	ws scopeguard.Workspace,
	budget scopeguard.Budget,
	router *ephemeral.WorkerRouter,
	graph scopeguard.GraphProvider,
) (*RuntimeEngine, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("runtime: empty workDir")
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("runtime: empty task id")
	}
	store := durable.NewTaskStore(workDir)
	if err := store.Open(); err != nil {
		return nil, fmt.Errorf("runtime: open task store: %w", err)
	}
	session, err := scopeguard.NewWorkspaceSession(taskID, "", ws)
	if err != nil {
		return nil, fmt.Errorf("runtime: workspace session: %w", err)
	}
	var ledger scopeguard.ScopeLedger = scopeguard.StoreLedger{Store: store}
	gateway := scopeguard.NewIntentGateway(
		scopeguard.NewScopeGuard(scope),
		scopeguard.NewStructuralGuard(graph),
		session,
		budget,
		ledger,
	)
	return &RuntimeEngine{
		store:    store,
		recovery: ephemeral.NewRecoveryEngine(store, router, 0, 0),
		planner:  adaptive.NewContextPlanner(),
		gateway:  gateway,
		executor: scopeguard.NewRuntimeExecutorWithWorkDir(store, workDir),
		policy:   session,
		workDir:  workDir,
		taskID:   taskID,
		scope:    append([]string(nil), scope...),
	}, nil
}

// Store exposes the bound durable task substrate.
func (e *RuntimeEngine) Store() *durable.TaskStore {
	if e == nil {
		return nil
	}
	return e.store
}

// Planner exposes the adaptive context planner (synced from the store on
// every ExecuteProposal entry).
func (e *RuntimeEngine) Planner() *adaptive.ContextPlanner {
	if e == nil {
		return nil
	}
	return e.planner
}

// Policy exposes the workspace session.
func (e *RuntimeEngine) Policy() *scopeguard.WorkspaceSession {
	if e == nil {
		return nil
	}
	return e.policy
}

// TaskID returns the single task this engine is bound to.
func (e *RuntimeEngine) TaskID() string {
	if e == nil {
		return ""
	}
	return e.taskID
}

// ExecuteProposal runs one worker proposal through the full pipeline and
// returns the updated durable TaskState. effect performs the real side
// effect (it MUST report the post-execution digest); verify is the
// evidence-verification seam (linter/test runner) and runs both as the
// ambiguity gate and as post-commit validation.
//
// Every intermediate state is persisted: reconciliation transitions,
// authorization lineage, cursor dispatch/commit, verification results,
// evidence pressure, negative knowledge, failure classification, handoffs,
// pauses and stale transitions all land in TaskState or ledger.ndjson.
func (e *RuntimeEngine) ExecuteProposal(
	ctx context.Context,
	p scopeguard.Proposal,
	primaryScope []string,
	effect scopeguard.Effect,
	verify scopeguard.Verifier,
) (durable.TaskState, error) {
	if e == nil || e.store == nil || e.gateway == nil || e.executor == nil ||
		e.recovery == nil || e.planner == nil || e.policy == nil {
		return durable.TaskState{}, fmt.Errorf("runtime: nil engine dependency (fail-closed)")
	}
	if p.TaskID != e.taskID {
		return durable.TaskState{}, fmt.Errorf("runtime: proposal task %q does not match engine task %q", p.TaskID, e.taskID)
	}

	// ── 1. Reconciliation boundary ──
	// Resolve pending or crash-interrupted side effects before evaluating
	// anything new. Transitions persist inside ReconcileAll.
	if _, err := e.executor.ReconcilePendingCursors(ctx); err != nil {
		return e.currentState(p.TaskID)
	}

	// Keep the in-memory planner rung aligned with durable truth.
	e.planner.SyncFromStore(p.TaskID, e.store.ContextTier(p.TaskID))

	// ── 2. Authority & scope check ──
	decision := e.gateway.EvaluateProposal(ctx, p, primaryScope)
	switch decision.Decision {
	case scopeguard.DecisionDeny:
		// Denial lineage (SCOPE_VIOLATION_REJECTED / STRUCTURAL_REJECT /
		// policy / budget) is already recorded by the gateway tiers.
		st, serr := e.currentState(p.TaskID)
		if serr != nil {
			return durable.TaskState{}, fmt.Errorf("runtime: scope violation (%s)", decision.Reason)
		}
		return st, fmt.Errorf("runtime: scope violation: %s", decision.Reason)
	case scopeguard.DecisionRequireVerification:
		// Evidence verification is triggered BEFORE any execution.
		verified, vdetail, verr := runVerification(ctx, verify)
		if verr != nil || !verified {
			detail := vdetail
			if verr != nil {
				detail = "verifier error: " + verr.Error()
			}
			return e.handleFailure(p, fmt.Errorf("runtime: verification failed: %s", detail), detail, true)
		}
		// Ambiguity resolved by evidence: persist the authorization so the
		// grant is audit lineage, not a memory-only upgrade.
		if err := e.store.RecordCustomEvent(p.TaskID, durable.EventProposalAuthorized, map[string]any{
			"proposalId": p.ID,
			"op":         string(p.Op),
			"targets":    append([]string(nil), p.TargetFiles...),
			"verified":   true,
		}); err != nil {
			return e.currentState(p.TaskID)
		}
		decision.Decision = scopeguard.DecisionAllow
		decision.Reason = "ambiguity resolved by verification: " + decision.Reason
	case scopeguard.DecisionAllow:
		// Proceed to execution.
	default:
		return e.currentState(p.TaskID)
	}

	// ── 3. Idempotent side-effect execution ──
	_, err := e.executor.ExecuteAuthorized(ctx, p, decision, effect, verify)
	if err != nil {
		// ── 4. Failure & feedback loop ──
		verificationFailed := isVerificationError(err)
		detail := err.Error()
		return e.handleFailure(p, err, detail, verificationFailed)
	}

	return e.currentState(p.TaskID)
}

// handleFailure classifies the error, records evidence pressure and
// negative knowledge for verification failures, triggers bounded recovery,
// marks constraints STALE on re-plan, and returns the updated TaskState
// with the original error. Every step persists to the ledger.
func (e *RuntimeEngine) handleFailure(
	p scopeguard.Proposal,
	execErr error,
	detail string,
	verificationFailed bool,
) (durable.TaskState, error) {
	reason := ephemeral.ClassifyError(execErr)
	if verificationFailed {
		reason = ephemeral.FailureVerificationFailure
	}

	if reason == ephemeral.FailureVerificationFailure {
		// Evidence pressure: the deterministic signal gating context-tier
		// expansion. Persisted before any recovery transition.
		if err := e.store.RecordEvidencePressure(p.TaskID, string(adaptive.SignalVerificationFailure), detail); err != nil {
			st, _ := e.currentState(p.TaskID)
			return st, fmt.Errorf("runtime: record evidence pressure: %w", execErr)
		}
		// Adaptive expansion: in-memory rung and durable tier advance
		// together so the planner can never drift from the ledger.
		dec := adaptive.EvidencePressureEvaluator{}.Evaluate(adaptive.PressureInput{VerificationFailed: true})
		if tier, expanded, _ := e.planner.RequestExpansion(p.TaskID, dec); expanded {
			if err := e.store.AdvanceContextTier(p.TaskID, int(tier)); err != nil {
				st, _ := e.currentState(p.TaskID)
				return st, fmt.Errorf("runtime: advance context tier: %w", execErr)
			}
		}
		// Verified negative knowledge: the disproven approach plus its
		// concrete evidence. Persisted as NEGATIVE_KNOWLEDGE_RECORDED.
		hypothesis := strings.TrimSpace(p.Detail)
		if hypothesis == "" {
			hypothesis = fmt.Sprintf("proposal %s: %s %s", p.ID, p.Op, strings.Join(p.TargetFiles, ", "))
		}
		opRef := p.OperationID
		if opRef == "" {
			opRef = p.ID
		}
		if err := e.store.RecordNegativeKnowledge(p.TaskID, durable.NegativeKnowledgeRecord{
			Hypothesis:   hypothesis,
			WhyRejected:  "verification failed: " + detail,
			EvidenceRefs: []string{opRef, "verification:" + truncateDetail(detail)},
			TargetScope:  append([]string(nil), p.TargetFiles...),
			Status:       durable.NegativeStatusActive,
		}); err != nil {
			st, _ := e.currentState(p.TaskID)
			return st, fmt.Errorf("runtime: record negative knowledge: %w", execErr)
		}
	}

	// Bounded recovery (ledger-first: FAILURE_CLASSIFIED is recorded inside
	// HandleFailure before any routing is attempted).
	fc := e.failureContext(p, execErr, verificationFailed)
	outcome, err := e.recovery.HandleFailure(fc)
	if err != nil {
		st, _ := e.currentState(p.TaskID)
		return st, fmt.Errorf("runtime: recovery: %w (caused by %w)", err, execErr)
	}

	// On re-plan the preconditions of active constraints are invalidated:
	// transition every ACTIVE record to STALE in the ledger.
	if outcome.Action == ephemeral.ActionRePlan {
		for _, rec := range e.store.ActiveNegativeKnowledge(p.TaskID, nil) {
			if err := e.store.MarkNegativeKnowledgeStale(p.TaskID, rec.ID, "RE_PLAN invalidated preconditions"); err != nil {
				st, _ := e.currentState(p.TaskID)
				return st, fmt.Errorf("runtime: mark negative knowledge stale: %w", execErr)
			}
		}
	}

	st, serr := e.currentState(p.TaskID)
	if serr != nil {
		return durable.TaskState{}, execErr
	}
	return st, fmt.Errorf("runtime: proposal failed (recovery %s): %w", outcome.Action, execErr)
}

// failureContext builds the bounded-recovery input for one failed proposal.
// It carries no transcripts: only materialized state, digests and validity
// gates.
func (e *RuntimeEngine) failureContext(
	p scopeguard.Proposal,
	execErr error,
	verificationFailed bool,
) ephemeral.FailureContext {
	stepID := p.StepID
	if strings.TrimSpace(stepID) == "" {
		stepID = p.OperationID
	}
	if strings.TrimSpace(stepID) == "" {
		stepID = p.ID
	}
	fingerprint := p.ID
	if digest, err := durable.ComputeTreeDigest(e.workDir, p.TargetFiles...); err == nil {
		fingerprint = digest
	}
	checkpoint := ""
	intent := ""
	if st, ok := e.store.State(p.TaskID); ok {
		checkpoint = st.LastCheckpointID
		intent = st.Intent
	}
	providerErr := ephemeral.ProviderError{Err: execErr, OperationID: p.OperationID}
	if verificationFailed && execErr != nil {
		// Ensure verification dialect survives classification inside
		// recovery: the ledger detail already carries it.
		providerErr.FinishReason = "verification_failed: " + execErr.Error()
	}
	return ephemeral.FailureContext{
		TaskID:                  p.TaskID,
		Step:                    ephemeral.StepDefinition{ID: stepID, Goal: p.Detail},
		ConditionFingerprint:    fingerprint,
		ProviderErr:             providerErr,
		CurrentWorker:           ephemeral.WorkerDescriptor{ID: "runtime-engine", Provider: "local", Healthy: true},
		PlanValid:               true,
		TargetValid:             true,
		EvidenceValid:           !verificationFailed,
		CurrentStepClear:        true,
		SemanticDriftObserved:   false,
		VerificationInvalidated: verificationFailed,
		Objective:               intent,
		PendingSteps:            append([]string(nil), p.TargetFiles...),
		CheckpointID:            checkpoint,
	}
}

// currentState returns the persisted TaskState for a task.
func (e *RuntimeEngine) currentState(taskID string) (durable.TaskState, error) {
	st, ok := e.store.State(taskID)
	if !ok {
		return durable.TaskState{}, fmt.Errorf("runtime: task %q has no persisted state", taskID)
	}
	return st, nil
}

// runVerification triggers evidence verification prior to execution. A nil
// verifier means verification cannot be performed.
func runVerification(ctx context.Context, verify scopeguard.Verifier) (bool, string, error) {
	if verify == nil {
		return false, "no verifier configured", fmt.Errorf("runtime: no verifier configured")
	}
	return verify(ctx)
}

// isVerificationError reports whether an execution error stems from failed
// evidence verification (post-commit VERIFICATION_RESULT ok=false).
func isVerificationError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "verification failed")
}

// truncateDetail bounds evidence detail strings for ledger payloads.
func truncateDetail(s string) string {
	const max = 256
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
