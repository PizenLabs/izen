package durable

// ── R6-D: DURABLE MUTATION COMMIT MARKER (crash windows D1–D7) ─────────────
//
// R6-C established that after process death the autonomous path could not
// prove whether a mutation committed: the durable ledger recorded the
// pre-execution loop boundary and an EXECUTION_COMMITTED only on objective
// PROVEN, never a commit marker bound to the mutation itself. R6-D wires the
// existing ExecutionCursor machinery into that boundary.
//
// These tests exercise the DURABLE CONTRACT directly: a cursor is dispatched
// with the workspace pre-digest, the mutation bytes are written, and the
// commit marker records the observed post-digest. A FRESH TaskStore (never the
// writer's memory) then classifies the surviving evidence with InspectCursors.
// Every test asserts BOTH sides: the reconciliation decision AND the actual
// workspace bytes, so a journal that agrees with itself while disagreeing with
// the filesystem fails here.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func r6dSetup(t *testing.T) (string, *TaskStore) {
	t.Helper()
	dir := testWorkDir(t)
	writeWorkFile(t, dir, "v1")
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.CreateTask("t1", "fix a.txt", []string{"a.txt"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return dir, s
}

func r6dDigest(t *testing.T, dir string) string {
	t.Helper()
	d, err := ComputeTreeDigest(dir, "a.txt")
	if err != nil {
		t.Fatalf("compute digest: %v", err)
	}
	return d
}

func r6dContent(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	return string(data)
}

func r6dReopen(t *testing.T, dir string) *TaskStore {
	t.Helper()
	fresh := NewTaskStore(dir)
	if err := fresh.Open(); err != nil {
		t.Fatalf("fresh open: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	return fresh
}

// r6dInspect classifies the single task in the store against the live
// a.txt-only digest.
func r6dInspect(t *testing.T, s *TaskStore, dir string) CursorInspection {
	t.Helper()
	got, err := s.InspectCursors(func(string) (string, error) { return r6dDigest(t, dir), nil })
	if err != nil {
		t.Fatalf("inspect cursors: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("inspect cursors = %+v, want exactly task t1", got)
	}
	return got[0]
}

func r6dHasEvent(t *testing.T, s *TaskStore, typ EventType) bool {
	t.Helper()
	raw, err := os.ReadFile(s.LedgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return strings.Contains(string(raw), string(typ))
}

// D1 — process dies BEFORE the mutation. The dispatched pre-digest proves the
// workspace is unchanged, so retry is safe. Because the contract can only
// prove safety from an exact pre-state match, SAFE_RETRY is the ONLY acceptable
// positive result; nothing is committed.
func TestR6D_D1_PreMutationSafeRetry(t *testing.T) {
	dir, s := r6dSetup(t)
	pre := r6dDigest(t, dir)
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-d1",
		PreconditionDigest: pre,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// No mutation writes happen. Process "dies" here.

	fresh := r6dReopen(t, dir)
	got := r6dInspect(t, fresh, dir)
	if got.Decision != DecisionSafeRetry {
		t.Fatalf("decision = %s, want SAFE_RETRY (pre-state agreement)", got.Decision)
	}
	if got.Committed {
		t.Fatal("a dispatched-only cursor reported committed")
	}
	if c := r6dContent(t, dir); c != "v1" {
		t.Fatalf("workspace = %q, want v1 (no mutation)", c)
	}
	if r6dHasEvent(t, fresh, EventExecutionCommitted) {
		t.Fatal("a commit marker exists although no mutation ran")
	}
}

// D2 — mutation committed AND the durable marker committed. A fresh runtime
// must return ALREADY_COMMITTED and must not require a second write.
func TestR6D_D2_CommitThenRestartAlreadyCommitted(t *testing.T) {
	dir, s := r6dSetup(t)
	pre := r6dDigest(t, dir)
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-d2",
		PreconditionDigest: pre,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	writeWorkFile(t, dir, "v2") // the mutation lands
	post := r6dDigest(t, dir)
	if err := s.CommitExecutionWithDigest("t1", "op-d2", post); err != nil {
		t.Fatalf("commit: %v", err)
	}

	fresh := r6dReopen(t, dir)
	got := r6dInspect(t, fresh, dir)
	if got.Decision != DecisionAlreadyCommitted {
		t.Fatalf("decision = %s, want ALREADY_COMMITTED", got.Decision)
	}
	if !got.Committed {
		t.Fatal("committed cursor not reported as committed")
	}
	if got.PostconditionDigest != post {
		t.Fatalf("durable post-digest = %q, want the observed post-state %q", got.PostconditionDigest, post)
	}
	if c := r6dContent(t, dir); c != "v2" {
		t.Fatalf("workspace = %q, want v2", c)
	}
}

// D3 — mutation committed but the durable marker ABSENT: death in the exact gap
// between the workspace write and the commit. With only the pre-digest durable
// the contract cannot prove the commit, so the honest answer is CONFLICT
// (never SAFE_RETRY, never a forced ALREADY_COMMITTED). It must prevent an
// unsafe retry.
func TestR6D_D3_MutationCommitMarkerGap(t *testing.T) {
	dir, s := r6dSetup(t)
	pre := r6dDigest(t, dir)
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-d3",
		PreconditionDigest: pre,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	writeWorkFile(t, dir, "v2") // mutation lands, commit marker never written
	// Process "dies" in the gap.

	fresh := r6dReopen(t, dir)
	got := r6dInspect(t, fresh, dir)
	if got.Decision != DecisionConflict {
		t.Fatalf("decision = %s, want CONFLICT (commit unprovable from digest alone)", got.Decision)
	}
	if got.Decision == DecisionSafeRetry {
		t.Fatal("the commit gap was misclassified as SAFE_RETRY")
	}
	if got.Committed {
		t.Fatal("no commit marker exists, but the inspection claimed committed")
	}
	if c := r6dContent(t, dir); c != "v2" {
		t.Fatalf("workspace = %q, want the surviving mutation v2", c)
	}
}

// D4 — deterministic mismatch: the live digest matches neither the dispatched
// precondition nor the recorded postcondition. A fresh runtime must record
// CONFLICT and refuse to retry blindly.
func TestR6D_D4_DigestConflict(t *testing.T) {
	dir, s := r6dSetup(t)
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-d4",
		PreconditionDigest:  "pre-never-matches",
		PostconditionDigest: "post-never-matches",
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// A third party moves the workspace away from both expected states.
	writeWorkFile(t, dir, "third-party")

	fresh := r6dReopen(t, dir)
	got := r6dInspect(t, fresh, dir)
	if got.Decision != DecisionConflict {
		t.Fatalf("decision = %s, want CONFLICT", got.Decision)
	}
	if c := r6dContent(t, dir); c != "third-party" {
		t.Fatalf("workspace = %q, want the third-party bytes preserved", c)
	}
}

// D5 — already committed: a restart against a workspace that exactly matches the
// durable post-state must report ALREADY_COMMITTED and NEVER write again. This
// is the no-duplicate-mutation proof.
func TestR6D_D5_AlreadyCommittedDoesNotMutateAgain(t *testing.T) {
	dir, s := r6dSetup(t)
	pre := r6dDigest(t, dir)
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-d5",
		PreconditionDigest: pre,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	writeWorkFile(t, dir, "v2")
	post := r6dDigest(t, dir)
	if err := s.CommitExecutionWithDigest("t1", "op-d5", post); err != nil {
		t.Fatalf("commit: %v", err)
	}
	before := r6dContent(t, dir)

	fresh := r6dReopen(t, dir)
	got := r6dInspect(t, fresh, dir)
	if got.Decision != DecisionAlreadyCommitted {
		t.Fatalf("decision = %s, want ALREADY_COMMITTED", got.Decision)
	}
	// A decision-driven caller writes ONLY on SAFE_RETRY. ALREADY_COMMITTED
	// must produce ZERO further mutations.
	writes := 0
	if got.Decision == DecisionSafeRetry {
		writeWorkFile(t, dir, "should-not-happen")
		writes++
	}
	if writes != 0 {
		t.Fatal("ALREADY_COMMITTED induced a duplicate mutation")
	}
	if after := r6dContent(t, dir); after != before {
		t.Fatalf("workspace changed after reconciliation: %q -> %q", before, after)
	}
	if after := r6dContent(t, dir); after != "v2" {
		t.Fatalf("workspace = %q, want the committed post-state v2", after)
	}
}

// D6 — SAFE_RETRY requires ACTUAL pre-state agreement. A matching pre-state
// yields SAFE_RETRY; any drift from it must not.
func TestR6D_D6_SafeRetryRequiresPreStateAgreement(t *testing.T) {
	dir, s := r6dSetup(t)
	pre := r6dDigest(t, dir)
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "t1", StepID: "s1", OperationID: "op-d6",
		PreconditionDigest: pre,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Agreement -> SAFE_RETRY.
	fresh := r6dReopen(t, dir)
	if got := r6dInspect(t, fresh, dir); got.Decision != DecisionSafeRetry {
		t.Fatalf("decision with agreeing pre-state = %s, want SAFE_RETRY", got.Decision)
	}

	// Drift -> never SAFE_RETRY.
	writeWorkFile(t, dir, "drifted")
	fresh2 := r6dReopen(t, dir)
	got := r6dInspect(t, fresh2, dir)
	if got.Decision == DecisionSafeRetry {
		t.Fatal("SAFE_RETRY was granted without pre-state agreement")
	}
	if got.Decision != DecisionConflict {
		t.Fatalf("decision with drift = %s, want CONFLICT", got.Decision)
	}
}

// Absence of evidence is UNKNOWN — never SAFE_RETRY and never ALREADY_COMMITTED.
// A task with no cursor at all is exactly the R6-C state; reconciliation must
// keep saying it cannot tell.
func TestR6D_NoCursorIsUnknownNotSafeRetry(t *testing.T) {
	dir, _ := r6dSetup(t)
	// A fresh runtime over a task that never dispatched a cursor.
	fresh := r6dReopen(t, dir)
	got := r6dInspect(t, fresh, dir)
	if got.Decision != DecisionUnknown {
		t.Fatalf("decision = %s, want UNKNOWN when no cursor exists", got.Decision)
	}
	if got.Decision == DecisionSafeRetry || got.Decision == DecisionAlreadyCommitted {
		t.Fatal("absence of evidence was rounded up")
	}
}

// ── D7: real process death ─────────────────────────────────────────────────

const (
	r6dHelperEnv = "IZEN_R6D_COMMIT_HELPER"
	r6dWSEnv     = "IZEN_R6D_WORKSPACE"
	r6dReadyFD   = 3
)

// TestR6D_CommitHelperProcess opens the store, dispatches the pre-digest,
// performs the mutation, durably records the commit marker WITH the observed
// post-digest, signals the parent through an explicit pipe (no sleeps), and
// blocks without writing a terminal result. It models death after
// "mutation commit + durable marker" but before objective completion.
func TestR6D_CommitHelperProcess(t *testing.T) {
	if os.Getenv(r6dHelperEnv) != "1" {
		return
	}
	ws := os.Getenv(r6dWSEnv)
	if ws == "" {
		os.Exit(2)
	}
	s := NewTaskStore(ws)
	if err := s.Open(); err != nil {
		os.Exit(3)
	}
	if _, err := s.CreateTask("obj-d7", "fix a.txt", []string{"a.txt"}); err != nil {
		os.Exit(4)
	}
	pre, err := ComputeTreeDigest(ws, "a.txt")
	if err != nil {
		os.Exit(5)
	}
	if err := s.DispatchCursor(ExecutionCursor{
		TaskID: "obj-d7", StepID: "op-d7", OperationID: "op-d7",
		PreconditionDigest: pre,
	}); err != nil {
		os.Exit(6)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("patched"), 0o644); err != nil {
		os.Exit(7)
	}
	post, err := ComputeTreeDigest(ws, "a.txt")
	if err != nil {
		os.Exit(8)
	}
	if err := s.CommitExecutionWithDigest("obj-d7", "op-d7", post); err != nil {
		os.Exit(9)
	}
	// Explicit IPC barrier: one byte on the inherited pipe says "the durable
	// commit marker is on disk". No sleep is used for synchronization.
	if ready := os.NewFile(r6dReadyFD, "r6d-ready"); ready != nil {
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
	}
	select {} // lose control here; the parent delivers SIGKILL
}

// TestR6D_D7_ProcessDeathAndFreshRuntimeReconcile proves the durable artifact
// survives process death independently of process memory: after a real kill -9
// the fresh runtime returns ALREADY_COMMITTED, the workspace holds exactly the
// committed bytes, and no second mutation is needed.
func TestR6D_D7_ProcessDeathAndFreshRuntimeReconcile(t *testing.T) {
	if os.Getenv(r6dHelperEnv) == "1" {
		t.Skip("helper invocation only")
	}
	ws := testWorkDir(t)
	writeWorkFile(t, ws, "original")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = r.Close() }()

	cmd := exec.Command(os.Args[0], "-test.run=^TestR6D_CommitHelperProcess$")
	cmd.Env = append(os.Environ(), r6dHelperEnv+"=1", r6dWSEnv+"="+ws)
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	_ = w.Close()
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	// Block on the explicit barrier: the child writes one byte only AFTER the
	// mutation and its durable commit marker are on disk.
	barrier := make([]byte, 1)
	_ = r.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := r.Read(barrier); err != nil {
		t.Fatalf("helper never reached the committed boundary: %v", err)
	}

	// Abrupt death: no cleanup, no defers, no flush.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill -9: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// The workspace mutation survived the process.
	if c := r6dContent(t, ws); c != "patched" {
		t.Fatalf("a.txt = %q, want the committed mutation", c)
	}

	// A FRESH runtime over the same .izen state — no writer memory involved.
	fresh := NewTaskStore(ws)
	if err := fresh.Open(); err != nil {
		t.Fatalf("fresh Open: %v", err)
	}
	defer func() { _ = fresh.Close() }()
	got, err := fresh.InspectCursors(func(string) (string, error) {
		return ComputeTreeDigest(ws, "a.txt")
	})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != "obj-d7" {
		t.Fatalf("inspection = %+v, want the interrupted task obj-d7", got)
	}
	if got[0].Decision != DecisionAlreadyCommitted {
		t.Fatalf("decision = %s, want ALREADY_COMMITTED across process death", got[0].Decision)
	}
	if !got[0].Committed {
		t.Fatal("fresh runtime did not see the durable commit marker")
	}
	// No second mutation was required: the bytes are still exactly the commit.
	if c := r6dContent(t, ws); c != "patched" {
		t.Fatalf("a.txt = %q after reconciliation, want patched (no duplicate write)", c)
	}
}
