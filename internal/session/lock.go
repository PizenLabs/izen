package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Two-Tier Session Locking (Concurrency Layering).
//
// Every session mutation (/new, /session resume, pointer commit) is serialized
// behind two independent tiers:
//
//  1. In-process tier: a sync.Mutex guards async task safety within a single
//     process (the TUI's own goroutines, background preflight, autonomy loop).
//  2. Cross-process tier: a NON-BLOCKING OS flock on a well-known lockfile
//     under .izen/sessions/. Two processes (two `izen` invocations on the same
//     workspace) contend on the same open-file-description lock. Acquisition
//     is non-blocking (LOCK_NB) with a configurable timeout/backoff retry loop
//     so a stuck peer fails the operation deterministically (ErrLockTimeout)
//     instead of hanging forever.
//
// The platform flock is deliberately NOT a hard architectural invariant: on
// platforms without flock support the OS tier degrades to a no-op and the
// in-process mutex remains authoritative. This is reported by
// ErrLockUnsupported only when the caller asks for the raw platform capability.

// ErrLockTimeout is returned when the cross-process flock could not be
// acquired within the configured timeout.
var ErrLockTimeout = errors.New("session: timed out acquiring cross-process session lock")

// ErrLockUnsupported is returned by the platform flock hook when the OS does
// not provide file locking for the current platform.
var ErrLockUnsupported = errors.New("session: OS file locking unsupported on this platform")

// LockConfig tunes the cross-process flock acquisition loop. Both values are
// configurable via Manager options — never hardcoded as architectural
// invariants.
type LockConfig struct {
	// Timeout bounds how long acquire() retries a contended flock.
	Timeout time.Duration
	// Backoff is the pause between non-blocking flock attempts.
	Backoff time.Duration
}

// DefaultLockConfig is the production default. 5s is long enough for a peer
// holding the lock through a single session persistence (sub-100ms) while
// still failing deterministically instead of hanging.
func DefaultLockConfig() LockConfig {
	return LockConfig{Timeout: 5 * time.Second, Backoff: 25 * time.Millisecond}
}

// sessionLock is the two-tier lock. It must be created via newSessionLock and
// released via release() — never copied after first use.
//
// Ownership model: acquire() takes the in-process mutex and HOLDS it across the
// caller's critical section; release() is invoked by the same goroutine at the
// end of the section, releases the flock, and unlocks the mutex last (so a
// concurrent acquirer can only proceed once the flock is free). A failed
// acquire (timeout/cancellation) leaves the mutex unlocked and heldFile nil, so
// a subsequent release() is a safe idempotent no-op.
type sessionLock struct {
	// mu is the in-process tier: it serializes whole critical sections. The
	// owner goroutine holds it from acquire() until release().
	mu sync.Mutex
	// fileMu guards heldFile so Close() from another goroutine never races an
	// in-flight release(). The ownership checks are on the same goroutine;
	// fileMu only makes the state transition safe for the shutdown path.
	fileMu   sync.Mutex
	path     string
	timeout  time.Duration
	backoff  time.Duration
	heldFile *os.File

	// flockUnsupported is set when the OS tier is unavailable so acquire()
	// reports the degraded mode exactly once instead of failing every
	// acquisition.
	flockUnsupported bool
}

func newSessionLock(dir string, cfg LockConfig) *sessionLock {
	return &sessionLock{
		path:    filepath.Join(dir, ".lock"),
		timeout: cfg.Timeout,
		backoff: cfg.Backoff,
	}
}

// acquire takes the in-process mutex, then the cross-process flock with
// timeout/backoff. It returns ErrLockTimeout (or ctx.Err()) without holding
// anything when acquisition fails.
func (l *sessionLock) acquire(ctx context.Context) error {
	l.mu.Lock()

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		l.mu.Unlock()
		return err
	}

	deadline := time.Now().Add(l.timeout)
	var lastErr error
	for {
		err = flockExclusive(f)
		switch {
		case err == nil:
			l.fileMu.Lock()
			l.heldFile = f
			l.fileMu.Unlock()
			return nil
		case errors.Is(err, ErrLockUnsupported):
			// OS tier unavailable: degrade to in-process tier only. The open
			// file is kept so the lock file exists for cross-process readers.
			l.flockUnsupported = true
			l.fileMu.Lock()
			l.heldFile = f
			l.fileMu.Unlock()
			return nil
		case errors.Is(err, errWouldBlock):
			lastErr = ErrLockTimeout
		default:
			_ = f.Close()
			l.mu.Unlock()
			return err
		}

		if time.Now().After(deadline) {
			_ = f.Close()
			l.mu.Unlock()
			return lastErr
		}
		timer := time.NewTimer(l.backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = f.Close()
			l.mu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// release drops the cross-process flock and the in-process mutex, in that
// order. It is idempotent and safe to call after a failed acquire or as a
// double release.
func (l *sessionLock) release() error {
	l.fileMu.Lock()
	f := l.heldFile
	l.heldFile = nil
	l.fileMu.Unlock()
	if f == nil {
		return nil
	}
	unlockErr := flockRelease(f)
	closeErr := f.Close()
	l.mu.Unlock()
	if unlockErr != nil && !errors.Is(unlockErr, ErrLockUnsupported) {
		return unlockErr
	}
	return closeErr
}

// close removes the lockfile. Callers must release() first. Best-effort: a
// leftover lockfile is harmless (flock is advisory; a stale file is ignored).
func (l *sessionLock) close() {
	_ = os.Remove(l.path)
}
