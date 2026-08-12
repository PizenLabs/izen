package review

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// initRealGitRepo creates a real (committed) git repo at root so `git status`
// actually reflects the working tree — a bare ".git" directory is not enough
// for the diff fast-path probes.
func initRealGitRepo(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=izen test", "GIT_AUTHOR_EMAIL=test@izen.dev",
			"GIT_COMMITTER_NAME=izen test", "GIT_COMMITTER_EMAIL=test@izen.dev",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "base.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write base.go: %v", err)
	}
	run("add", "base.go")
	run("commit", "-m", "baseline")
}

// TestEngineRunRespectsPreCancelledContext asserts a pre-cancelled context
// aborts the review pipeline at the first state boundary instead of running
// the whole (potentially slow) pipeline. This is the engine side of the UI's
// Esc / 30s-timeout cancellation contract.
func TestEngineRunRespectsPreCancelledContext(t *testing.T) {
	dir := t.TempDir()
	initRealGitRepo(t, dir)
	_ = os.WriteFile(filepath.Join(dir, "changed.go"), []byte("package main\n"), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	e := NewEngine(dir, &mockRetriever{}, nil).WithContext(ctx)
	start := time.Now()
	_, err := e.Run()
	if err == nil {
		t.Fatal("Run with pre-cancelled context must return an error")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.Canceled/DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Run took %v with a pre-cancelled context — must abort immediately", elapsed)
	}
}

// TestEngineRunDeadlineExpiredAborts asserts a deadline-expired context aborts
// the pipeline rather than hanging (the UI falls back to a 30s watchdog).
func TestEngineRunDeadlineExpiredAborts(t *testing.T) {
	dir := t.TempDir()
	initRealGitRepo(t, dir)
	_ = os.WriteFile(filepath.Join(dir, "changed.go"), []byte("package main\n"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// Let the deadline expire before the pipeline reaches its first boundary.
	<-ctx.Done()

	e := NewEngine(dir, &mockRetriever{}, nil).WithContext(ctx)
	start := time.Now()
	_, err := e.Run()
	if err == nil {
		t.Fatal("Run with an expired deadline must return an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Run took %v after deadline expiry — must abort immediately", elapsed)
	}
}

// TestEngineIsCleanWorkingTree asserts the fast-path probe used by the TUI to
// skip the spinner entirely on a clean tree: true for a committed repo, false
// once the tree has changes, and false for a target-scoped audit.
func TestEngineIsCleanWorkingTree(t *testing.T) {
	dir := t.TempDir()
	initRealGitRepo(t, dir)

	e := NewEngine(dir, &mockRetriever{}, nil)
	if !e.IsCleanWorkingTree() {
		t.Error("IsCleanWorkingTree = false for a committed repo, want true")
	}

	_ = os.WriteFile(filepath.Join(dir, "changed.go"), []byte("package main\n"), 0644)
	if e.IsCleanWorkingTree() {
		t.Error("IsCleanWorkingTree = true for a tree with changes, want false")
	}

	// Target-scoped audits are never "clean": an explicit target is reviewable.
	e2 := NewEngine(dir, &mockRetriever{}, nil)
	_, _ = e2.RunTarget("changed.go")
	if e2.IsCleanWorkingTree() {
		t.Error("IsCleanWorkingTree = true for a target-scoped audit, want false")
	}
}
