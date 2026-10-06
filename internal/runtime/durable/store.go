package durable

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

// TaskStore owns the two canonical artifacts under .izen/runtime/:
// ledger.ndjson (append-only source of truth) and snapshot.json
// (materialized view). All mutating calls are serialized by an internal
// mutex and guarded by the process-level FileLock.
type TaskStore struct {
	mu         sync.Mutex
	workDir    string
	runtimeDir string
	ledgerPath string
	snapPath   string
	lock       *FileLock

	tasks     map[string]*TaskState
	currentID string

	// Phase 3 adaptive state (derived from ledger replay, like tasks):
	// per-task context ladder tier, recorded evidence-pressure signals,
	// and negative-knowledge entries (active + stale).
	contextTiers map[string]int
	pressures    map[string][]LedgerEvent
	negatives    map[string][]NegativeKnowledgeRecord

	// seq is the highest sequence number durably appended. It is assigned
	// and advanced under s.mu inside appendLocked, so two concurrent
	// callers can never mint the same position, and it is reconstructed
	// from the journal on Open rather than trusted across restarts.
	seq uint64
	// gaps and faults hold the sequence defects observed by the last
	// replayLocked. They are data, not errors: a torn or hand-corrupted
	// journal still reconstructs the state it can, but the caller is told
	// exactly what could not be verified.
	gaps   []uint64
	faults []SequenceFault
	// touch records, per task, how many replayed events touched it. It is
	// the reconstruction ORDER of the fold (file order stays authoritative
	// even for a legacy journal), so the most recently worked task can be
	// recovered without trusting sequence numbers.
	touch    map[string]uint64
	touchSeq uint64
	// closed marks a torn-down store: after Close every append is refused,
	// so a shut-down runtime can never keep writing execution truth.
	closed bool
}

// maxReportedSequenceGaps bounds how many missing sequence positions a single
// replay enumerates. A journal whose sequence was hand-edited can claim an
// arbitrarily large position; the audit must stay bounded, and the truncation
// is itself recorded as a fault so the caller still sees that the enumeration
// was incomplete.
const maxReportedSequenceGaps = 4096

// SnapshotFile is the on-disk shape of snapshot.json: purely derived,
// fully reconstructible from ledger.ndjson.
type SnapshotFile struct {
	CurrentTaskID string               `json:"currentTaskId"`
	Tasks         map[string]TaskState `json:"tasks"`
}

// NewTaskStore binds a store to workDir (paths only; no I/O).
func NewTaskStore(workDir string) *TaskStore {
	clean := filepath.Clean(workDir)
	rt := filepath.Join(clean, ".izen", "runtime")
	return &TaskStore{
		workDir:      clean,
		runtimeDir:   rt,
		ledgerPath:   filepath.Join(rt, "ledger.ndjson"),
		snapPath:     filepath.Join(rt, "snapshot.json"),
		lock:         NewFileLock(clean),
		tasks:        make(map[string]*TaskState),
		contextTiers: make(map[string]int),
		pressures:    make(map[string][]LedgerEvent),
		negatives:    make(map[string][]NegativeKnowledgeRecord),
		touch:        make(map[string]uint64),
	}
}

// LedgerPath returns the ledger file path (instrumentation for tests).
func (s *TaskStore) LedgerPath() string { return s.ledgerPath }

// WorkDir returns the bound working directory root used for digest
// computation.
func (s *TaskStore) WorkDir() string { return s.workDir }

// TaskScopes returns a snapshot of per-task ActiveTargetScope keyed by task
// ID. It exists so reconciliation can resolve digests per task without
// re-entering the store lock from inside ReconcileAll (which already holds
// it while invoking the digest func).
func (s *TaskStore) TaskScopes() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]string, len(s.tasks))
	for id, t := range s.tasks {
		if t != nil {
			out[id] = append([]string(nil), t.ActiveTargetScope...)
		}
	}
	return out
}

// SnapshotPath returns the snapshot file path.
func (s *TaskStore) SnapshotPath() string { return s.snapPath }

// Open creates the runtime directory, replays the ledger (tolerating a
// torn tail from SIGKILL), and rebuilds in-memory state. It also reclaims
// a stale lock left by a killed process, logging STALE_LOCK_RECLAIMED.
func (s *TaskStore) Open() error {
	if s == nil {
		return fmt.Errorf("durable: nil TaskStore")
	}
	if strings.TrimSpace(s.workDir) == "" {
		return fmt.Errorf("durable: empty workDir")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.runtimeDir, 0o755); err != nil {
		return fmt.Errorf("durable: mkdir runtime: %w", err)
	}
	reclaimed, err := s.lock.Acquire(DefaultLeaseTTLMs)
	if err != nil {
		return err
	}
	defer s.lock.Release()
	if err := s.replayLocked(); err != nil {
		return err
	}
	if reclaimed {
		// Best-effort: record the reclaim so the lineage shows why a
		// successor took over. A failure here must not fail Open.
		_ = s.appendLocked(newEvent("", EventStaleLockReclaimed, map[string]any{
			"pid": os.Getpid(),
		}))
	}
	return nil
}

// CreateTask appends TASK_CREATED and materializes the task. Legacy
// three-argument shape: tasks created without an explicit provenance carry
// the read-only zero value (ScopeNone).
func (s *TaskStore) CreateTask(id, intent string, scope []string) (*TaskState, error) {
	return s.CreateTaskWithProvenance(id, intent, scope, domain.ScopeNone)
}

// CreateTaskWithProvenance appends TASK_CREATED carrying the authorization
// provenance so the durable TaskState inherits the grant that created it.
func (s *TaskStore) CreateTaskWithProvenance(id, intent string, scope []string, provenance domain.ScopeProvenance) (*TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("durable: empty task id")
	}
	if err := s.withLock(func() error {
		return s.appendLocked(newEvent(id, EventTaskCreated, map[string]any{
			"intent":     intent,
			"scope":      scope,
			"provenance": int(provenance),
		}))
	}); err != nil {
		return nil, err
	}
	s.apply(newEvent(id, EventTaskCreated, map[string]any{"intent": intent, "scope": scope, "provenance": int(provenance)}))
	// Re-read canonical state (apply is idempotent for TASK_CREATED
	// only on first creation; guard against double-apply by replay
	// consistency: if task already existed, keep original).
	t := s.tasks[id]
	if t == nil {
		return nil, fmt.Errorf("durable: task %q not materialized", id)
	}
	out := *t
	return &out, nil
}

// DispatchCursor appends CURSOR_DISPATCHED for a side-effecting operation.
func (s *TaskStore) DispatchCursor(c ExecutionCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(c.OperationID) == "" {
		return fmt.Errorf("durable: empty operation id")
	}
	c.Phase = PhaseExecuting
	c.Status = CursorDispatched
	ev := newEvent(c.TaskID, EventCursorDispatched, map[string]any{
		"stepId":              c.StepID,
		"operationId":         c.OperationID,
		"phase":               string(c.Phase),
		"preconditionDigest":  c.PreconditionDigest,
		"postconditionDigest": c.PostconditionDigest,
		"status":              string(c.Status),
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// CommitExecution appends EXECUTION_COMMITTED (truth boundary: fsync)
// and advances the cursor to COMMITTED. It records no postcondition digest:
// callers that observed one use CommitExecutionWithDigest.
func (s *TaskStore) CommitExecution(taskID, operationID string) error {
	return s.CommitExecutionWithDigest(taskID, operationID, "")
}

// CommitExecutionWithDigest appends EXECUTION_COMMITTED (truth boundary:
// fsync), advances the cursor to COMMITTED, and — when postconditionDigest is
// non-empty — records the observed post-state on the cursor. The digest is
// what makes a fresh runtime able to answer ALREADY_COMMITTED vs CONFLICT
// from surviving evidence: the commit marker proves the operation committed,
// and the digest binds that commitment to the exact workspace bytes it
// produced. An empty digest preserves the pre-existing behavior (a commit
// with no recorded post-state, as the scopeguard path writes).
func (s *TaskStore) CommitExecutionWithDigest(taskID, operationID, postconditionDigest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload := map[string]any{
		"operationId": operationID,
		"phase":       string(PhaseCommitted),
		"status":      string(CursorCommitted),
	}
	if strings.TrimSpace(postconditionDigest) != "" {
		payload["postconditionDigest"] = postconditionDigest
	}
	ev := newEvent(taskID, EventExecutionCommitted, payload)
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// RecordVerification appends VERIFICATION_RESULT (truth boundary: fsync).
// When ok is true the cursor clears (operation fully done); otherwise
// the cursor is retained for inspection / re-plan.
func (s *TaskStore) RecordVerification(taskID, operationID string, ok bool, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := newEvent(taskID, EventVerificationResult, map[string]any{
		"operationId": operationID,
		"ok":          ok,
		"detail":      detail,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// RecordTerminalResult appends VERIFICATION_RESULT carrying the terminal
// flag: it is the ONE transition that retires a task (COMPLETED when ok, FAILED
// when not).
//
// It deliberately reuses the existing VERIFICATION_RESULT event rather than
// introducing a terminal event type: the journal's vocabulary stays closed,
// and a replay written before this method existed is unaffected. Without a
// terminal transition there is no way to record "this objective is finished",
// and a finished objective would then replay as unfinished work on every
// restart — exactly the silent re-prompt the state-reconstruction contract
// forbids.
func (s *TaskStore) RecordTerminalResult(taskID, operationID string, ok bool, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload := map[string]any{
		"operationId": operationID,
		"ok":          ok,
		"detail":      detail,
		"terminal":    true,
	}
	if !ok {
		payload["failed"] = true
	}
	ev := newEvent(taskID, EventVerificationResult, payload)
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// Checkpoint appends CHECKPOINT_CREATED (truth boundary: fsync) and then
// atomically rewrites snapshot.json (temp file + rename).
func (s *TaskStore) Checkpoint(taskID, checkpointID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := newEvent(taskID, EventCheckpointCreated, map[string]any{
		"checkpointId": checkpointID,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return s.writeSnapshotLocked()
}

// RecordConflict appends TARGET_CONFLICT and moves the task to RE_PLAN.
func (s *TaskStore) RecordConflict(taskID, operationID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := newEvent(taskID, EventTargetConflict, map[string]any{
		"operationId": operationID,
		"reason":      reason,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// RecordFailure appends FAILURE_CLASSIFIED. It MUST be called before any
// Phase 2 recovery transition is attempted: no recovery without a
// classified failure event in ledger.ndjson.
func (s *TaskStore) RecordFailure(taskID, reason, operationID, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := newEvent(taskID, EventFailureClassified, map[string]any{
		"reason":      reason,
		"operationId": operationID,
		"detail":      detail,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// RecordHandoff appends WORKER_HANDOFF, preserving TaskID, CheckpointID and
// event lineage: a worker/provider change never rewrites task identity.
func (s *TaskStore) RecordHandoff(taskID, fromWorker, toWorker, checkpointID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := newEvent(taskID, EventWorkerHandoff, map[string]any{
		"fromWorker":   fromWorker,
		"toWorker":     toWorker,
		"checkpointId": checkpointID,
		"reason":       reason,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// PauseTask appends TASK_PAUSED and moves the task to PAUSED, yielding
// control to the human boundary. Used when the recovery budget is exhausted.
func (s *TaskStore) PauseTask(taskID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := newEvent(taskID, EventTaskPaused, map[string]any{
		"reason": reason,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// ── Phase 3: adaptive context engine ──

// RecordEvidencePressure appends EVIDENCE_PRESSURE. Context-tier expansion
// is valid ONLY when a matching pressure event exists in ledger.ndjson;
// self-reported model confidence alone never expands context.
func (s *TaskStore) RecordEvidencePressure(taskID, signal, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("durable: empty task id")
	}
	if strings.TrimSpace(signal) == "" {
		return fmt.Errorf("durable: empty evidence-pressure signal")
	}
	ev := newEvent(taskID, EventEvidencePressure, map[string]any{
		"signal": signal,
		"detail": detail,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// HasEvidencePressure reports whether any EVIDENCE_PRESSURE event with the
// given signal exists for the task. Empty signal matches any pressure.
func (s *TaskStore) HasEvidencePressure(taskID, signal string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.pressures[taskID] {
		if signal == "" || strField(ev.Payload, "signal") == signal {
			return true
		}
	}
	return false
}

// RecordNegativeKnowledge appends NEGATIVE_KNOWLEDGE_RECORDED for a
// hypothesis disproven with concrete evidence (failed build, failing test,
// static-analysis error). Empty hypothesis/evidence is rejected: negative
// knowledge requires evidence, never bare model assertion.
func (s *TaskStore) RecordNegativeKnowledge(taskID string, rec NegativeKnowledgeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("durable: empty task id")
	}
	if strings.TrimSpace(rec.Hypothesis) == "" {
		return fmt.Errorf("durable: empty negative-knowledge hypothesis")
	}
	if len(rec.EvidenceRefs) == 0 {
		return fmt.Errorf("durable: negative knowledge requires evidence refs")
	}
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = newID()
	}
	if strings.TrimSpace(rec.Status) == "" {
		rec.Status = NegativeStatusActive
	}
	ev := newEvent(taskID, EventNegativeKnowledge, map[string]any{
		"id":           rec.ID,
		"hypothesis":   rec.Hypothesis,
		"whyRejected":  rec.WhyRejected,
		"evidenceRefs": rec.EvidenceRefs,
		"targetScope":  rec.TargetScope,
		"status":       rec.Status,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// NegativeKnowledge returns a copy of all negative-knowledge records for a task.
func (s *TaskStore) NegativeKnowledge(taskID string) []NegativeKnowledgeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]NegativeKnowledgeRecord(nil), s.negatives[taskID]...)
}

// ActiveNegativeKnowledge returns only ACTIVE records matching scope.
// Empty scope matches all active records.
func (s *TaskStore) ActiveNegativeKnowledge(taskID string, scope []string) []NegativeKnowledgeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []NegativeKnowledgeRecord
	for _, r := range s.negatives[taskID] {
		if r.Status != NegativeStatusActive {
			continue
		}
		if len(scope) > 0 && len(r.TargetScope) > 0 && !scopesOverlap(r.TargetScope, scope) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// MarkNegativeKnowledgeStale appends NEGATIVE_KNOWLEDGE_STALED for one entry.
// Called on RE_PLAN / structural scope shifts that invalidate the
// preconditions of a rejected hypothesis.
func (s *TaskStore) MarkNegativeKnowledgeStale(taskID, id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, r := range s.negatives[taskID] {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("durable: negative knowledge %q not found for task %q", id, taskID)
	}
	ev := newEvent(taskID, EventNegativeKnowledgeStale, map[string]any{
		"id":     id,
		"reason": reason,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// MarkStaleOnScopeShift transitions to STALE every ACTIVE record whose
// TargetScope no longer overlaps newScope. Records with an empty
// TargetScope are scope-universal and are retained. Returns the count
// transitioned. An empty newScope transitions nothing.
func (s *TaskStore) MarkStaleOnScopeShift(taskID string, newScope []string, reason string) (int, error) {
	if len(newScope) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	staleIDs := make([]string, 0)
	for _, r := range s.negatives[taskID] {
		if r.Status != NegativeStatusActive || len(r.TargetScope) == 0 {
			continue
		}
		if !scopesOverlap(r.TargetScope, newScope) {
			staleIDs = append(staleIDs, r.ID)
		}
	}
	s.mu.Unlock()
	count := 0
	for _, id := range staleIDs {
		if err := s.MarkNegativeKnowledgeStale(taskID, id, reason); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// AdvanceContextTier appends CONTEXT_TIER_ADVANCED and records the new tier.
// Callers MUST have recorded a matching EVIDENCE_PRESSURE event first;
// this method does not itself verify that invariant (the adaptive planner
// enforces it) so replay stays a pure fold.
func (s *TaskStore) AdvanceContextTier(taskID string, tier int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tier < 0 || tier > 4 {
		return fmt.Errorf("durable: context tier %d out of range [0,4]", tier)
	}
	ev := newEvent(taskID, EventContextTierAdvanced, map[string]any{
		"tier": tier,
	})
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

// ContextTier returns the persisted ladder tier for a task (default L0).
func (s *TaskStore) ContextTier(taskID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contextTiers[taskID]
}

// RecordCustomEvent appends an audit-lineage event that carries no
// materialized state transition (Phase 4 guard/gateway lineage:
// SCOPE_VIOLATION_REJECTED, STRUCTURAL_AMBIGUITY, PROPOSAL_AUTHORIZED,
// WORKSPACE_SWITCHED). The event is durable in ledger.ndjson and survives
// snapshot rebuild; replay folds it as a currentID touch only so task
// identity and cursor state are never rewritten by audit events.
func (s *TaskStore) RecordCustomEvent(taskID string, typ EventType, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("durable: empty task id")
	}
	if typ == "" {
		return fmt.Errorf("durable: empty event type")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	ev := newEvent(taskID, typ, payload)
	if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
		return err
	}
	s.apply(ev)
	return nil
}

func scopesOverlap(a, b []string) bool {
	for _, x := range a {
		nx := strings.TrimSpace(x)
		if nx == "" {
			continue
		}
		for _, y := range b {
			ny := strings.TrimSpace(y)
			if ny == "" {
				continue
			}
			if nx == ny || strings.HasPrefix(nx, ny) || strings.HasPrefix(ny, nx) {
				return true
			}
		}
	}
	return false
}

// State returns a copy of the materialized task state.
func (s *TaskStore) State(taskID string) (TaskState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return TaskState{}, false
	}
	return *t, true
}

// CurrentID returns the most recently touched task id.
func (s *TaskStore) CurrentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentID
}

// NextSequence reports the sequence the next appended event will carry. It is
// the value reconstructed by the last Open/replay, so a fresh store reports 1
// and a store reopened over a journal whose highest sequence is N reports
// N+1. Callers never assign it: appendLocked owns the position.
func (s *TaskStore) NextSequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq + 1
}

// SequenceGaps returns the sequence positions the last replay found missing
// from the journal. A non-empty result means the journal is incomplete: the
// events recorded there are absent, and any conclusion drawn across the gap
// rests on a hole in the execution truth.
func (s *TaskStore) SequenceGaps() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.gaps...)
}

// SequenceFaults returns every sequence defect the last replay observed —
// both the missing positions (SequenceFaultGap) and the positions that
// arrived out of order (SequenceFaultNonMonotonic). Replay never fails on
// either: it is the caller's job to decide what a corrupt journal means.
func (s *TaskStore) SequenceFaults() []SequenceFault {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SequenceFault(nil), s.faults...)
}

// RecoverableTasks returns every replayed task whose lifecycle is still open,
// most recently worked first. This is the §9 state reconstruction surface: a
// restart reads it to learn what was in flight when the process died instead
// of inferring that nothing happened.
func (s *TaskStore) RecoverableTasks() []TaskState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskState, 0, len(s.tasks))
	for _, t := range s.tasks {
		if t == nil || t.Status.Terminal() {
			continue
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if s.touch[out[i].ID] != s.touch[out[j].ID] {
			return s.touch[out[i].ID] > s.touch[out[j].ID]
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// MostRecentRecoverable returns the most recently replayed task that never
// reached a terminal state. ok is false when every task completed or failed —
// the ordinary end of a run, and never an error.
func (s *TaskStore) MostRecentRecoverable() (TaskState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := TaskState{}
	bestSeq := uint64(0)
	found := false
	for id, t := range s.tasks {
		if t == nil || t.Status.Terminal() {
			continue
		}
		touch := s.touch[id]
		// A task with no recorded touch (an empty id, an audit-only event)
		// must never outrank one the fold actually worked on.
		if !found || touch > bestSeq || (touch == bestSeq && id < best.ID) {
			best, bestSeq, found = *t, touch, true
		}
	}
	return best, found
}

// Close tears the store down: it releases the runtime file lock and refuses
// every later append. It is idempotent. A shut-down runtime that could still
// write execution truth would make the journal's last writer unknowable.
func (s *TaskStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.lock.Release()
	return nil
}

// RebuildSnapshot replays the ledger from disk and atomically rewrites
// snapshot.json. It proves the canonical lineage invariant: deleting or
// corrupting snapshot.json loses nothing.
func (s *TaskStore) RebuildSnapshot() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lock.Held() {
		if _, err := s.lock.Acquire(DefaultLeaseTTLMs); err != nil {
			return err
		}
		defer s.lock.Release()
	}
	if err := s.replayLocked(); err != nil {
		return err
	}
	return s.writeSnapshotLocked()
}

// ReconcileAll applies the deterministic side-effect reconciliation rule
// to every task holding a pending cursor, using digestOf to compute the
// current worktree digest. It MUST be called on runtime init / worker
// crash recovery before executing any new action.
//
//   - current == post  -> advance to COMMITTED/VERIFICATION_PENDING,
//     append EXECUTION_COMMITTED, DO NOT RE-EXECUTE.
//   - current == pre   -> reset to PENDING for safe retry (no event;
//     the retry dispatch will append its own CURSOR_DISPATCHED).
//   - otherwise        -> CONFLICT + TARGET_CONFLICT + RE_PLAN.
//
// It returns the per-task decisions.
func (s *TaskStore) ReconcileAll(digestOf func(taskID string) (string, error)) (map[string]ReconcileDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ReconcileDecision)
	for id, t := range s.tasks {
		if t == nil || t.Cursor == nil {
			continue
		}
		c := t.Cursor
		if c.Status == CursorCommitted || c.Status == CursorConflict {
			continue
		}
		if digestOf == nil {
			return nil, fmt.Errorf("durable: nil digest func")
		}
		current, err := digestOf(id)
		if err != nil {
			return nil, err
		}
		switch Reconcile(*c, current) {
		case DecisionAlreadyCommitted:
			ev := newEvent(id, EventExecutionCommitted, map[string]any{
				"operationId": c.OperationID,
				"phase":       string(PhaseVerificationPending),
				"status":      string(CursorCommitted),
				"reconciled":  true,
			})
			if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
				return nil, err
			}
			s.apply(ev)
			out[id] = DecisionAlreadyCommitted
		case DecisionSafeRetry:
			c.Status = CursorPending
			c.Phase = PhasePreExecution
			out[id] = DecisionSafeRetry
		case DecisionConflict:
			ev := newEvent(id, EventTargetConflict, map[string]any{
				"operationId": c.OperationID,
				"reason":      "digest matches neither precondition nor postcondition",
				"current":     current,
			})
			if err := s.withLock(func() error { return s.appendLocked(ev) }); err != nil {
				return nil, err
			}
			s.apply(ev)
			out[id] = DecisionConflict
		}
	}
	return out, nil
}

// InspectCursors is the READ-ONLY reconciliation surface a fresh runtime uses
// to classify an interrupted mutation WITHOUT resuming or retrying it.
//
// Unlike ReconcileAll it (a) transitions nothing — it appends no event and
// mutates no task — and (b) also reports tasks whose cursor already carries a
// durable commit marker, so a committed-but-abandoned operation is
// inspectable after a restart. digestOf computes the live workspace digest for
// a task; every NON-TERMINAL task yields exactly one entry (a terminal task
// already has an authoritative end). Absence of evidence yields
// DecisionUnknown, never SafeRetry and never AlreadyCommitted.
//
// It is deliberately separate from ReconcileAll: the autonomous crash question
// ("did the mutation commit?") is answered by a fresh runtime that must not
// silently act on the answer.
func (s *TaskStore) InspectCursors(digestOf func(taskID string) (string, error)) ([]CursorInspection, error) {
	if s == nil {
		return nil, fmt.Errorf("durable: nil TaskStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.tasks))
	for id, t := range s.tasks {
		if t == nil {
			continue
		}
		// Only INTERRUPTED work needs reconciling: a task that reached a
		// durable terminal state already has an authoritative end, so it is
		// not offered back as unfinished. Every non-terminal task is surfaced
		// — with its cursor decision when one exists, and UNKNOWN when none
		// does.
		if t.Status.Terminal() {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]CursorInspection, 0, len(ids))
	for _, id := range ids {
		t := s.tasks[id]
		insp := CursorInspection{TaskID: id, Decision: DecisionUnknown}
		if t.Cursor != nil {
			c := t.Cursor
			insp.OperationID = c.OperationID
			insp.StepID = c.StepID
			insp.Phase = c.Phase
			insp.Status = c.Status
			insp.PreconditionDigest = c.PreconditionDigest
			insp.PostconditionDigest = c.PostconditionDigest
			insp.Committed = c.Status == CursorCommitted
			if digestOf != nil {
				current, err := digestOf(id)
				if err != nil {
					return nil, err
				}
				insp.CurrentDigest = current
				insp.Decision = inspectDecision(*c, current)
			}
		}
		out = append(out, insp)
	}
	return out, nil
}

// inspectDecision maps a durable cursor plus the live workspace digest onto the
// reconciliation vocabulary. A durable commit marker is authoritative: the
// operation committed, and the recorded postcondition digest (when present)
// only distinguishes "workspace still matches" (ALREADY_COMMITTED) from "a
// third party changed it after the commit" (CONFLICT). A dispatched cursor is
// judged by the existing Reconcile rule; a conflicted one stays CONFLICT.
func inspectDecision(c ExecutionCursor, current string) ReconcileDecision {
	switch c.Status {
	case CursorCommitted:
		if strings.TrimSpace(c.PostconditionDigest) != "" {
			if current == c.PostconditionDigest {
				return DecisionAlreadyCommitted
			}
			return DecisionConflict
		}
		return DecisionAlreadyCommitted
	case CursorConflict:
		return DecisionConflict
	default:
		return Reconcile(c, current)
	}
}

// ── internals ──

// withLock acquires the runtime FileLock for the duration of fn when this
// handle does not already hold it. STALE_LOCK_RECLAIMED is logged inline
// on reclaim.
func (s *TaskStore) withLock(fn func() error) error {
	if s.lock.Held() {
		return fn()
	}
	reclaimed, err := s.lock.Acquire(DefaultLeaseTTLMs)
	if err != nil {
		return err
	}
	defer s.lock.Release()
	if reclaimed {
		_ = s.appendLocked(newEvent(s.currentID, EventStaleLockReclaimed, map[string]any{
			"pid": os.Getpid(),
		}))
	}
	return fn()
}

// appendLocked serializes one event as a single JSON line, fsyncing at
// truth boundaries, and assigns the event's monotonic sequence. Callers must
// hold s.mu (and the file lock via withLock or Open).
//
// The sequence is minted HERE, not by the caller: every append path in this
// package funnels through here, so a caller-supplied position could only ever
// duplicate or skip one. It is written in the same JSON line as the event and
// reaches the same fsync, which makes the position durable exactly when the
// event is. The counter advances only after the bytes are on disk: a failed
// write leaves the next append on the same position rather than burning it.
func (s *TaskStore) appendLocked(ev LedgerEvent) error {
	if s.closed {
		return fmt.Errorf("durable: store closed")
	}
	ev.Sequence = s.seq + 1
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("durable: marshal event: %w", err)
	}
	data = append(data, '\n')
	f, err := os.OpenFile(s.ledgerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("durable: open ledger: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("durable: write ledger: %w", err)
	}
	// Truth boundaries MUST fsync. Non-boundary events fsync too: the
	// cost is negligible at this scale and it keeps every prefix of the
	// ledger durable, which can only strengthen crash invariance.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("durable: fsync ledger: %w", err)
	}
	if ev.EventType.IsTruthBoundary() {
		if d, err := os.Open(s.runtimeDir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	s.seq = ev.Sequence
	return nil
}

// replayLocked rebuilds in-memory state from ledger.ndjson, tolerating a
// torn tail: undecodable lines (a SIGKILL mid-write) are skipped so the
// runtime recovers to the last coherent prefix.
//
// It also reconstructs the next sequence from the journal — never from a
// side file — and audits the sequence it found:
//
//   - a sequence of 0 is a legacy event written before the field existed. It
//     replays normally and is deliberately NOT a gap or a fault: it neither
//     advances nor rewinds the counter, which is the max of the non-zero
//     values observed. A journal of nothing but legacy events therefore
//     reconstructs to "next = 1".
//   - a non-zero sequence higher than expected means events are missing from
//     the journal; each missing position is recorded as a gap.
//   - a non-zero sequence that does not exceed the highest value already seen
//     is non-monotonic: the journal's order can no longer be trusted to match
//     its claimed order, so the offending position is recorded as a fault.
//
// Neither a gap nor a non-monotonic value stops the fold: the events that ARE
// present still reconstruct, because a partially corrupt journal is still
// evidence. What it must never do is accept the journal silently.
func (s *TaskStore) replayLocked() error {
	s.tasks = make(map[string]*TaskState)
	s.contextTiers = make(map[string]int)
	s.pressures = make(map[string][]LedgerEvent)
	s.negatives = make(map[string][]NegativeKnowledgeRecord)
	s.touch = make(map[string]uint64)
	s.touchSeq = 0
	s.currentID = ""
	s.seq = 0
	s.gaps = nil
	s.faults = nil
	f, err := os.Open(s.ledgerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("durable: open ledger for replay: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	const maxLine = 4 << 20
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev LedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Torn write (kill -9 mid-append): skip the partial line.
			continue
		}
		if ev.EventType == "" {
			continue
		}
		if ev.Sequence != 0 {
			switch {
			case ev.Sequence > s.seq+1:
				for missing := s.seq + 1; missing < ev.Sequence; missing++ {
					s.gaps = append(s.gaps, missing)
					s.faults = append(s.faults, SequenceFault{
						Kind:     SequenceFaultGap,
						Observed: ev.Sequence,
						Expected: missing,
					})
					// Bounded audit: a hand-edited journal may claim a
					// position millions of events past the end. Stop
					// enumerating and record that the enumeration was
					// truncated rather than exhausting memory.
					if len(s.gaps) >= maxReportedSequenceGaps {
						s.faults = append(s.faults, SequenceFault{
							Kind:     SequenceFaultGap,
							Observed: ev.Sequence,
							Expected: 0,
						})
						break
					}
				}
			case ev.Sequence <= s.seq:
				s.faults = append(s.faults, SequenceFault{
					Kind:     SequenceFaultNonMonotonic,
					Observed: ev.Sequence,
					Expected: s.seq + 1,
				})
			}
			if ev.Sequence > s.seq {
				s.seq = ev.Sequence
			}
		}
		s.apply(ev)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("durable: scan ledger: %w", err)
	}
	if len(s.faults) > 0 {
		log.Printf("[durable] ledger %s: %d sequence fault(s), %d gap(s); state reconstructed from the events present",
			s.ledgerPath, len(s.faults), len(s.gaps))
	}
	return nil
}

// apply folds one event into the materialized view.
func (s *TaskStore) apply(ev LedgerEvent) {
	// Every folded event advances the per-task touch counter. It is what
	// makes "which task was being worked when the process died" answerable
	// after a restart without depending on the sequence numbers at all —
	// a legacy journal and a sequenced one are reconstructed the same way.
	if s.touch == nil {
		s.touch = make(map[string]uint64)
	}
	s.touchSeq++
	s.touch[ev.TaskID] = s.touchSeq
	switch ev.EventType {
	case EventTaskCreated:
		if _, exists := s.tasks[ev.TaskID]; !exists {
			t := &TaskState{
				ID:     ev.TaskID,
				Status: TaskCreated,
			}
			if v, ok := ev.Payload["intent"].(string); ok {
				t.Intent = v
			}
			// The live path hands apply an `int`; replay hands it a JSON
			// `float64`. Accepting only one of them made a freshly created
			// task read as read-only until the process restarted — the grant
			// was in the journal the whole time, just not in memory.
			switch pv := ev.Payload["provenance"].(type) {
			case float64:
				t.ScopeProvenance = domain.ScopeProvenance(uint8(pv))
			case int:
				t.ScopeProvenance = domain.ScopeProvenance(uint8(pv))
			}
			t.ActiveTargetScope = payloadStrings(ev.Payload["scope"])
			s.tasks[ev.TaskID] = t
		}
		s.currentID = ev.TaskID
	case EventCursorDispatched:
		t := ensureTask(s.tasks, ev.TaskID)
		t.CurrentStepID, _ = ev.Payload["stepId"].(string)
		t.Cursor = &ExecutionCursor{
			TaskID:      ev.TaskID,
			StepID:      t.CurrentStepID,
			OperationID: strField(ev.Payload, "operationId"),
			Phase:       CursorPhase(strField(ev.Payload, "phase")),
			Status:      CursorStatus(strField(ev.Payload, "status")),
		}
		t.Cursor.PreconditionDigest = strField(ev.Payload, "preconditionDigest")
		t.Cursor.PostconditionDigest = strField(ev.Payload, "postconditionDigest")
		if t.Cursor.Phase == "" {
			t.Cursor.Phase = PhaseExecuting
		}
		if t.Cursor.Status == "" {
			t.Cursor.Status = CursorDispatched
		}
		t.Status = TaskRunning
		s.currentID = ev.TaskID
	case EventExecutionCommitted:
		t := ensureTask(s.tasks, ev.TaskID)
		op := strField(ev.Payload, "operationId")
		if t.Cursor != nil && (op == "" || t.Cursor.OperationID == op) {
			t.Cursor.Status = CursorCommitted
			if ph := strField(ev.Payload, "phase"); ph != "" {
				t.Cursor.Phase = CursorPhase(ph)
			} else {
				t.Cursor.Phase = PhaseCommitted
			}
			// The observed post-state, when the committing caller recorded
			// it, is durable with the marker. It is what a fresh runtime
			// compares against to distinguish ALREADY_COMMITTED from a
			// post-commit third-party change (CONFLICT).
			if post := strField(ev.Payload, "postconditionDigest"); post != "" {
				t.Cursor.PostconditionDigest = post
			}
		}
		s.currentID = ev.TaskID
	case EventVerificationResult:
		t := ensureTask(s.tasks, ev.TaskID)
		ok, _ := ev.Payload["ok"].(bool)
		if ok {
			t.Cursor = nil
		} else if t.Cursor != nil {
			t.Cursor.Phase = PhaseVerificationPending
		}
		// A terminal result is the only transition that retires the task:
		// anything else leaves it recoverable, which is the honest answer
		// for a run that ended without a verdict.
		if terminal, _ := ev.Payload["terminal"].(bool); terminal {
			if ok {
				t.Status = TaskCompleted
			} else {
				t.Status = TaskFailed
			}
		} else if _, failed := ev.Payload["failed"]; failed {
			t.Status = TaskFailed
		}
		s.currentID = ev.TaskID
	case EventCheckpointCreated:
		t := ensureTask(s.tasks, ev.TaskID)
		t.LastCheckpointID = strField(ev.Payload, "checkpointId")
		if t.Status == TaskCreated {
			t.Status = TaskRunning
		}
		s.currentID = ev.TaskID
	case EventStaleLockReclaimed:
		if ev.TaskID != "" {
			s.currentID = ev.TaskID
		}
	case EventTargetConflict:
		t := ensureTask(s.tasks, ev.TaskID)
		if t.Cursor != nil {
			t.Cursor.Status = CursorConflict
		}
		t.Status = TaskRePlan
		s.currentID = ev.TaskID
	case EventFailureClassified:
		t := ensureTask(s.tasks, ev.TaskID)
		if t.Status == TaskRunning {
			t.Status = TaskInterrupted
		}
		s.currentID = ev.TaskID
	case EventWorkerHandoff:
		// State-driven failover: identity is preserved. Only the
		// checkpoint pointer advances when the payload carries one.
		t := ensureTask(s.tasks, ev.TaskID)
		if cp := strField(ev.Payload, "checkpointId"); cp != "" {
			t.LastCheckpointID = cp
		}
		if t.Status == TaskInterrupted {
			t.Status = TaskRunning
		}
		s.currentID = ev.TaskID
	case EventTaskPaused:
		t := ensureTask(s.tasks, ev.TaskID)
		t.Status = TaskPaused
		s.currentID = ev.TaskID
	case EventEvidencePressure:
		s.pressures[ev.TaskID] = append(s.pressures[ev.TaskID], ev)
		s.currentID = ev.TaskID
	case EventNegativeKnowledge:
		rec := NegativeKnowledgeRecord{
			ID:           strField(ev.Payload, "id"),
			Hypothesis:   strField(ev.Payload, "hypothesis"),
			WhyRejected:  strField(ev.Payload, "whyRejected"),
			EvidenceRefs: payloadStrings(ev.Payload["evidenceRefs"]),
			TargetScope:  payloadStrings(ev.Payload["targetScope"]),
			Status:       NegativeStatusActive,
		}
		if st, _ := ev.Payload["status"].(string); st != "" {
			rec.Status = st
		}
		s.negatives[ev.TaskID] = append(s.negatives[ev.TaskID], rec)
		s.currentID = ev.TaskID
	case EventNegativeKnowledgeStale:
		id := strField(ev.Payload, "id")
		for i, r := range s.negatives[ev.TaskID] {
			if r.ID == id {
				s.negatives[ev.TaskID][i].Status = NegativeStatusStale
			}
		}
		s.currentID = ev.TaskID
	case EventContextTierAdvanced:
		if s.contextTiers == nil {
			s.contextTiers = make(map[string]int)
		}
		tier := 0
		switch v := ev.Payload["tier"].(type) {
		case float64:
			tier = int(v)
		case int:
			tier = v
		}
		s.contextTiers[ev.TaskID] = tier
		s.currentID = ev.TaskID
	case EventScopeViolationRejected, EventStructuralAmbiguity,
		EventProposalAuthorized, EventWorkspaceSwitched, EventPhaseTransition:
		// Phase 4 audit lineage: durable in the ledger, no materialized
		// state transition. Task identity, cursor, checkpoint, evidence
		// and negative knowledge are preserved verbatim.
		s.currentID = ev.TaskID
		ensureTask(s.tasks, ev.TaskID)
	}
}

// writeSnapshotLocked persists the materialized view atomically.
func (s *TaskStore) writeSnapshotLocked() error {
	snap := SnapshotFile{
		CurrentTaskID: s.currentID,
		Tasks:         make(map[string]TaskState, len(s.tasks)),
	}
	for id, t := range s.tasks {
		if t != nil {
			snap.Tasks[id] = *t
		}
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("durable: marshal snapshot: %w", err)
	}
	if err := atomicio.WriteFileAtomic(s.snapPath, data, 0o644); err != nil {
		return fmt.Errorf("durable: write snapshot: %w", err)
	}
	return nil
}

func ensureTask(m map[string]*TaskState, id string) *TaskState {
	if t, ok := m[id]; ok && t != nil {
		return t
	}
	t := &TaskState{ID: id, Status: TaskRunning}
	m[id] = t
	return t
}

func strField(p map[string]any, k string) string {
	if p == nil {
		return ""
	}
	v, _ := p[k].(string)
	return v
}

func payloadStrings(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func newEvent(taskID string, typ EventType, payload map[string]any) LedgerEvent {
	if payload == nil {
		payload = map[string]any{}
	}
	return LedgerEvent{
		EventID:   newID(),
		TaskID:    taskID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		EventType: typ,
		Payload:   payload,
	}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
