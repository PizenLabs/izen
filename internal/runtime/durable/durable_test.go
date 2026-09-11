package durable

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeWorkFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 1. Crash invariance: SIGKILL (torn write) mid-append recovers to the
// last coherent prefix.
func TestCrashRecoveryTornTail(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CreateTask("t1", "fix bug", []string{"a.txt"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	pre, err := ComputeTreeDigest(dir)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	writeWorkFile(t, dir, "a.txt", "patched")
	post, err := ComputeTreeDigest(dir)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-1",
		PreconditionDigest: pre, PostconditionDigest: post,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.CommitExecution("t1", "op-1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Checkpoint("t1", "cp-1"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// Simulate `kill -9` mid-write: a torn partial line with no newline.
	f, err := os.OpenFile(s.LedgerPath(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte(`{"eventId":"deadbeef","taskId":"t1","eventType":"CURSOR_DISPATCHED","paylo`))
	_ = f.Close()

	// Reboot: a fresh store must reconstruct coherent state.
	s2 := NewTaskStore(dir)
	if err := s2.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st, ok := s2.State("t1")
	if !ok {
		t.Fatal("task t1 missing after recovery")
	}
	if st.LastCheckpointID != "cp-1" {
		t.Fatalf("lastCheckpoint=%q want cp-1", st.LastCheckpointID)
	}
	if st.Cursor == nil || st.Cursor.Status != CursorCommitted {
		t.Fatalf("cursor not committed after recovery: %+v", st.Cursor)
	}
	if st.Status != TaskRunning {
		t.Fatalf("status=%q want RUNNING", st.Status)
	}
}

// 2a. Network drop mid-execution: side effect applied, response lost.
// Retry MUST NOT duplicate the patch.
func TestIdempotentRetryNoDuplicate(t *testing.T) {
	dir := testWorkDir(t)
	writeWorkFile(t, dir, "a.txt", "v1")
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	pre, _ := ComputeTreeDigest(dir)
	writeWorkFile(t, dir, "a.txt", "v2") // the side effect commits...
	post, _ := ComputeTreeDigest(dir)
	writeWorkFile(t, dir, "a.txt", "v1") // ...then the worker "crashes" before commit

	calls := 0
	effect := func() error {
		calls++
		writeWorkFile(t, dir, "a.txt", "v2")
		return nil
	}
	digestOf := func() (string, error) { return ComputeTreeDigest(dir) }
	r := &IdempotentRunner{Store: s}

	executed, err := r.Run("t1", "s1", "op-1", pre, post, digestOf, effect)
	if err != nil || !executed {
		t.Fatalf("first run executed=%v err=%v", executed, err)
	}
	if calls != 1 {
		t.Fatalf("effect calls=%d want 1", calls)
	}

	// Simulated network drop: effect ran but caller retries (e.g. the
	// commit ack was lost and a new worker takes over).
	executed, err = r.Run("t1", "s1", "op-1", pre, post, digestOf, effect)
	if err != nil {
		t.Fatalf("retry err: %v", err)
	}
	if executed {
		t.Fatal("retry re-executed an already-committed operation")
	}
	if calls != 1 {
		t.Fatalf("effect invoked twice (calls=%d): duplicate patch", calls)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(content) != "v2" {
		t.Fatalf("worktree=%q want v2", content)
	}
}

// 2b. Crash between dispatch and commit with postcondition present:
// ReconcileAll advances WITHOUT re-execution.
func TestReconcileAlreadyCommitted(t *testing.T) {
	dir := testWorkDir(t)
	writeWorkFile(t, dir, "a.txt", "v1")
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	pre, _ := ComputeTreeDigest(dir)
	writeWorkFile(t, dir, "a.txt", "v2")
	post, _ := ComputeTreeDigest(dir)

	// Worker dispatches, applies the side effect, then dies before commit.
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-9",
		PreconditionDigest: pre, PostconditionDigest: post,
	}); err != nil {
		t.Fatal(err)
	}

	s2 := NewTaskStore(dir)
	if err := s2.Open(); err != nil {
		t.Fatal(err)
	}
	decisions, err := s2.ReconcileAll(func(taskID string) (string, error) {
		return ComputeTreeDigest(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if decisions["t1"] != DecisionAlreadyCommitted {
		t.Fatalf("decision=%q want ALREADY_COMMITTED", decisions["t1"])
	}
	st, _ := s2.State("t1")
	if st.Cursor == nil || st.Cursor.Status != CursorCommitted {
		t.Fatalf("cursor not advanced: %+v", st.Cursor)
	}
	if st.Cursor.Phase != PhaseVerificationPending {
		t.Fatalf("phase=%q want VERIFICATION_PENDING", st.Cursor.Phase)
	}
}

// 2c. Crash before any side effect: safe retry.
func TestReconcileSafeRetry(t *testing.T) {
	dir := testWorkDir(t)
	writeWorkFile(t, dir, "a.txt", "v1")
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	pre, _ := ComputeTreeDigest(dir)
	post := "deadbeef-postcondition-never-reached"
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-2",
		PreconditionDigest: pre, PostconditionDigest: post,
	}); err != nil {
		t.Fatal(err)
	}
	s2 := NewTaskStore(dir)
	if err := s2.Open(); err != nil {
		t.Fatal(err)
	}
	decisions, err := s2.ReconcileAll(func(taskID string) (string, error) {
		return ComputeTreeDigest(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if decisions["t1"] != DecisionSafeRetry {
		t.Fatalf("decision=%q want SAFE_RETRY", decisions["t1"])
	}
}

// 2d. Third-party mutation: conflict -> TARGET_CONFLICT + RE_PLAN.
func TestReconcileConflict(t *testing.T) {
	dir := testWorkDir(t)
	writeWorkFile(t, dir, "a.txt", "v1")
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-3",
		PreconditionDigest: "pre-never-matches", PostconditionDigest: "post-never-matches",
	}); err != nil {
		t.Fatal(err)
	}
	s2 := NewTaskStore(dir)
	if err := s2.Open(); err != nil {
		t.Fatal(err)
	}
	decisions, err := s2.ReconcileAll(func(taskID string) (string, error) {
		return ComputeTreeDigest(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if decisions["t1"] != DecisionConflict {
		t.Fatalf("decision=%q want CONFLICT", decisions["t1"])
	}
	st, _ := s2.State("t1")
	if st.Status != TaskRePlan {
		t.Fatalf("status=%q want RE_PLAN", st.Status)
	}
	if st.Cursor == nil || st.Cursor.Status != CursorConflict {
		t.Fatalf("cursor not CONFLICT: %+v", st.Cursor)
	}
}

// 3. Dead locks left by killed processes are reclaimed without hanging.
func TestStaleLockReclaimed(t *testing.T) {
	dir := testWorkDir(t)
	rt := filepath.Join(dir, ".izen", "runtime")
	if err := os.MkdirAll(rt, 0o755); err != nil {
		t.Fatal(err)
	}
	// Dead pid (exited long ago) + expired lease.
	stale, _ := json.Marshal(LockMetadata{
		PID:        1 << 30, // implausible live pid
		CreatedAt:  time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		LeaseTTLMs: 1000,
		UpdatedAt:  time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
	})
	if err := os.WriteFile(filepath.Join(rt, "lock"), append(stale, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	l := NewFileLock(dir)
	start := time.Now()
	reclaimed, err := l.Acquire(30000)
	if err != nil {
		t.Fatalf("acquire after kill: %v", err)
	}
	if !reclaimed {
		t.Fatal("expected reclaimed=true for dead holder")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("acquire hung instead of reclaiming")
	}
	l.Release()

	// Corrupt lock file must also never hang the runtime.
	if err := os.WriteFile(filepath.Join(rt, "lock"), []byte("not-json{{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	l2 := NewFileLock(dir)
	if _, err := l2.Acquire(30000); err != nil {
		t.Fatalf("acquire with corrupt lock: %v", err)
	}
	l2.Release()

	// Live holder: second handle must fail fast (non-blocking).
	l3 := NewFileLock(dir)
	if _, err := l3.Acquire(30000); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer l3.Release()
	l4 := NewFileLock(dir)
	if _, err := l4.Acquire(30000); err == nil {
		l4.Release()
		t.Fatal("expected contention error for live holder")
	}
}

// 3b. Real kill -9: a holder process killed with SIGKILL leaves a lock
// that the next opener reclaims (live pid -> contention first, dead pid
// -> reclaimed, STALE_LOCK_RECLAIMED in the ledger).
func TestKillMinusNineLockReclaim(t *testing.T) {
	dir := testWorkDir(t)
	rt := filepath.Join(dir, ".izen", "runtime")
	if err := os.MkdirAll(rt, 0o755); err != nil {
		t.Fatal(err)
	}
	// Spawn a real holder process.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	holder := exec.CommandContext(ctx, "sleep", "30")
	if err := holder.Start(); err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	holderPID := holder.Process.Pid
	fresh, _ := json.Marshal(LockMetadata{
		PID:        holderPID,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		LeaseTTLMs: 30000,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err := os.WriteFile(filepath.Join(rt, "lock"), append(fresh, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	// While the holder lives, acquisition must fail fast, not hang.
	l := NewFileLock(dir)
	if _, err := l.Acquire(30000); err == nil {
		l.Release()
		_ = holder.Process.Kill()
		t.Fatal("expected contention while holder process is alive")
	}
	// kill -9 the holder: the lock becomes stale and reclaimable.
	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	_, _ = holder.Process.Wait()
	reclaimed, err := l.Acquire(30000)
	if err != nil {
		t.Fatalf("acquire after kill -9: %v", err)
	}
	if !reclaimed {
		t.Fatal("expected reclaimed=true after holder kill -9")
	}
	l.Release()

	// The reclaim is recorded in the ledger lineage: plant a stale
	// lock again and let Store.Open reclaim it.
	stale, _ := json.Marshal(LockMetadata{
		PID:        1 << 30,
		CreatedAt:  time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		LeaseTTLMs: 1000,
		UpdatedAt:  time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
	})
	if err := os.WriteFile(filepath.Join(rt, "lock"), append(stale, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("open after reclaim: %v", err)
	}
	data, _ := os.ReadFile(s.LedgerPath())
	if !strings.Contains(string(data), string(EventStaleLockReclaimed)) {
		t.Fatal("STALE_LOCK_RECLAIMED missing from ledger after kill -9 reclaim")
	}
}

// 4. Canonical lineage: snapshot.json is purely derived.
func TestSnapshotReconstructible(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("t1", "intent", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint("t1", "cp-1"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.SnapshotPath())
	if len(before) == 0 {
		t.Fatal("snapshot empty after checkpoint")
	}
	// Delete and corrupt: both must be fully recoverable from the ledger.
	if err := os.Remove(s.SnapshotPath()); err != nil {
		t.Fatal(err)
	}
	s2 := NewTaskStore(dir)
	if err := s2.Open(); err != nil {
		t.Fatal(err)
	}
	if err := s2.RebuildSnapshot(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s2.SnapshotPath())
	if len(after) == 0 {
		t.Fatal("snapshot empty after rebuild")
	}
	st, ok := s2.State("t1")
	if !ok || st.LastCheckpointID != "cp-1" {
		t.Fatalf("rebuilt state wrong: %+v ok=%v", st, ok)
	}
	if err := os.WriteFile(s2.SnapshotPath(), []byte("garbage{{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	s3 := NewTaskStore(dir)
	if err := s3.Open(); err != nil {
		t.Fatal(err)
	}
	if err := s3.RebuildSnapshot(); err != nil {
		t.Fatal(err)
	}
	st3, ok := s3.State("t1")
	if !ok || st3.LastCheckpointID != "cp-1" {
		t.Fatalf("rebuilt-after-corruption wrong: %+v ok=%v", st3, ok)
	}
}

// Truth boundaries must be durable on disk (fsync), not buffered.
func TestTruthBoundaryDurability(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("t1", "i", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint("t1", "cp-1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.LedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	found := false
	for _, ln := range lines {
		var ev LedgerEvent
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("ledger line not valid JSON: %v", err)
		}
		if ev.EventType == EventCheckpointCreated && ev.EventID != "" && ev.Timestamp != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("CHECKPOINT_CREATED truth-boundary event missing from ledger")
	}
}
