package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This file proves the cross-process tier of the two-tier lock with a REAL
// second process: a helper subprocess (the same test binary) holds the flock on
// the shared lockfile while the parent's SessionManager operation must time out,
// then proceed once the helper releases it.

// TestHelperProcess is the cross-process lock holder. It is only executed when
// spawned by TestCrossProcessFlockContention (env-guarded). It acquires the
// flock, publishes a .ready marker, waits for a .release marker, then exits.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("IZEN_TEST_HELPER") != "1" {
		return
	}
	lockpath := os.Getenv("IZEN_TEST_LOCKPATH")
	if lockpath == "" {
		os.Exit(2)
	}
	f, err := os.OpenFile(lockpath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		os.Exit(3)
	}
	if err := flockExclusive(f); err != nil {
		_ = f.Close()
		os.Exit(4)
	}
	_ = os.WriteFile(lockpath+".ready", []byte("ready"), 0644)
	for {
		if _, err := os.Stat(lockpath + ".release"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = flockRelease(f)
	_ = f.Close()
	os.Exit(0)
}

// TestManagerBlocksWhileHelperHoldsLock is the parent-side assertion: while a
// real subprocess holds the flock, the manager's operation times out; after the
// helper exits, the same operation succeeds.
func TestManagerBlocksWhileHelperHoldsLock(t *testing.T) {
	if os.Getenv("IZEN_TEST_HELPER") == "1" {
		t.Skip("helper invocation only")
	}
	root := t.TempDir()
	lockpath := filepath.Join(root, ".izen", "sessions", ".lock")

	// Seed the workspace so the manager has a real session to persist.
	seed := NewManager(root, WithLockConfig(LockConfig{Timeout: 2 * time.Second, Backoff: 5 * time.Millisecond}))
	if err := seed.Open(context.Background()); err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed.Session().Objective = "cross-process"
	if err := seed.Persist(context.Background()); err != nil {
		t.Fatalf("seed Persist: %v", err)
	}
	_ = seed.Close()

	// Open the manager BEFORE the helper takes the flock; Open itself needs
	// the lock to resolve the pointer.
	m := NewManager(root, WithLockConfig(LockConfig{Timeout: 300 * time.Millisecond, Backoff: 5 * time.Millisecond}))
	if err := m.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = m.Close() }()

	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(),
		"IZEN_TEST_HELPER=1",
		"IZEN_TEST_LOCKPATH="+lockpath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	defer func() {
		_ = os.WriteFile(lockpath+".release", []byte("go"), 0644)
		_ = cmd.Wait()
	}()

	// Wait for the helper to hold the flock.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(lockpath + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never acquired the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := m.Persist(context.Background()); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("Persist while subprocess holds flock = %v, want ErrLockTimeout", err)
	}

	// Release the helper (deferred), then it must proceed.
	_ = os.WriteFile(lockpath+".release", []byte("go"), 0644)
	_ = cmd.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for {
		err := m.Persist(context.Background())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Persist after helper release never succeeded: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
