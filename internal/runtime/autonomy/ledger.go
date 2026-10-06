package autonomy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/domain/command"
	"github.com/PizenLabs/izen/internal/parser"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ── DURABLE EXECUTION LEDGER (RFC v1.0 §10/§13/§15) ─────────────────────────
//
// The Driver already owns the authoritative lifecycle of an objective: it is
// the only component that knows when a run starts, when the completion
// authority PROVED it, when a step was observed, and when the loop stopped for
// a reason that was not success. The append-only ledger under .izen/runtime is
// where that lifecycle must be recorded, because a process that dies mid
// mutation leaves no memory of it anywhere else.
//
// Everything here is OPTIONAL. With no ledger bound the driver behaves exactly
// as it always did — the store is a witness, never a participant in the loop's
// decisions, and a refused append can never change what the loop does next.

// WithLedger binds the authoritative append-only execution ledger to the
// driver, together with the process session identity that scopes task keys.
//
// The session identity is required, not decorative: the task id is derived
// from (session, objective), so two sessions running the same objective in one
// workspace stay two separate durable tasks instead of one interleaved mess.
// Pass an empty id only when the caller genuinely has no session — the key then
// degrades to the objective alone.
//
// A nil store is a no-op: every helper below short-circuits.
func WithLedger(store *durable.TaskStore, sessionID string) Option {
	return func(d *Driver) {
		if d == nil || store == nil {
			return
		}
		d.ledger = store
		d.ledgerSession = sessionID
	}
}

// ledgerTaskID derives the stable identity of one objective's durable task.
// It is a pure function of the session and the objective text, so a resumed or
// repeated run of the SAME objective attaches to the SAME task instead of
// minting a second, competing record for identical work.
func (d *Driver) ledgerTaskID(objective string) string {
	sum := sha256.Sum256([]byte(d.ledgerSession + "\x00" + objective))
	return "obj-" + hex.EncodeToString(sum[:8])
}

// ledgerProvenance resolves the scope provenance this objective was authorized
// under, from the SAME parser and the SAME call the IntentGateway and the UI
// use. It does not invent a provenance value and does not re-derive one from
// the run's own history: the durable task inherits exactly the grant the human
// bound, or the read-only zero value when the objective carried no directive.
func (d *Driver) ledgerProvenance(objective string) domain.ScopeProvenance {
	ast, err := parser.ParseInWorkspace(objective, nil, command.WorkspaceBuild)
	if err != nil || ast == nil {
		return domain.ScopeNone
	}
	return ast.ScopeProvenance
}

// ledgerReady reports whether this driver can and should write: a store is
// bound and the current run owns a task.
func (d *Driver) ledgerReady() bool {
	return d != nil && d.ledger != nil && d.ledgerTask != ""
}

// ledgerOperationID is the stable causal identity of the run's current unit of
// work. It is derived from the parent request id and the loop's own attempt
// counter, so it survives a resume and never collides across attempts.
func (d *Driver) ledgerOperationID() string {
	op := d.runRequestID
	if op == "" {
		op = "run"
	}
	if d.loop == nil {
		return op
	}
	return fmt.Sprintf("%s-attempt-%d", op, d.loop.Attempts())
}

// ledgerFail routes a store error onto the runtime's own diagnostic sink. A
// ledger that cannot record MUST NOT stop the loop: the workspace mutation
// already happened, and losing the run over the loss of its record would be
// the worse outcome. It must, however, be visible — a silent drop is how a
// journal rots unnoticed.
func ledgerFail(format string, args ...any) {
	diagnosticf("[ledger] "+format, args...)
}

// ledgerBeginRun opens the objective's durable task. An unfinished task for the
// same objective — one left behind by an interrupted run — is CONTINUED, never
// duplicated: re-running an objective must not erase the record of the run it
// is continuing.
func (d *Driver) ledgerBeginRun(objective string) {
	if d == nil || d.ledger == nil {
		return
	}
	id := d.ledgerTaskID(objective)
	d.ledgerTask = id
	d.ledgerLastState = ""
	d.ledgerCommittedOp = ""
	d.ledgerPendingOp = ""
	if st, ok := d.ledger.State(id); ok && !st.Status.Terminal() {
		return
	}
	if _, err := d.ledger.CreateTaskWithProvenance(
		id,
		objective,
		append([]string(nil), d.objectiveTargets()...),
		d.ledgerProvenance(objective),
	); err != nil {
		ledgerFail("TASK_CREATED refused for %s: %v", id, err)
		d.ledgerTask = ""
	}
}

// ledgerExecutionCommitted records that the objective completion authority
// PROVED the objective, so the work it authorized is committed in the
// workspace. This is the execution-truth boundary and the store fsyncs it.
//
// It is deliberately NOT terminal. The authority's verdict is one gate; the
// behavioral proof gate and the loop's own transition run after it and may
// still downgrade the completion. The task is retired by ledgerTerminal, once
// the loop has actually reached its terminal state.
func (d *Driver) ledgerExecutionCommitted() {
	if !d.ledgerReady() {
		return
	}
	op := d.ledgerPendingOp
	if op == "" {
		op = d.ledgerOperationID()
	}
	// The mutation boundary already recorded this operation's commit with the
	// observed post-state. Objective PROVEN is a different fact (the OUTCOME
	// contract), never a second mutation commit; appending another truth
	// boundary for the same operation would claim the operation committed
	// twice.
	if d.ledgerCommittedOp == op {
		return
	}
	if err := d.ledger.CommitExecution(d.ledgerTask, op); err != nil {
		ledgerFail("EXECUTION_COMMITTED refused for %s: %v", d.ledgerTask, err)
		return
	}
	d.ledgerCommittedOp = op
}

// ledgerMutationPrepared durably records the workspace PRE-state BEFORE a
// side-effecting dispatch, so a crash between the mutation and its commit
// marker is reconcilable from the surviving digest. It returns the recorded
// pre-digest ("" when no ledger is bound, when the scope is empty, or when the
// digest could not be computed — in which case no cursor is dispatched and the
// post-crash state honestly remains UNKNOWN rather than SAFE_RETRY).
//
// Dispatch alone NEVER means the mutation committed: it is the durable
// `PREPARED` boundary the commit marker later completes.
func (d *Driver) ledgerMutationPrepared(scope []string) string {
	if !d.ledgerReady() {
		return ""
	}
	pre, ok := d.cursorDigest(scope)
	if !ok {
		return ""
	}
	op := d.ledgerOperationID()
	cursor := durable.ExecutionCursor{
		TaskID:             d.ledgerTask,
		StepID:             op,
		OperationID:        op,
		PreconditionDigest: pre,
		// The postcondition is deliberately unestablished at dispatch time:
		// this path has no execution proof yet, and a fabricated one would let
		// a fresh runtime skip work that never happened. It is recorded by
		// ledgerMutationCommitted once the mutation is observed to have landed.
		PostconditionDigest: "",
	}
	if err := d.ledger.DispatchCursor(cursor); err != nil {
		ledgerFail("CURSOR_DISPATCHED refused for %s: %v", d.ledgerTask, err)
		return ""
	}
	// Bind the later commit to THIS cursor. The loop's attempt counter can
	// move between the dispatch and the commit (it is a progress counter, not
	// an operation identity), so the operation id is latched here rather than
	// recomputed at commit time.
	d.ledgerPendingOp = op
	return pre
}

// ledgerMutationCommitted durably records that a mutation LANDED, together
// with the observed POST-state. It is the commit marker the R6-C gap lacked:
// it is written after the workspace mutation and is independent of whether the
// objective was later PROVEN, because "the mutation committed" and "the
// objective was satisfied" are different facts. It fsyncs (EXECUTION_COMMITTED
// is a truth boundary).
func (d *Driver) ledgerMutationCommitted(scope []string) {
	if !d.ledgerReady() {
		return
	}
	op := d.ledgerPendingOp
	if op == "" {
		op = d.ledgerOperationID()
	}
	if d.ledgerCommittedOp == op {
		return
	}
	post, ok := d.cursorDigest(scope)
	if !ok {
		post = ""
	}
	if err := d.ledger.CommitExecutionWithDigest(d.ledgerTask, op, post); err != nil {
		ledgerFail("EXECUTION_COMMITTED refused for %s: %v", d.ledgerTask, err)
		return
	}
	d.ledgerCommittedOp = op
}

// mutationObservedCommitted reports whether an observation is positive
// evidence that a workspace mutation actually landed. Changed and Created are
// the ONLY outcomes that prove a real filesystem change; a no-op, a pending
// approval, a failure and a control-plane refusal are NOT commits. Absence of
// a mutation is never rounded up to a commit marker.
func mutationObservedCommitted(obs autonomy.Observation) bool {
	switch obs.Outcome {
	case autonomy.OutcomeChanged, autonomy.OutcomeCreated:
		return true
	default:
		return false
	}
}

// cursorDigest computes the durable worktree digest over the run's declared
// scope, rooted at the adapter's workspace. It uses the SAME digest function
// the reconciliation reads use (durable.ComputeTreeDigest), so a pre-digest
// recorded here and the digest a fresh runtime computes later are directly
// comparable. ok is false when the digest is unavailable.
func (d *Driver) cursorDigest(scope []string) (string, bool) {
	if d == nil || d.adapter == nil {
		return "", false
	}
	root := d.adapter.Root()
	if strings.TrimSpace(root) == "" {
		return "", false
	}
	digest, err := durable.ComputeTreeDigest(root, scope...)
	if err != nil || strings.TrimSpace(digest) == "" {
		return "", false
	}
	return digest, true
}

// ReconcileInterrupted is the FRESH-RUNTIME reconciliation surface. It reads
// the durable mutation evidence of every task in the bound ledger, compares it
// against the live workspace digest, and returns the per-task decision
// (ALREADY_COMMITTED / SAFE_RETRY / CONFLICT / UNKNOWN). It is READ-ONLY: it
// never resumes, retries or mutates anything. A decision is a fact the caller
// may act on at a human boundary — SAFE_RETRY does NOT mean "retry now", and
// ALREADY_COMMITTED does NOT mean "the objective is PROVEN".
func (d *Driver) ReconcileInterrupted() ([]durable.CursorInspection, error) {
	if d == nil || d.ledger == nil {
		return nil, fmt.Errorf("durable: reconcile requires a bound ledger")
	}
	root := ""
	if d.adapter != nil {
		root = d.adapter.Root()
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("durable: reconcile requires a workspace root")
	}
	// Snapshot scopes BEFORE InspectCursors: it holds the store lock while
	// invoking the digest func, so consulting the store here would deadlock.
	scopes := d.ledger.TaskScopes()
	return d.ledger.InspectCursors(func(taskID string) (string, error) {
		if scope, ok := scopes[taskID]; ok && len(scope) > 0 {
			return durable.ComputeTreeDigest(root, scope...)
		}
		return durable.ComputeTreeDigest(root)
	})
}

// ledgerObserved records the outcome of one executed step. A step that failed
// to produce evidence is recorded as a failed verification, never as a silent
// gap: the next process must be able to tell "it tried and failed" from "it
// never ran".
func (d *Driver) ledgerObserved(obs autonomy.Observation) {
	if !d.ledgerReady() {
		return
	}
	detail := "outcome=" + string(obs.Outcome)
	if stage := strings.TrimSpace(obs.Verification.Stage); stage != "" {
		detail += " stage=" + boundedLedgerDetail(stage)
	}
	detail += fmt.Sprintf(" verified=%t", obs.Verification.Passed)
	// A failed step is never a success, and a step whose result the workspace
	// did not verify is never a success either.
	ok := !obs.Outcome.Failed() && obs.Verification.Passed
	if err := d.ledger.RecordVerification(d.ledgerTask, d.ledgerOperationID(), ok, detail); err != nil {
		ledgerFail("VERIFICATION_RESULT refused for %s: %v", d.ledgerTask, err)
	}
}

// ledgerCheckpoint snapshots the materialized view at a loop boundary. It is
// the point at which "where was this run when the process stopped" becomes
// readable without replaying the whole journal.
func (d *Driver) ledgerCheckpoint(state autonomy.RuntimeState) {
	if !d.ledgerReady() {
		return
	}
	id := fmt.Sprintf("%s-%s", d.ledgerOperationID(), strings.ToLower(string(state)))
	if err := d.ledger.Checkpoint(d.ledgerTask, id); err != nil {
		ledgerFail("CHECKPOINT_CREATED refused for %s: %v", d.ledgerTask, err)
	}
}

// ledgerTerminal records how a run ended, exactly once per state transition,
// with the truthful reason.
//
//   - COMPLETED: the authority proved it and the loop reached completion.
//   - ANY TERMINAL NON-SUCCESS (aborted, unsubstantiated): classified first,
//     then retired. The classification is what a recovery has to read.
//   - ANYTHING ELSE (a run parked at a human boundary): PAUSED, not finished.
//     A parked loop has no termination — the human owns the next decision, and
//     the next process must still surface the task.
//
// A run that ends any other way stays non-terminal on purpose: an unfinished
// objective is the one thing a restart is required to remember.
func (d *Driver) ledgerTerminal(term *autonomy.LoopTermination) {
	if !d.ledgerReady() {
		return
	}
	state := autonomy.RuntimeState("")
	reason := ""
	if term != nil {
		state, reason = term.State, term.Reason
	} else {
		// A parked run has no termination: it is not over.
		if d.loop == nil || d.loop.State() != autonomy.RuntimeAwaitingHuman {
			return
		}
		state = autonomy.RuntimeAwaitingHuman
		if b := d.loop.Boundary(); b != nil {
			reason = b.Reason
		}
	}
	// Every return path funnels through term(), and several of them fire
	// within one run. Re-recording an unchanged state would append the same
	// transition twice; a genuine state change must always be recorded.
	if d.ledgerLastState == state {
		return
	}
	d.ledgerLastState = state

	reason = boundedLedgerDetail(reason)
	if strings.TrimSpace(reason) == "" {
		reason = "loop terminated with no recorded reason"
	}
	op := d.ledgerOperationID()
	switch {
	case state == autonomy.RuntimeCompleted:
		if err := d.ledger.RecordTerminalResult(d.ledgerTask, op, true, reason); err != nil {
			ledgerFail("terminal VERIFICATION_RESULT refused for %s: %v", d.ledgerTask, err)
		}
	case state.IsTerminal():
		if err := d.ledger.RecordFailure(d.ledgerTask, string(state), op, reason); err != nil {
			ledgerFail("FAILURE_CLASSIFIED refused for %s: %v", d.ledgerTask, err)
		}
		if err := d.ledger.RecordTerminalResult(d.ledgerTask, op, false, reason); err != nil {
			ledgerFail("terminal VERIFICATION_RESULT refused for %s: %v", d.ledgerTask, err)
		}
	default:
		if err := d.ledger.PauseTask(d.ledgerTask, reason); err != nil {
			ledgerFail("TASK_PAUSED refused for %s: %v", d.ledgerTask, err)
		}
	}
}

// ledgerDetailLimit bounds what a provider-derived string may write into the
// journal. The ledger is a record of what happened, not a transcript: an
// unbounded model answer must never be able to dominate the file.
const ledgerDetailLimit = 512

func boundedLedgerDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= ledgerDetailLimit {
		return s
	}
	return s[:ledgerDetailLimit] + "…"
}
