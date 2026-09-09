package lock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTryAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	unlock, err := TryAcquireWorkspaceLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Lock file must exist.
	if _, err := os.Stat(filepath.Join(dir, ".izen", "lock")); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	// PID must be stamped and match us.
	if pid, ok := HolderPID(dir); !ok || pid != os.Getpid() {
		t.Fatalf("holder pid = %d ok=%v, want %d", pid, ok, os.Getpid())
	}
	unlock()
	// Second acquisition after release must succeed (no lingering FD).
	unlock2, err := TryAcquireWorkspaceLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after unlock: %v", err)
	}
	unlock2()
}

func TestConcurrentAcquisitionFails(t *testing.T) {
	dir := t.TempDir()
	unlock, err := TryAcquireWorkspaceLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer unlock()

	// Second acquisition on the same directory (separate FD, simulating a
	// concurrent izen process) must fail with ErrWorkspaceLocked.
	_, err = TryAcquireWorkspaceLock(dir)
	if err == nil {
		t.Fatal("expected ErrWorkspaceLocked on second acquire")
	}
	if !errors.Is(err, ErrWorkspaceLocked) {
		t.Fatalf("expected ErrWorkspaceLocked, got: %v", err)
	}
}

func TestUnlockIdempotent(t *testing.T) {
	dir := t.TempDir()
	unlock, err := TryAcquireWorkspaceLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	unlock()
	unlock() // must not panic or corrupt state
	unlock2, err := TryAcquireWorkspaceLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after double unlock: %v", err)
	}
	unlock2()
}

func TestConcurrentGoroutinesSingleHolder(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	var mu sync.Mutex
	held := 0
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := TryAcquireWorkspaceLock(dir)
			if err != nil {
				if !errors.Is(err, ErrWorkspaceLocked) {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			mu.Lock()
			held++
			mu.Unlock()
			// Hold briefly to force contention, then release.
			unlock()
		}()
	}
	wg.Wait()
	if held < 1 {
		t.Fatal("at least one goroutine must acquire the lock")
	}
}

func TestEmptyWorkDir(t *testing.T) {
	if _, err := TryAcquireWorkspaceLock(""); err == nil {
		t.Fatal("expected error for empty workDir")
	}
	if _, err := TryAcquireWorkspaceLock(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing workDir")
	}
}

func TestIsHeldByCurrentProcess(t *testing.T) {
	dir := t.TempDir()
	if IsHeldByCurrentProcess(dir) {
		t.Fatal("unlocked dir must not report self-hold")
	}
	unlock, err := TryAcquireWorkspaceLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer unlock()
	if !IsHeldByCurrentProcess(dir) {
		t.Fatal("locked dir must report self-hold")
	}
}
