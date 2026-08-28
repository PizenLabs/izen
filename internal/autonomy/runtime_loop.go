package autonomy

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── RuntimeState ────────────────────────────────────────────────────────────
//
// RuntimeState is the runtime-owned canonical position of the autonomous loop.
// It is a RUNTIME concept: the loop state machine is the single authority that
// moves between these positions, the UI only projects them, and the loop can
// run headless (runtime + bus only). See docs/design/PHASE/
// PHASE_4_AUTONOMOUS_RUNTIME_REPORT.md §2.1.

type RuntimeState string

const (
	// RuntimeIdle is the pre-start position.
	RuntimeIdle RuntimeState = "idle"
	// RuntimeObserving collects the bounded, structured Observation for the
	// current objective.
	RuntimeObserving RuntimeState = "observing"
	// RuntimeDeciding validates a proposed Decision before any Action.
	RuntimeDeciding RuntimeState = "deciding"
	// RuntimeExecuting is the position where the RuntimeExecutor is running one
	// ExecutionRequest. The loop never executes anything itself.
	RuntimeExecuting RuntimeState = "executing"
	// RuntimeVerifying consumes the verification outcome of the execution.
	RuntimeVerifying RuntimeState = "verifying"
	// RuntimeInterpreting maps the ExecutionResult to a RecoveryDecision or
	// continuation.
	RuntimeInterpreting RuntimeState = "interpreting"
	// RuntimeRecovering is a bounded recovery cycle (re-plan/re-scope).
	RuntimeRecovering RuntimeState = "recovering"
	// RuntimeAwaitingHuman parks the loop: the human must respond before the
	// loop advances. This is a runtime state; the UI only renders it.
	RuntimeAwaitingHuman RuntimeState = "awaiting_human"
	// RuntimeCompleted is the terminal success position.
	RuntimeCompleted RuntimeState = "completed"
	// RuntimeAborted is the terminal position: loop bounds, cancellation, or a
	// permanent runtime failure.
	RuntimeAborted RuntimeState = "aborted"
)

// IsTerminal reports whether the state is terminal (Completed/Aborted).
func (s RuntimeState) IsTerminal() bool {
	return s == RuntimeCompleted || s == RuntimeAborted
}

// String returns the canonical runtime-state label.
func (s RuntimeState) String() string {
	return string(s)
}

// ── FailureClass ────────────────────────────────────────────────────────────
//
// FailureClass mirrors the canonical EventExecutionFailed taxonomy
// (events.FailureTransient/Recoverable/Permanent) so the loop's recovery
// matrix aligns with the runtime's canonical failure classification without
// importing execution internals.

type FailureClass string

const (
	// FailureTransient is a failure that can be retried immediately.
	FailureTransient FailureClass = "transient"
	// FailureRecoverable is a failure recoverable with a re-plan/re-scope.
	FailureRecoverable FailureClass = "recoverable"
	// FailurePermanent cannot be recovered within the loop; it must terminate.
	FailurePermanent FailureClass = "permanent"
)

// ── ExecutionOutcome ────────────────────────────────────────────────────────
//
// ExecutionOutcome is the normalized, authoritative outcome of one runtime
// execution as observed by the loop. It mirrors the canonical MutationOutcome
// vocabulary. The composition-root adapter maps execution.ExecutionResult onto
// it; the loop never fabricates an outcome.

type ExecutionOutcome string

const (
	OutcomeNoArtifact       ExecutionOutcome = "no_artifact"
	OutcomeCancelled        ExecutionOutcome = "cancelled"
	OutcomeFailed           ExecutionOutcome = "failed"
	OutcomeArtifactProduced ExecutionOutcome = "artifact_produced"
	OutcomePatchGenFailed   ExecutionOutcome = "patch_generation_failed"
	// ── Extended vocabulary (Phase 5) ─────────────────────────────────
	// The extended outcomes mirror the canonical MutationOutcome strings one
	// to one so the composition-root adapter maps execution.ExecutionResult
	// outcomes onto loop outcomes without lossy reclassification. The Phase 4
	// outcomes above are retained for backward compatibility; the adapter
	// always emits the canonical strings below.
	OutcomeChanged                   ExecutionOutcome = "changed"
	OutcomeCreated                   ExecutionOutcome = "created"
	OutcomeNoChange                  ExecutionOutcome = "nochange"
	OutcomeArtifactRejected          ExecutionOutcome = "artifact_rejected"
	OutcomeArtifactRetryableRejected ExecutionOutcome = "artifact_retryable_rejected"
	OutcomeTruncated                 ExecutionOutcome = "truncated"
	// OutcomePatchFailed is the canonical MutationOutcome string ("patch_failed").
	// It differs from the Phase 4 OutcomePatchGenFailed value
	// ("patch_generation_failed"); the adapter always emits the canonical string.
	OutcomePatchFailed     ExecutionOutcome = "patch_failed"
	OutcomeApplyFailed     ExecutionOutcome = "apply_failed"
	OutcomeVerifyFailed    ExecutionOutcome = "verify_failed"
	OutcomeSkipped         ExecutionOutcome = "skipped"
	OutcomePendingApproval ExecutionOutcome = "pending_approval"
	OutcomeRejected        ExecutionOutcome = "rejected"
	OutcomeCompleted       ExecutionOutcome = "completed"
	// OutcomePreflightInfeasible is the Boundary-2 verdict (I5): the estimated
	// generation budget exceeded max_output, so execution was refused BEFORE
	// any provider request. Only an explicit human re-scope continues.
	OutcomePreflightInfeasible ExecutionOutcome = "preflight_infeasible"
	// OutcomeWorkspaceDrift is the Boundary-5 verdict: the workspace version
	// changed between attempts. The run aborts — a stale attempt never
	// re-executes over moved ground.
	OutcomeWorkspaceDrift ExecutionOutcome = "workspace_drift"
	// ── NO-OP semantic sub-states ─────────────────────────────────────────
	// A NO_CHANGES_REQUIRED answer is a model CLAIM, classified by the
	// executor's deterministic structural analysis into exactly one of three
	// sub-states. The monolithic no_op_success outcome was retired: a raw
	// claim can never terminate a run as success by itself.

	// OutcomeNoOpObjectiveSatisfied: structural analysis confirms zero work
	// is needed — the claim stands uncontradicted. Terminal success.
	OutcomeNoOpObjectiveSatisfied ExecutionOutcome = "no_op_objective_satisfied"
	// OutcomeNoOpNoSafeMutation: candidate edits detected below the safety
	// threshold. Terminal warning (requires_review) — the loop parks at a
	// human boundary instead of completing.
	OutcomeNoOpNoSafeMutation ExecutionOutcome = "no_op_no_safe_mutation"
	// OutcomeNoOpObjectiveUnresolved: the claim conflicts with structural
	// evidence. Escalation trigger — never terminal completion; the DAG
	// runtime re-hydrates the sub-task once, then escalates to a human.
	OutcomeNoOpObjectiveUnresolved ExecutionOutcome = "no_op_objective_unresolved"
)

// RecoveryStrategy names the artifact protocol of the observation's own
// attempt ("bounded_patch" after a typed FULL_REWRITE → BOUNDED_PATCH
// transition; "" for the initial full-artifact contract). The recovery matrix
// reads it as the authoritative transition-latch state.
const (
	StrategyFullArtifact = ""
	StrategyBoundedPatch = "bounded_patch"
)

// Failed reports whether the outcome represents a failed execution.
func (o ExecutionOutcome) Failed() bool {
	return o == OutcomeFailed || o == OutcomePatchGenFailed
}

// ClassifyOutcome maps a normalized execution outcome onto the canonical
// failure class used by the recovery matrix. Deterministic: identical outcomes
// always classify identically.
func ClassifyOutcome(o ExecutionOutcome) FailureClass {
	switch o {
	case OutcomeNoArtifact, OutcomeArtifactProduced, OutcomeChanged, OutcomeCreated,
		OutcomeNoChange, OutcomeCompleted, OutcomeNoOpObjectiveSatisfied,
		OutcomeNoOpNoSafeMutation, OutcomeSkipped, OutcomePendingApproval:
		// Successes and non-failure outcomes are classified as transient
		// (never permanent): the recovery matrix is only consulted for actual
		// failures, and the success/human outcomes are decided before it.
		// A no_safe_mutation warning parks at a human boundary before the
		// matrix runs; it is never a failure class.
		return FailureTransient
	case OutcomeCancelled, OutcomeRejected, OutcomeArtifactRejected:
		return FailurePermanent
	case OutcomeFailed, OutcomePatchGenFailed, OutcomePatchFailed, OutcomeApplyFailed, OutcomeVerifyFailed,
		OutcomeArtifactRetryableRejected, OutcomeTruncated:
		return FailureRecoverable
	case OutcomeNoOpObjectiveUnresolved:
		// A claim contradicted by structural evidence is a recoverable
		// condition: the loop escalates (re-hydrated context, then a human)
		// instead of aborting or — worse — completing.
		return FailureRecoverable
	default:
		return FailurePermanent
	}
}

// ── VerificationOutcome ─────────────────────────────────────────────────────

type VerificationOutcome struct {
	Passed bool
	Stage  string
}

// ── Observation ─────────────────────────────────────────────────────────────
//
// Observation is the bounded, structured context the loop is allowed to see.
// It NEVER carries raw full file contents or unbounded history. Every field is
// either authoritative (Outcome/Verification/RequestID) or bounded evidence.

type Observation struct {
	// RequestID correlates the observation to the execution that produced it.
	RequestID string
	// ContractID is the immutable contract identity of the observed execution
	// attempt (Phase 2 P2). Recovery decisions back-point at it: a repair
	// re-submits with ParentContractID = this ID so the runtime appends a
	// causally linked recovery contract instead of rewriting history.
	ContractID string
	// Intent is the classified intent (authoritative).
	Intent Intent
	// Target is the resolved mutation target (authoritative).
	Target string
	// Evidence is the bounded structural ledger (deterministic, not raw files).
	Evidence string
	// Diagnostic carries the bounded advisory of a FAILED attempt (I2
	// Recovery Isolation): WHAT failed and WHAT to do differently — the
	// gate/validation error text, never a byte of the rejected artifact. It
	// feeds self-correction contexts so a successor attempt can repair the
	// output structure instead of repeating it blind.
	Diagnostic string
	// Outcome is the normalized authoritative execution outcome.
	Outcome ExecutionOutcome
	// PatchID is the approval-held patch identity when Outcome is
	// pending_approval (authoritative). It is the ONLY handle the loop may
	// forward to the human approval boundary; the loop never inspects the patch.
	PatchID string
	// ClarificationRequired is true when the execution demanded human input
	// before any model call or mutation (no invocation occurred). The loop
	// parks at AwaitingHuman instead of treating it as a failure.
	ClarificationRequired bool
	// Verification is the verification outcome (populated post-execution).
	Verification VerificationOutcome
	// TokenUsage is the provider usage accounted by the loop for bounds.
	TokenUsage int
	// InputTokens / OutputTokens are the authoritative provider-reported
	// input/output counts for this observation (split for aggregate truth).
	InputTokens  int
	OutputTokens int
	// UsageKnown reports whether the provider reported authoritative usage for
	// this observation.
	UsageKnown bool
	// FinishReason is the provider's terminal finish_reason for this observation.
	FinishReason string
	// MaxOutputTokens is the output budget that was enforced for this attempt.
	MaxOutputTokens int
	// RecoveryStrategy names the artifact protocol of THIS attempt
	// (StrategyBoundedPatch after a typed transition; StrategyFullArtifact for
	// the initial contract). It is the authoritative I3 transition-latch
	// state: the matrix refuses a second OUTPUT_EXHAUSTED transition when it
	// is already set.
	RecoveryStrategy string
	// AttemptNum is the attempt counter for the current objective.
	AttemptNum int
	// RecoveryCycle is the recovery-cycle counter for the current objective.
	RecoveryCycle int
	// LastAction is the previous loop action (identical-decision detection).
	LastAction LoopAction
	// Escalated is set by the DAG runtime's NO-OP escalation circuit when a
	// NO_OP_OBJECTIVE_UNRESOLVED claim survived its re-hydration attempt: the
	// observation demands human escalation, never terminal completion.
	Escalated bool
}

// ── LoopAction / LoopDecision ───────────────────────────────────────────────

// LoopAction is the closed decision vocabulary of the autonomous loop.
type LoopAction string

const (
	// LoopContinue proceeds with the next step of the current objective.
	LoopContinue LoopAction = "continue"
	// LoopComplete declares the objective satisfied and terminates the loop.
	LoopComplete LoopAction = "complete"
	// LoopRetry re-executes the same request (bounded).
	LoopRetry LoopAction = "retry"
	// LoopRepair re-plans/re-scopes before re-executing (bounded recovery).
	LoopRepair LoopAction = "repair"
	// LoopAskHuman parks the loop in AwaitingHuman (runtime state).
	LoopAskHuman LoopAction = "ask_human"
	// LoopAbort terminates the loop with a failure classification.
	LoopAbort LoopAction = "abort"
)

// Valid reports whether the action is in the closed vocabulary.
func (a LoopAction) Valid() bool {
	switch a {
	case LoopContinue, LoopComplete, LoopRetry, LoopRepair, LoopAskHuman, LoopAbort:
		return true
	default:
		return false
	}
}

// LoopDecision is a proposed decision. It is VALIDATED by the runtime before
// any Action may proceed; an invalid decision is rejected, never silently
// accepted. A decision is NOT an execution: the loop can decide anything, but
// only the RuntimeExecutor executes.
type LoopDecision struct {
	Action LoopAction
	Reason string
	// PatchID is the approval-held patch identity when Action is LoopAskHuman
	// and the boundary is an approval gate. It is carried onto the
	// HumanBoundary verbatim; the loop never inspects the patch.
	PatchID string
	// Options is the candidate list for a target-ambiguity AskHuman; nil for a
	// yes/no decision.
	Options []string
}

// Valid reports whether the decision carries a legal action.
func (d LoopDecision) Valid() bool {
	return d.Action.Valid()
}

// ── RecoveryDecision / failure matrix ───────────────────────────────────────
//
// RecoverFailure maps an observation + failure class onto the runtime-owned
// recovery decision using the canonical failure matrix:
//
//	permanent            → Abort
//	transient            → Retry   (bounded by MaxAttempts)
//	recoverable          → Repair  (bounded by MaxRecoveryCycles)
//	bounds exhausted     → AskHuman
//	unknown class        → Abort
//
// Only Transient/Recoverable failures re-enter the loop; Permanent failures
// terminate. Deterministic: identical inputs always yield identical decisions.

func RecoverFailure(o Observation, class FailureClass, b LoopBounds) LoopDecision {
	attemptsExhausted := b.MaxAttempts > 0 && o.AttemptNum >= b.MaxAttempts
	cyclesExhausted := b.MaxRecoveryCycles > 0 && o.RecoveryCycle >= b.MaxRecoveryCycles

	switch class {
	case FailurePermanent:
		return LoopDecision{Action: LoopAbort, Reason: "permanent failure — cannot recover"}
	case FailureTransient:
		if attemptsExhausted {
			return LoopDecision{Action: LoopAskHuman, Reason: "transient failure; attempts exhausted — ask human"}
		}
		return LoopDecision{Action: LoopRetry, Reason: "transient failure — retry (bounded)"}
	case FailureRecoverable:
		if cyclesExhausted {
			return LoopDecision{Action: LoopAskHuman, Reason: "recoverable failure; recovery cycles exhausted — ask human"}
		}
		return LoopDecision{Action: LoopRepair, Reason: "recoverable failure — repair (bounded)"}
	default:
		return LoopDecision{Action: LoopAbort, Reason: fmt.Sprintf("unknown failure class %q", class)}
	}
}

// ── HumanBoundary ───────────────────────────────────────────────────────────
//
// HumanBoundary is the runtime condition that parks the loop in
// AwaitingHuman: an approval requirement, a target ambiguity, insufficient
// evidence for a bounded decision, or recovery exhaustion. While parked the
// loop holds its runtime state and never burns budget.
//
// Phase 6 added the presentation-enabling fields (Targets, Action, Resumable)
// so the UI can render a boundary card without sniffing internals. They are
// derived deterministically by the loop and, when set, the human resumes via
// the matching Driver.Resume* surface.

// HumanBoundaryAction discriminates the kind of human response a parked
// boundary requires.
type HumanBoundaryAction string

const (
	// HumanBoundaryApproval is an approval gate: a held patch awaits
	// Approve/Reject (resumable).
	HumanBoundaryApproval HumanBoundaryAction = "approve"
	// HumanBoundaryClarify is a target ambiguity: the human must pick a
	// candidate before any execution (resumable).
	HumanBoundaryClarify HumanBoundaryAction = "clarify"
	// HumanBoundaryInform is an informational park (recovery exhaustion,
	// bounds exhaustion): no resume decision exists — the human may start a
	// fresh bounded run.
	HumanBoundaryInform HumanBoundaryAction = "inform"
	// HumanBoundaryDecomposition is the typed DECOMPOSITION_PROPOSAL gate:
	// Boundary 2 refused the objective as preflight_infeasible and the
	// planner staged a validated ExecutionDAG. The human approves the WHOLE
	// plan (every sub-task listed on the boundary) before the atomic
	// transaction loop may run; resumable via the driver's proposal surface.
	HumanBoundaryDecomposition HumanBoundaryAction = "decomposition_proposal"
)

// String returns the canonical boundary-action label.
func (a HumanBoundaryAction) String() string { return string(a) }

type HumanBoundary struct {
	Reason string
	// RequestID identifies the parked execution when the boundary is an
	// approval gate.
	RequestID string
	// PatchID identifies the approval-held patch when the boundary is an
	// approval gate. The human resumes via Approve/Reject of this identity.
	PatchID string
	// Options is the candidate list for a target ambiguity; nil for a
	// yes/no decision.
	Options []string
	// Targets is the resolved workspace-relative target set the parked
	// execution holds (approval gates) or would hold (clarify). It is
	// authoritative for the UI: the executor authorization issued on approve
	// covers exactly these files.
	Targets []string
	// Proposal is the staged task-decomposition plan when Action is
	// HumanBoundaryDecomposition. It lists every sub-task the human is being
	// asked to authorize; approval covers ALL of them as one atomic plan.
	Proposal *planner.ExecutionDAG
	// Action discriminates the boundary kind (approve/clarify/inform/
	// decomposition_proposal).
	Action HumanBoundaryAction
	// Resumable reports whether a Resume* decision exists for this boundary.
	// An inform boundary is not resumable; only a fresh bounded run continues.
	Resumable bool
}

// DeriveBoundaryAction computes the canonical Action/Resumable from a
// boundary's fields. Deterministic: identical boundary facts always yield
// identical presentation facts.
func DeriveBoundaryAction(b *HumanBoundary) {
	if b == nil {
		return
	}
	switch {
	case b.PatchID != "":
		b.Action = HumanBoundaryApproval
		b.Resumable = true
	case b.Proposal != nil:
		b.Action = HumanBoundaryDecomposition
		b.Resumable = true
	case len(b.Options) > 0:
		b.Action = HumanBoundaryClarify
		b.Resumable = true
	default:
		b.Action = HumanBoundaryInform
		b.Resumable = false
	}
}

// ── LoopBounds / LoopTermination ────────────────────────────────────────────
//
// The RUNTIME owns termination: every bound below is enforced by the loop,
// never by the model. See docs/design/PHASE/PHASE_4_AUTONOMOUS_RUNTIME_REPORT.md
// §2.8.

type LoopBounds struct {
	// MaxAttempts caps executions per objective.
	MaxAttempts int
	// MaxRecoveryCycles caps re-plan/re-scope cycles.
	MaxRecoveryCycles int
	// MaxExecutionSteps caps mutated steps in one loop run.
	MaxExecutionSteps int
	// MaxIdenticalDecisions caps consecutive identical continue/retry/repair
	// decisions — detects a pathological repair→fail→repair loop.
	MaxIdenticalDecisions int
	// MaxTotalTokens caps provider usage across the loop run.
	MaxTotalTokens int
}

// DefaultLoopBounds returns the standard runtime-owned termination bounds.
func DefaultLoopBounds() LoopBounds {
	return LoopBounds{
		MaxAttempts:           3,
		MaxRecoveryCycles:     2,
		MaxExecutionSteps:     10,
		MaxIdenticalDecisions: 2,
		MaxTotalTokens:        8000,
	}
}

// LoopTermination is the terminal outcome of a loop run.
type LoopTermination struct {
	State  RuntimeState // RuntimeCompleted or RuntimeAborted
	Reason string
	Class  FailureClass
}

// RuntimeTransition records one observable step of the loop: the from/to
// states, the action, and the reason the runtime moved.
type RuntimeTransition struct {
	From   RuntimeState
	To     RuntimeState
	Action LoopAction
	Reason string
}

// String renders the transition compactly (e.g. "observing -> executing").
func (t RuntimeTransition) String() string {
	return fmt.Sprintf("%s -> %s (%s)", t.From, t.To, t.Action)
}

// ── Executor port (composition-boundary adapter) ────────────────────────────
//
// LoopRequest is the normalized execution request the loop issues. The
// composition root maps it onto execution.ExecuteRequest and binds the
// resulting Executor to the RuntimeExecutor. The loop NEVER reaches the
// provider, the PatchManager, or the filesystem directly.

type LoopRequest struct {
	// RequestID correlates the execution's lifecycle events and observation.
	// Empty yields a deterministic auto-generated ID inside the runtime.
	RequestID        string
	Prompt           string
	Target           string
	Targets          []string
	Evidence         string
	Intent           string
	IntentConfidence float64
	TargetConfidence float64
	Scope            string
	// RecoveryAttempt is the 1-indexed attempt number for this request (0 = initial).
	RecoveryAttempt int
	// RecoveryReason is the human-readable reason for a recovery re-execution.
	RecoveryReason string
	// RecoveryStrategy is the explicit strategy change for this recovery attempt
	// (e.g. "bounded_patch" after a truncation). Empty for the initial attempt.
	RecoveryStrategy string
	// ParentContractID is the CAUSAL RECOVERY pointer (Phase 2 P2): the failed
	// parent ContractID this request continues from. The executor resolves it
	// at admission — pure retries stay under the same contract identity,
	// material changes append a new causally linked contract — and enforces
	// the bounded chain limit there.
	ParentContractID string
	// MaxOutputTokens is an explicit output budget override for this attempt (0 = use profile default).
	MaxOutputTokens int
	// WorkspaceDigest is the Boundary-5 workspace version
	// SHA256(Σ path(f)+hash(f)) captured when the run resolved its targets.
	// The adapter re-validates it BEFORE submitting the execution: any
	// out-of-band change between attempts returns a workspace_drift
	// observation with ZERO provider requests.
	WorkspaceDigest string
	// StagedPlan carries the approved decomposition plan when this request is
	// ONE sub-task of a staged ExecutionDAG. The adapter projects its sub-task
	// windows onto the executor's Boundary-2 preflight so every unit is
	// evaluated individually and the monolithic full-rewrite estimation of
	// the original target is suppressed. Nil for non-DAG requests.
	StagedPlan *planner.ExecutionDAG
	// ProposalIntent carries the human-selected interactive proposal strategy
	// (Phase 2 proposal gateway) injected into the execution-context
	// constraints for this attempt. It is pure data selected on the TUI
	// decision surface; the runtime consumes it to bound the authorized DAG.
	// Empty when no interactive proposal was selected.
	ProposalIntent string
	// FocusStartLine / FocusEndLine optionally pin the executor's
	// deterministic bounded-patch context window to THIS unit's assigned
	// inclusive 1-indexed line interval. Under a staged decomposition plan
	// each sub-task sets these to its Region: retries can then neither see
	// nor anchor on another unit's content — the local-context blindness that
	// produced false NO_CHANGES_REQUIRED claims. Zero values mean "no focus"
	// (the whole file remains windowable, the pre-DAG behavior).
	FocusStartLine int
	FocusEndLine   int
	// NoOpEscalation marks this request as a NO-OP escalation attempt: the
	// previous attempt for this unit answered NO_CHANGES_REQUIRED while
	// structural analysis indicated work remains. The executor widens the
	// bounded-patch context window for the re-hydrated judgment.
	NoOpEscalation bool
	// FinishReason carries the previous attempt's finish_reason for observability.
	FinishReason string
	// StreamCallback is an optional callback for incremental streaming progress.
	// When set, the executor invokes it during provider streaming for each
	// content delta, first token, and completion.
	StreamCallback execution.StreamCallback
}

// Executor is the ONLY authority the loop may invoke. The loop is a consumer
// of the RuntimeExecutor — never an executor itself.
type Executor interface {
	Execute(ctx context.Context, req LoopRequest) (Observation, error)
}

// ── RuntimeLoop ─────────────────────────────────────────────────────────────
//
// RuntimeLoop is the runtime-owned bounded loop state machine. It owns loop
// position, bounds accounting, and termination. It has NO execution authority:
// it only validates decisions, consumes observations, and records transitions.

type RuntimeLoop struct {
	state           RuntimeState
	bounds          LoopBounds
	history         []RuntimeTransition
	attempts        int
	recoveryCycles  int
	steps           int
	tokens          int
	identical       int
	lastAction      LoopAction
	termination     *LoopTermination
	boundary        *HumanBoundary
	lastObservation Observation
}

// NewRuntimeLoop returns a loop at Idle with the given runtime-owned bounds.
func NewRuntimeLoop(bounds LoopBounds) *RuntimeLoop {
	return &RuntimeLoop{state: RuntimeIdle, bounds: bounds}
}

// State returns the current loop position.
func (l *RuntimeLoop) State() RuntimeState {
	if l == nil {
		return RuntimeIdle
	}
	return l.state
}

// Bounds returns the runtime-owned termination bounds.
func (l *RuntimeLoop) Bounds() LoopBounds {
	if l == nil {
		return DefaultLoopBounds()
	}
	return l.bounds
}

// WidenBounds raises the runtime-owned bounds to at least the given floors.
// It exists for HUMAN-APPROVED plans (the DECOMPOSITION_PROPOSAL gate): a
// proposal of N sub-tasks legitimately exceeds the single-objective defaults,
// and the human consented to exactly that scope when approving. Bounds are
// never lowered — widening only reserves headroom for the consented plan.
func (l *RuntimeLoop) WidenBounds(minAttempts, minSteps, minTokens, minIdentical int) {
	if l == nil {
		return
	}
	if l.bounds.MaxAttempts < minAttempts {
		l.bounds.MaxAttempts = minAttempts
	}
	if l.bounds.MaxExecutionSteps < minSteps {
		l.bounds.MaxExecutionSteps = minSteps
	}
	if l.bounds.MaxTotalTokens < minTokens {
		l.bounds.MaxTotalTokens = minTokens
	}
	if l.bounds.MaxIdenticalDecisions < minIdentical {
		l.bounds.MaxIdenticalDecisions = minIdentical
	}
}

// Attempts returns the execution-attempt counter for the current objective.
func (l *RuntimeLoop) Attempts() int {
	if l == nil {
		return 0
	}
	return l.attempts
}

// RecoveryCycles returns the recovery-cycle counter for the current objective.
func (l *RuntimeLoop) RecoveryCycles() int {
	if l == nil {
		return 0
	}
	return l.recoveryCycles
}

// History returns the observed transitions, oldest first.
func (l *RuntimeLoop) History() []RuntimeTransition {
	if l == nil {
		return nil
	}
	out := make([]RuntimeTransition, len(l.history))
	copy(out, l.history)
	return out
}

// Termination returns the terminal outcome, or nil while the loop is running.
func (l *RuntimeLoop) Termination() *LoopTermination {
	if l == nil {
		return nil
	}
	return l.termination
}

// Boundary returns the active human boundary, or nil while the loop is not
// parked in AwaitingHuman.
func (l *RuntimeLoop) Boundary() *HumanBoundary {
	if l == nil {
		return nil
	}
	return l.boundary
}

// Start moves Idle → Observing.
func (l *RuntimeLoop) Start(reason string) RuntimeState {
	if l == nil || l.state != RuntimeIdle {
		return l.State()
	}
	return l.push(LoopContinue, RuntimeObserving, reason)
}

// Observe moves Observing → Deciding and records the bounded observation as
// the current context. Bounds are enforced at the decision position, never
// here.
func (l *RuntimeLoop) Observe(o Observation) RuntimeState {
	if l == nil || l.state != RuntimeObserving {
		return l.State()
	}
	l.lastObservation = o
	return l.push(LoopContinue, RuntimeDeciding, "observation consumed")
}

// Step advances the loop by one bounded decision step.
//
//   - From Deciding, the decision is validated and applied: Continue/Retry/
//     Repair → Executing, Complete → Completed, AskHuman → AwaitingHuman,
//     Abort → Aborted.
//   - From Interpreting, the same vocabulary applies (recovery decisions are
//     legal here).
//   - From Recovering, Continue/Retry/Repair → Executing, AskHuman →
//     AwaitingHuman, Abort → Aborted.
//   - From AwaitingHuman only Abort is legal; re-entry happens via
//     ReleaseHuman.
//
// Bounds are enforced at execution-bound decision positions (Continue/Retry/
// Repair): when a bound is violated the loop terminates with RuntimeAborted
// before the next execution. Non-executing decisions (Complete/AskHuman/Abort)
// are never budget-gated. A cancelled context terminates with FailurePermanent.
// Terminal loops reject further steps. An invalid or illegal decision is
// rejected, never silently accepted.
func (l *RuntimeLoop) Step(ctx context.Context, d LoopDecision) (RuntimeState, error) {
	if l == nil {
		return "", errors.New("autonomy: nil runtime loop")
	}
	if l.state.IsTerminal() {
		return l.state, errors.New("autonomy: loop already terminated at " + string(l.state))
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return l.terminate(LoopAbort, RuntimeAborted,
				"context cancelled", FailurePermanent), nil
		default:
		}
	}
	if !d.Valid() {
		return l.state, fmt.Errorf("autonomy: invalid decision action %q", d.Action)
	}

	// Recovery cycles are bounded at DECISION time: a repair that would cross
	// the bound is rejected before it consumes a cycle.
	if d.Action == LoopRepair {
		b := l.bounds
		if b.MaxRecoveryCycles > 0 && l.recoveryCycles >= b.MaxRecoveryCycles {
			return l.terminate(LoopAbort, RuntimeAborted,
				fmt.Sprintf("max recovery cycles (%d) exhausted", b.MaxRecoveryCycles),
				FailurePermanent), nil
		}
	}

	// Enforce remaining bounds at decision positions — but ONLY for
	// execution-bound decisions (Continue/Retry/Repair). Bounds gate the NEXT
	// execution, never a legitimate completion or a human park: a Complete
	// decision is honored exactly at the attempt bound, and RecoverFailure's
	// "bounds exhausted → AskHuman" parks the loop instead of aborting it.
	executionBound := d.Action == LoopContinue || d.Action == LoopRetry || d.Action == LoopRepair
	if executionBound {
		if t := l.violation(); t != nil {
			return l.terminate(LoopAbort, t.State, t.Reason, t.Class), nil
		}
	}

	if !l.legalFrom(l.state, d.Action) {
		return l.state, fmt.Errorf("autonomy: action %s is not legal from state %s", d.Action, l.state)
	}

	next := l.applyDecision(d)
	if !next.IsTerminal() && executionBound {
		if t := l.violation(); t != nil {
			return l.terminate(LoopAbort, t.State, t.Reason, t.Class), nil
		}
	}
	return next, nil
}

// ConsumeExecution moves Executing → Verifying once the executor returned a
// result. It records the attempt, step and token counters (runtime-owned
// bounds) from the authoritative observation.
func (l *RuntimeLoop) ConsumeExecution(o Observation) RuntimeState {
	if l == nil || l.state != RuntimeExecuting {
		return l.State()
	}
	l.attempts++
	l.steps++
	l.tokens += o.TokenUsage
	l.lastObservation = o
	return l.push(LoopContinue, RuntimeVerifying, "execution consumed: "+string(o.Outcome))
}

// ConsumeVerification moves Verifying → Interpreting once the verification
// result is in.
func (l *RuntimeLoop) ConsumeVerification(o Observation) RuntimeState {
	if l == nil || l.state != RuntimeVerifying {
		return l.State()
	}
	return l.push(LoopContinue, RuntimeInterpreting, "verification consumed")
}

// applyDecision applies a validated decision from a decision position. The
// transition is recorded with the true from-state (push captures it before
// moving), so the history/event projection never shows self-transitions.
func (l *RuntimeLoop) applyDecision(d LoopDecision) RuntimeState {
	l.recordUsage(d)

	switch d.Action {
	case LoopContinue, LoopRetry:
		l.push(d.Action, RuntimeExecuting, d.Reason)
	case LoopRepair:
		l.recoveryCycles++
		l.push(d.Action, RuntimeRecovering, d.Reason)
	case LoopComplete:
		l.terminate(LoopComplete, RuntimeCompleted, d.Reason, "")
	case LoopAskHuman:
		l.push(d.Action, RuntimeAwaitingHuman, d.Reason)
		b := &HumanBoundary{Reason: d.Reason, PatchID: d.PatchID, Options: d.Options}
		DeriveBoundaryAction(b)
		l.boundary = b
	case LoopAbort:
		l.terminate(LoopAbort, RuntimeAborted, d.Reason, FailurePermanent)
	}
	return l.state
}

// AwaitHuman parks the loop at AwaitingHuman with an explicit boundary. The
// loop holds state and burns no budget until Released or Aborted.
func (l *RuntimeLoop) AwaitHuman(b HumanBoundary) RuntimeState {
	if l == nil || l.state.IsTerminal() {
		return l.State()
	}
	DeriveBoundaryAction(&b)
	l.boundary = &b
	return l.push(LoopAskHuman, RuntimeAwaitingHuman, b.Reason)
}

// ReleaseHuman re-enters the loop from AwaitingHuman back to Observing after
// the human responded. The response is authoritative; the loop never guesses
// a human decision.
func (l *RuntimeLoop) ReleaseHuman(reason string) RuntimeState {
	if l == nil || l.state != RuntimeAwaitingHuman {
		return l.State()
	}
	l.boundary = nil
	return l.push(LoopContinue, RuntimeObserving, reason)
}

// Complete terminates the loop successfully.
func (l *RuntimeLoop) Complete(reason string) (RuntimeState, *LoopTermination) {
	if l == nil {
		return RuntimeIdle, nil
	}
	term := &LoopTermination{State: RuntimeCompleted, Reason: reason}
	l.terminate(LoopComplete, term.State, term.Reason, term.Class)
	return l.state, term
}

// Abort terminates the loop with a failure classification. This is the only
// runtime-owned termination path for a non-completed loop.
func (l *RuntimeLoop) Abort(reason string, class FailureClass) (RuntimeState, *LoopTermination) {
	if l == nil {
		return RuntimeIdle, nil
	}
	term := &LoopTermination{State: RuntimeAborted, Reason: reason, Class: class}
	l.terminate(LoopAbort, term.State, term.Reason, term.Class)
	return l.state, term
}

// ── internal accounting ─────────────────────────────────────────────────────

func (l *RuntimeLoop) recordUsage(d LoopDecision) {
	if d.Action == LoopContinue || d.Action == LoopRetry || d.Action == LoopRepair {
		if d.Action == l.lastAction {
			l.identical++
		} else {
			l.identical = 1
		}
	} else {
		l.identical = 0
	}
	l.lastAction = d.Action
}

// violation returns a terminal outcome when any runtime-owned bound is crossed.
func (l *RuntimeLoop) violation() *LoopTermination {
	b := l.bounds
	if b.MaxAttempts > 0 && l.attempts >= b.MaxAttempts {
		return &LoopTermination{
			State:  RuntimeAborted,
			Reason: fmt.Sprintf("max attempts (%d) exhausted", b.MaxAttempts),
			Class:  FailurePermanent,
		}
	}
	if b.MaxExecutionSteps > 0 && l.steps >= b.MaxExecutionSteps {
		return &LoopTermination{
			State:  RuntimeAborted,
			Reason: fmt.Sprintf("max execution steps (%d) exhausted", b.MaxExecutionSteps),
			Class:  FailurePermanent,
		}
	}
	if b.MaxIdenticalDecisions > 0 && l.identical > b.MaxIdenticalDecisions {
		return &LoopTermination{
			State:  RuntimeAborted,
			Reason: fmt.Sprintf("repeated identical decisions (%d) — pathological loop detected", l.identical),
			Class:  FailurePermanent,
		}
	}
	if b.MaxTotalTokens > 0 && l.tokens > b.MaxTotalTokens {
		return &LoopTermination{
			State:  RuntimeAborted,
			Reason: fmt.Sprintf("max total tokens (%d) exhausted", b.MaxTotalTokens),
			Class:  FailurePermanent,
		}
	}
	return nil
}

// legalFrom reports whether the action is legal from the given state.
func (l *RuntimeLoop) legalFrom(s RuntimeState, a LoopAction) bool {
	switch s {
	case RuntimeDeciding, RuntimeInterpreting:
		return a.Valid()
	case RuntimeRecovering:
		return a == LoopContinue || a == LoopRetry || a == LoopRepair ||
			a == LoopAskHuman || a == LoopAbort
	case RuntimeObserving:
		return false // must Observe first
	case RuntimeAwaitingHuman:
		return a == LoopAbort // re-entry is handled by ReleaseHuman
	default:
		return false
	}
}

// push records one transition and moves the loop.
func (l *RuntimeLoop) push(a LoopAction, to RuntimeState, reason string) RuntimeState {
	from := l.state
	l.history = append(l.history, RuntimeTransition{From: from, To: to, Action: a, Reason: reason})
	l.state = to
	return l.state
}

// terminate records the terminal transition and freezes the loop. If the loop
// is already at the given terminal state it records the transition only.
func (l *RuntimeLoop) terminate(a LoopAction, to RuntimeState, reason string, class FailureClass) RuntimeState {
	l.termination = &LoopTermination{State: to, Reason: reason, Class: class}
	if l.state != to {
		l.push(a, to, reason)
	} else {
		l.history = append(l.history, RuntimeTransition{From: l.state, To: to, Action: a, Reason: reason})
	}
	l.state = to
	return l.state
}
