package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFlockPlatformHooks verifies the raw flock hooks: non-blocking exclusive
// acquisition, contention detection between two open file descriptions of the
// same lockfile, and release.
func TestFlockPlatformHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	f1, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open f1: %v", err)
	}
	defer func() { _ = f1.Close() }()

	if err := flockExclusive(f1); err != nil {
		t.Fatalf("flockExclusive(f1): %v", err)
	}

	// A second open file description of the same file must contend.
	f2, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open f2: %v", err)
	}
	defer func() { _ = f2.Close() }()

	if err := flockExclusive(f2); !errors.Is(err, errWouldBlock) {
		t.Fatalf("flockExclusive(f2) while held = %v, want contention sentinel", err)
	}

	if err := flockRelease(f1); err != nil {
		t.Fatalf("flockRelease(f1): %v", err)
	}
	if err := flockExclusive(f2); err != nil {
		t.Fatalf("flockExclusive(f2) after release: %v", err)
	}
}

// TestLockDoubleReleaseIsSafe verifies release() is idempotent and safe after a
// failed acquire (no mutex panic, no panic on double release).
func TestLockDoubleReleaseIsSafe(t *testing.T) {
	l := newSessionLock(t.TempDir(), DefaultLockConfig())
	if err := l.release(); err != nil {
		t.Fatalf("release on never-acquired lock: %v", err)
	}
	if err := l.release(); err != nil {
		t.Fatalf("double release: %v", err)
	}
}
