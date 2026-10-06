package durable

// ── R6-C: ABRUPT PROCESS DEATH (cases C5 + C8) ─────────────────────────────
//
// This is the most important forensic scenario of R6-C. It answers, with a REAL
// kill -9:
//
//	Before death: a durable task exists, a loop-boundary checkpoint exists, and
//	              the mutation's bytes have landed on disk.
//	After death:  the workspace holds the mutation; the ledger holds no commit.
//	Fresh start:  the task is surfaced as in-flight, but NOTHING durable
//	              distinguishes "mutation committed" from "mutation never
//	              started". The exact state is UNKNOWN and must not be assumed.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	r6cHelperEnv = "IZEN_R6C_CRASH_HELPER"
	r6cWSEnv     = "IZEN_R6C_WORKSPACE"
)

// TestR6C_CrashHelperProcess is the durable probe. It records an in-flight task
// and a loop-boundary checkpoint (the last durable state the autonomy driver
// writes before execution), performs a real workspace mutation, then blocks
// without ever recording a commit or a terminal result. It models a process
// that dies after mutating but before success is recorded.
func TestR6C_CrashHelperProcess(t *testing.T) {
	if os.Getenv(r6cHelperEnv) != "1" {
		return
	}
	ws := os.Getenv(r6cWSEnv)
	if ws == "" {
		os.Exit(2)
	}
	s := NewTaskStore(ws)
	if err := s.Open(); err != nil {
		os.Exit(3)
	}
	if _, err := s.CreateTask("obj-crash", "fix the greeting", []string{"a.txt"}); err != nil {
		os.Exit(4)
	}
	// The autonomy driver checkpoints the loop boundary BEFORE execution.
	if err := s.Checkpoint("obj-crash", "cp-deciding"); err != nil {
		os.Exit(5)
	}
	// The mutation's bytes have already landed on disk (the kernel write
	// happened), but no EXECUTION_COMMITTED and no terminal result exist.
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("patched"), 0o644); err != nil {
		os.Exit(6)
	}
	if err := os.WriteFile(filepath.Join(ws, ".r6c-ready"), []byte("ready"), 0o644); err != nil {
		os.Exit(7)
	}
	select {} // lose control here; the parent delivers SIGKILL
}

// TestR6C_AbruptProcessDeathLeavesMutationStateUnreconstructible is case C8
// (and C5). After a real kill -9 at a post-mutation / pre-commit boundary, a
// FRESH runtime over the surviving .izen state can see that the task was in
// flight and that the workspace changed — but it has NO durable marker that can
// distinguish "mutation committed" from "mutation never started".
func TestR6C_AbruptProcessDeathLeavesMutationStateUnreconstructible(t *testing.T) {
	if os.Getenv(r6cHelperEnv) == "1" {
		t.Skip("helper invocation only")
	}
	ws := testWorkDir(t)
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestR6C_CrashHelperProcess$")
	cmd.Env = append(os.Environ(), r6cHelperEnv+"=1", r6cWSEnv+"="+ws)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	// Wait for the helper to reach the post-mutation / pre-commit boundary.
	ready := filepath.Join(ws, ".r6c-ready")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never reached the post-mutation boundary")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Abrupt death: no cleanup, no defers, no flush.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill -9: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// The workspace mutation survived the process.
	got, err := os.ReadFile(filepath.Join(ws, "a.txt"))
	if err != nil {
		t.Fatalf("read mutated file: %v", err)
	}
	if string(got) != "patched" {
		t.Fatalf("a.txt = %q, want the mutation to have survived the kill", got)
	}

	// A FRESH runtime over the same .izen state.
	fresh := NewTaskStore(ws)
	if err := fresh.Open(); err != nil {
		t.Fatalf("fresh Open: %v", err)
	}
	defer func() { _ = fresh.Close() }()

	recoverable := fresh.RecoverableTasks()
	if len(recoverable) != 1 {
		t.Fatalf("recoverable tasks = %d, want exactly the interrupted task surfaced", len(recoverable))
	}
	st, ok := fresh.State("obj-crash")
	if !ok {
		t.Fatal("the interrupted task is absent from the fresh runtime")
	}
	if st.Status.Terminal() {
		t.Fatalf("the interrupted task replayed as terminal (%s)", st.Status)
	}
	// The decisive gap: no execution cursor / commit marker exists, because the
	// autonomy path never dispatched one for the mutation. The fresh runtime
	// therefore cannot tell whether the mutation committed.
	if st.Cursor != nil {
		t.Fatalf("unexpected cursor %+v: the autonomy path must not fabricate a commit marker", st.Cursor)
	}
	ledger, err := os.ReadFile(fresh.LedgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if strings.Contains(string(ledger), string(EventExecutionCommitted)) {
		t.Fatal("the interrupted mutation wrote an EXECUTION_COMMITTED it never earned")
	}
	// The last durable checkpoint is the pre-execution loop boundary, not a
	// post-mutation record.
	if st.LastCheckpointID != "cp-deciding" {
		t.Fatalf("last checkpoint = %q, want the pre-execution boundary", st.LastCheckpointID)
	}
}
