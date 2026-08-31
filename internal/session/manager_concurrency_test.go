package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConcurrentSessionSwitching hammers the manager with concurrent /new and
// /session resume operations and verifies the pointer invariant holds at every
// step: the active pointer names exactly one valid slot and that slot always
// holds a durable session record.
func TestConcurrentSessionSwitching(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	m.Session().Objective = "seed"
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	// Pre-seed the dormant slot so both slots always exist from here on:
	// ResumeSession(dormant) is only valid against a slot with durable data.
	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("seed NewSession: %v", err)
	}

	const workers = 8
	const opsPerWorker = 12

	// Note: m.Active()/pointer reads are safe — the manager's mutex
	// serializes the in-memory mirror. The persistence writes serialize on the
	// two-tier lock.
	var wg sync.WaitGroup
	errCh := make(chan error, workers*opsPerWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				var err error
				if (w+i)%2 == 0 {
					_, err = m.NewSession(context.Background())
				} else {
					target := SlotA
					if m.Active() == SlotA {
						target = SlotB
					}
					_, err = m.ResumeSession(context.Background(), target)
				}
				if err != nil {
					errCh <- err
					return
				}
				// Invariant probe from a concurrent reader.
				active := m.Active()
				if !validSlot(active) {
					errCh <- &pointerInvariantError{msg: "active pointer names invalid slot"}
					return
				}
				data, rerr := os.ReadFile(filepath.Join(m.dir, activeFile))
				if rerr != nil {
					errCh <- rerr
					return
				}
				ptr := SlotID(strings.TrimSpace(string(data)))
				if ptr != active {
					errCh <- &pointerInvariantError{msg: "pointer file and in-memory active disagree"}
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent switch error: %v", err)
	}

	// Final invariant: a fresh "process" over the same root opens cleanly.
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("final Persist: %v", err)
	}
	assertInvariantState(t, m.root, m.Active())
}

// pointerInvariantError is a typed invariant violation for error reporting.
type pointerInvariantError struct{ msg string }

func (e *pointerInvariantError) Error() string { return "pointer invariant violated: " + e.msg }

// TestTwoInstancesContendOnCrossProcessLock verifies the flock tier: two
// Manager instances over the same workspace contend on the lockfile. A raw
// flock hold forces the second instance's operation to time out deterministically.
func TestTwoInstancesContendOnCrossProcessLock(t *testing.T) {
	root := t.TempDir()
	m1 := NewManager(root, WithLockConfig(LockConfig{Timeout: 2 * time.Second, Backoff: 5 * time.Millisecond}))
	if err := m1.Open(context.Background()); err != nil {
		t.Fatalf("m1 Open: %v", err)
	}
	defer func() { _ = m1.Close() }()

	m2 := NewManager(root, WithLockConfig(LockConfig{Timeout: 300 * time.Millisecond, Backoff: 5 * time.Millisecond}))
	if err := m2.Open(context.Background()); err != nil {
		t.Fatalf("m2 Open: %v", err)
	}
	defer func() { _ = m2.Close() }()

	// A third party (another "process") takes the flock on the shared lockfile.
	holder, err := os.OpenFile(filepath.Join(m1.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open lockfile: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if err := flockExclusive(holder); err != nil {
		t.Fatalf("holder flock: %v", err)
	}

	start := time.Now()
	err = m2.Persist(context.Background())
	if err == nil {
		t.Fatal("m2.Persist must fail while the cross-process lock is held")
	}
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("m2.Persist error = %v, want ErrLockTimeout", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("timeout fired too early (%v) despite the configured 300ms window", time.Since(start))
	}

	// Release the holder; m2 must now proceed.
	if err := flockRelease(holder); err != nil {
		t.Fatalf("holder release: %v", err)
	}
	if err := m2.Persist(context.Background()); err != nil {
		t.Fatalf("m2.Persist after release: %v", err)
	}
}

// TestAcquireContextCancellation verifies a cancelled context aborts the
// lock wait without leaving the lock held.
func TestAcquireContextCancellation(t *testing.T) {
	root := t.TempDir()
	m1 := NewManager(root, WithLockConfig(LockConfig{Timeout: 5 * time.Second, Backoff: 10 * time.Millisecond}))
	if err := m1.Open(context.Background()); err != nil {
		t.Fatalf("m1 Open: %v", err)
	}
	defer func() { _ = m1.Close() }()

	holder, err := os.OpenFile(filepath.Join(m1.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open lockfile: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if err := flockExclusive(holder); err != nil {
		t.Fatalf("holder flock: %v", err)
	}

	m2 := NewManager(root, WithLockConfig(LockConfig{Timeout: 5 * time.Second, Backoff: 10 * time.Millisecond}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err = m2.Persist(ctx)
	if err == nil {
		t.Fatal("m2.Persist must fail on a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("cancelled acquire took too long: %v", time.Since(start))
	}
	// The mutex must not be left held after the failed acquire.
	if err := flockRelease(holder); err != nil {
		t.Fatalf("holder release: %v", err)
	}
	if err := m2.Persist(context.Background()); err != nil {
		t.Fatalf("m2.Persist after cancel+release: %v (mutex leaked)", err)
	}
}
