package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PizenLabs/izen/internal/app"
	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/domain/command"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/parser"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ── DURABLE EXECUTION LEDGER FOR THE HEADLESS `izen run` PATH ──────────────
//
// `izen run` builds its own pipeline instead of going through compose.Wire, so
// it used to leave no reconstructable execution state at all: the audit log it
// writes has no reader and no fold, and a process killed mid-cycle took every
// fact about what it had done with it. This file binds the headless cycle to
// the SAME authoritative ledger the TUI Driver writes (durable.TaskStore under
// <dir>/.izen/runtime/), with the SAME event vocabulary and the SAME task
// lifecycle. There is no second journal and no second persistence path.
//
// FAILURE HANDLING — one rule, applied consistently:
//
//   - OPEN/REPLAY fails ⇒ FAIL CLOSED. The store cannot be constructed or its
//     journal cannot be folded back into state, so the run aborts before the
//     pipeline can dispatch anything. This matches compose.Wire exactly: a
//     runtime that cannot be reconstructed after an interruption must not be
//     allowed to mutate a workspace in the first place.
//   - An individual APPEND is refused after the run started ⇒ DEGRADE, loudly.
//     By then the workspace may already hold the cycle's mutations; aborting the
//     process over the loss of its record would be the worse outcome, but the
//     refusal is printed to stderr and never swallowed. This mirrors the TUI
//     Driver's ledgerFail sink.

const runLedgerDetailLimit = 512

// runLedger witnesses one headless `izen run` cycle into the durable
// execution ledger. Every method appends through the store's existing API; the
// helper owns no event vocabulary of its own.
type runLedger struct {
	store *durable.TaskStore
	// taskID is the durable identity of THIS invocation's cycle.
	taskID string
	// operationID is the causal identity of the single operation the cycle
	// dispatches (the run contract is exactly one cycle per invocation).
	operationID string
	// scope is the target scope declared on the command line.
	scope []string
}

// newRunLedger opens the ledger under dir (the same root convention as
// compose.Wire: <dir>/.izen/runtime/) and creates the durable task for this
// objective.
//
// Task identity is scoped to the INVOCATION, not to the objective text: two
// `izen run` invocations over the same workspace are two executions, and
// collapsing them into one task would erase the first one's record — exactly
// what the autonomy Driver's session scoping exists to prevent. An unfinished
// task from an earlier invocation therefore stays PAUSED and recoverable while
// this invocation opens its own.
func newRunLedger(dir, objective string, scope []string) (*runLedger, error) {
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		return nil, fmt.Errorf("open durable execution ledger: %w", err)
	}
	// A journal whose sequence does not add up is reported, never silently
	// folded: the caller decides what an incomplete record means for this
	// workspace. Same rule as compose.Wire.
	for _, f := range store.SequenceFaults() {
		fmt.Fprintf(os.Stderr, "izen run: durable ledger %s %s at sequence %d (expected %d)\n",
			store.LedgerPath(), f.Kind, f.Observed, f.Expected)
	}

	invocation := events.NewEnvelopeID()
	l := &runLedger{
		store:       store,
		taskID:      runLedgerTaskID(invocation, objective),
		operationID: "run-" + invocation,
		scope:       append([]string(nil), scope...),
	}
	if _, err := store.CreateTaskWithProvenance(l.taskID, objective, l.scope, runScopeProvenance(objective)); err != nil {
		// Fail closed: the store cannot name the work it is about to do, so
		// the cycle must not start. Release the runtime lock on the way out.
		_ = store.Close()
		return nil, fmt.Errorf("create durable task: %w", err)
	}
	return l, nil
}

// runLedgerTaskID derives the durable identity of one invocation's cycle from
// the invocation identity and the objective text — the same derivation shape
// the autonomy Driver uses for (session, objective).
func runLedgerTaskID(invocation, objective string) string {
	sum := sha256.Sum256([]byte(invocation + "\x00" + objective))
	return "run-" + hex.EncodeToString(sum[:8])
}

// runScopeProvenance resolves the scope provenance this objective was
// authorized under, from the SAME parser and the SAME call the TUI Driver, the
// IntentGateway and the UI use (WorkspaceBuild, the workspace `izen run`
// executes in). It never invents a value and never defaults to a mutating
// grant: a prompt that carries no $prompt/$hot directive yields the read-only
// zero value, because that is the authorization the human actually typed. The
// durable task inherits exactly that grant — a task must not claim a scope
// authority the objective did not carry.
func runScopeProvenance(objective string) domain.ScopeProvenance {
	ast, err := parser.ParseInWorkspace(objective, nil, command.WorkspaceBuild)
	if err != nil || ast == nil {
		return domain.ScopeNone
	}
	return ast.ScopeProvenance
}

// Dispatch records that the cycle's operation entered the substrate. The
// precondition digest is the worktree as observed BEFORE the pipeline runs, so
// a crash between here and the commit can be reconciled later: an unchanged
// worktree is a safe retry, anything else is a conflict that forces re-planning.
//
// The postcondition digest is deliberately left unestablished: this path has no
// execution proof to derive it from, and a fabricated one would let recovery
// skip work that never happened. Reconciliation therefore errs toward
// re-planning, which is the safe direction.
func (l *runLedger) Dispatch() {
	if l == nil || l.store == nil {
		return
	}
	cursor := durable.ExecutionCursor{
		TaskID:              l.taskID,
		StepID:              l.operationID,
		OperationID:         l.operationID,
		PreconditionDigest:  runWorktreeDigest(l.store.WorkDir(), l.scope),
		PostconditionDigest: "",
	}
	if err := l.store.DispatchCursor(cursor); err != nil {
		l.warn("CURSOR_DISPATCHED refused: %v", err)
	}
}

// Complete records a cycle that finished WITHOUT error, from the pipeline
// result it actually produced.
//
// SEMANTICS (read before changing this): the headless path holds no
// objective-proof authority. A provider DONE and a green pipeline only
// establish that the execution committed and that the artifacts passed the
// pipeline's validation gate — never that an objective was proven. So the
// record uses the existing committed/verification vocabulary only:
//
//	EXECUTION_COMMITTED  the cycle's work reached the workspace
//	VERIFICATION_RESULT  what was actually observed, with the counts that
//	                     produced the verdict
//	VERIFICATION_RESULT  terminal: this execution finished (COMPLETED)
//	CHECKPOINT_CREATED   materialized view written last, so snapshot.json
//	                     reflects the end of the cycle
//
// There is deliberately no "objective proven" notion here: a headless run that
// retired its task has committed and validated an execution, nothing more.
func (l *runLedger) Complete(res *app.Result) {
	if l == nil || l.store == nil {
		return
	}
	detail := runOutcomeDetail(res)
	if err := l.store.CommitExecution(l.taskID, l.operationID); err != nil {
		l.warn("EXECUTION_COMMITTED refused: %v", err)
	}
	if err := l.store.RecordVerification(l.taskID, l.operationID, true, detail); err != nil {
		l.warn("VERIFICATION_RESULT refused: %v", err)
	}
	terminal := boundedRunDetail(detail + "; execution committed — no objective-proof authority on this path, so this retires a committed EXECUTION, not a proven objective")
	if err := l.store.RecordTerminalResult(l.taskID, l.operationID, true, terminal); err != nil {
		l.warn("terminal VERIFICATION_RESULT refused: %v", err)
	}
	l.CheckpointBoundary("completed")
}

// Fail records a cycle that ended in an error, with the REAL reason, and parks
// the task. It is never a success: FAILURE_CLASSIFIED carries the classified
// reason (what a recovery must read) and TASK_PAUSED leaves the task
// non-terminal and recoverable, because a headless run has no resume — the
// human re-invokes, and the next process must still be able to see that this
// attempt happened and why it stopped.
//
// The order matters: the classification precedes the pause so the journal never
// shows a paused task with no recorded reason.
func (l *runLedger) Fail(reason string, cause error) {
	if l == nil || l.store == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = boundedRunDetail(cause.Error())
	}
	if err := l.store.RecordFailure(l.taskID, reason, l.operationID, detail); err != nil {
		l.warn("FAILURE_CLASSIFIED refused: %v", err)
	}
	pause := boundedRunDetail(reason + ": " + detail)
	if err := l.store.PauseTask(l.taskID, pause); err != nil {
		l.warn("TASK_PAUSED refused: %v", err)
	}
	l.CheckpointBoundary("failed")
}

// CheckpointBoundary materializes the snapshot at a cycle boundary, so "where
// was this run when the process stopped" is readable without replaying the
// whole journal.
func (l *runLedger) CheckpointBoundary(reason string) {
	if l == nil || l.store == nil {
		return
	}
	id := l.operationID + "-" + reason
	if err := l.store.Checkpoint(l.taskID, id); err != nil {
		l.warn("CHECKPOINT_CREATED refused: %v", err)
	}
}

// Close releases the runtime file lock. It is idempotent and its error is
// surfaced by the caller: a shut-down runtime that still holds the journal's
// writer lock would make the next process's lineage unknowable.
func (l *runLedger) Close() error {
	if l == nil || l.store == nil {
		return nil
	}
	return l.store.Close()
}

// warn routes a refused append to stderr. A silent drop is how a journal rots
// unnoticed; the run itself continues because its mutations already stand.
func (l *runLedger) warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "izen run: durable ledger "+format+"\n", args...)
}

// runWorktreeDigest computes the pre-dispatch worktree digest over the declared
// scope, degrading to the whole worktree when a declared target does not exist
// yet, and to "" when neither can be read. An empty digest is not a failure of
// the run: it only means recovery will conservatively re-plan rather than
// re-execute.
func runWorktreeDigest(dir string, scope []string) string {
	if d, err := durable.ComputeTreeDigest(dir, scope...); err == nil {
		return d
	}
	if len(scope) > 0 {
		if d, err := durable.ComputeTreeDigest(dir); err == nil {
			return d
		}
	}
	return ""
}

// runOutcomeDetail renders what a successful cycle actually established, from
// the pipeline result: how it ran, how much it wrote, and how much of what it
// wrote passed validation. It is a record of measurements, not a verdict on an
// objective.
func runOutcomeDetail(res *app.Result) string {
	if res == nil {
		return "pipeline returned no result"
	}
	passed := 0
	for _, v := range res.Validations {
		if v.Passed {
			passed++
		}
	}
	return boundedRunDetail(fmt.Sprintf("mode=%s artifacts=%d validations=%d/%d passed",
		string(res.Mode), len(res.Artifacts), passed, len(res.Validations)))
}

// boundedRunDetail caps what an error or provider-derived string may write into
// the journal. The ledger is a record of what happened, not a transcript.
func boundedRunDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= runLedgerDetailLimit {
		return s
	}
	return s[:runLedgerDetailLimit] + "…"
}

// errRunLedgerClosed joins a deferred Close error onto the run's own outcome so
// a teardown failure can never be dropped on the floor.
func errRunLedgerClosed(runErr, closeErr error) error {
	if closeErr == nil {
		return runErr
	}
	wrapped := fmt.Errorf("izen run: close durable execution ledger: %w", closeErr)
	if runErr == nil {
		return wrapped
	}
	return errors.Join(runErr, wrapped)
}
