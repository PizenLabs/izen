package durable

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
}

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
		workDir:    clean,
		runtimeDir: rt,
		ledgerPath: filepath.Join(rt, "ledger.ndjson"),
		snapPath:   filepath.Join(rt, "snapshot.json"),
		lock:       NewFileLock(clean),
		tasks:      make(map[string]*TaskState),
	}
}

// LedgerPath returns the ledger file path (instrumentation for tests).
func (s *TaskStore) LedgerPath() string { return s.ledgerPath }

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

// CreateTask appends TASK_CREATED and materializes the task.
func (s *TaskStore) CreateTask(id, intent string, scope []string) (*TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("durable: empty task id")
	}
	if err := s.withLock(func() error {
		return s.appendLocked(newEvent(id, EventTaskCreated, map[string]any{
			"intent": intent,
			"scope":  scope,
		}))
	}); err != nil {
		return nil, err
	}
	s.apply(newEvent(id, EventTaskCreated, map[string]any{"intent": intent, "scope": scope}))
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
// and advances the cursor to COMMITTED.
func (s *TaskStore) CommitExecution(taskID, operationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := newEvent(taskID, EventExecutionCommitted, map[string]any{
		"operationId": operationID,
		"phase":       string(PhaseCommitted),
		"status":      string(CursorCommitted),
	})
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
// truth boundaries. Callers must hold s.mu (and the file lock via
// withLock or Open).
func (s *TaskStore) appendLocked(ev LedgerEvent) error {
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
	return nil
}

// replayLocked rebuilds in-memory state from ledger.ndjson, tolerating a
// torn tail: undecodable lines (a SIGKILL mid-write) are skipped so the
// runtime recovers to the last coherent prefix.
func (s *TaskStore) replayLocked() error {
	s.tasks = make(map[string]*TaskState)
	s.currentID = ""
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
		s.apply(ev)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("durable: scan ledger: %w", err)
	}
	return nil
}

// apply folds one event into the materialized view.
func (s *TaskStore) apply(ev LedgerEvent) {
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
		}
		s.currentID = ev.TaskID
	case EventVerificationResult:
		t := ensureTask(s.tasks, ev.TaskID)
		if ok, _ := ev.Payload["ok"].(bool); ok {
			t.Cursor = nil
		} else if t.Cursor != nil {
			t.Cursor.Phase = PhaseVerificationPending
		}
		if _, failed := ev.Payload["failed"]; failed {
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
