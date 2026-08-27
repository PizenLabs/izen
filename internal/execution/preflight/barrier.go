package preflight

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrPreflightTimeout is returned when the barrier times out waiting for
// BackgroundPreflight.
var ErrPreflightTimeout = errors.New("PREFLIGHT_TIMEOUT")

// PreflightSyncBarrier gates the state-machine transition from observing to
// deciding/executing until BackgroundPreflight completes and publishes its
// StructuralSnapshot to Observation State. It is the execution invariant that
// async discovery never means unverified execution.
//
// Barrier is safe for concurrent use. Wait blocks until Notify or context
// cancellation / timeout.
type PreflightSyncBarrier struct {
	mu       sync.Mutex
	done     chan struct{}
	snapshot *StructuralSnapshot
	err      error
	notified bool
	// timeout is the per-wait ceiling (default 10s per spec).
	timeout time.Duration
}

// NewBarrier returns a barrier with the spec's 10s timeout.
func NewBarrier() *PreflightSyncBarrier {
	return &PreflightSyncBarrier{
		done:    make(chan struct{}),
		timeout: 10 * time.Second,
	}
}

// NewBarrierWithTimeout returns a barrier with a custom timeout (tests).
func NewBarrierWithTimeout(d time.Duration) *PreflightSyncBarrier {
	if d <= 0 {
		d = 10 * time.Second
	}
	return &PreflightSyncBarrier{
		done:    make(chan struct{}),
		timeout: d,
	}
}

// Notify signals completion. The first call wins; subsequent calls are no-ops.
// It publishes the snapshot and wakes all Waiters.
func (b *PreflightSyncBarrier) Notify(snap *StructuralSnapshot, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.notified {
		return
	}
	b.snapshot = snap
	b.err = err
	b.notified = true
	close(b.done)
}

// Wait blocks until Notify or until ctx expires / barrier timeout. On success
// it returns the snapshot; on preflight failure it returns the preflight error;
// on timeout it returns ErrPreflightTimeout.
func (b *PreflightSyncBarrier) Wait(ctx context.Context) (*StructuralSnapshot, error) {
	// Derive a timeout context from the barrier's ceiling when caller has no deadline.
	waitCtx := ctx
	var cancel context.CancelFunc
	if b.timeout > 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			waitCtx, cancel = context.WithTimeout(ctx, b.timeout)
			defer cancel()
		} else {
			// Caller already has a deadline; enforce barrier timeout as well
			// by racing both.
			tctx, tcancel := context.WithTimeout(ctx, b.timeout)
			defer tcancel()
			waitCtx = tctx
		}
	}
	select {
	case <-b.done:
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.err != nil {
			return b.snapshot, b.err
		}
		return b.snapshot, nil
	case <-waitCtx.Done():
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrPreflightTimeout
		}
		return nil, waitCtx.Err()
	}
}

// Done reports whether the barrier has been notified.
func (b *PreflightSyncBarrier) Done() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.notified
}

// Reset clears the barrier for reuse (tests only).
func (b *PreflightSyncBarrier) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.done = make(chan struct{})
	b.snapshot = nil
	b.err = nil
	b.notified = false
}
