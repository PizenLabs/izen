package loop

import (
	"context"
	"errors"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/preflight"
)

// PreflightSyncBarrier gates the state-machine transition from observing to
// deciding/executing until BackgroundPreflight completes and publishes its
// StructuralSnapshot to Observation State. It is the execution invariant that
// async discovery never means unverified execution.
//
// Context timeout for the barrier: 10s (if preflight hangs, abort with
// PREFLIGHT_TIMEOUT).
const BarrierTimeout = 10 * time.Second

// ErrPreflightTimeout is returned when the barrier times out.
var ErrPreflightTimeout = preflight.ErrPreflightTimeout

// Barrier wraps the execution preflight barrier for the loop. It is the
// boundary between observing and deciding. Wait blocks until the background
// worker signals completion or the timeout expires.
type Barrier struct {
	inner *preflight.PreflightSyncBarrier
	bus   *events.Bus
}

// NewBarrier returns a barrier with the spec's 10s timeout.
func NewBarrier(bus *events.Bus) *Barrier {
	return &Barrier{
		inner: preflight.NewBarrier(),
		bus:   bus,
	}
}

// NewBarrierWithTimeout is the test seam.
func NewBarrierWithTimeout(d time.Duration, bus *events.Bus) *Barrier {
	return &Barrier{
		inner: preflight.NewBarrierWithTimeout(d),
		bus:   bus,
	}
}

// Notify forwards the snapshot/error to the inner barrier.
func (b *Barrier) Notify(snap *preflight.StructuralSnapshot, err error) {
	if b == nil || b.inner == nil {
		return
	}
	b.inner.Notify(snap, err)
}

// Wait blocks at the observing→deciding boundary. It logs the barrier wait
// via activity events (never stdout) and respects the 10s PREFLIGHT_TIMEOUT.
// On preflight failure it returns the error so the caller can halt to
// awaiting_human / error.
func (b *Barrier) Wait(ctx context.Context) (*preflight.StructuralSnapshot, error) {
	if b == nil || b.inner == nil {
		return nil, nil
	}
	if b.bus != nil {
		b.bus.Publish(events.NewActivity("[barrier] waiting preflight"))
	}
	// Enforce spec timeout if caller has no deadline.
	waitCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		waitCtx, cancel = context.WithTimeout(ctx, BarrierTimeout)
		defer cancel()
	}
	snap, err := b.inner.Wait(waitCtx)
	if errors.Is(err, preflight.ErrPreflightTimeout) || errors.Is(err, context.DeadlineExceeded) {
		if b.bus != nil {
			b.bus.Publish(events.NewExecutionFailed(events.FailurePermanent, ErrPreflightTimeout, "preflight"))
		}
		return nil, ErrPreflightTimeout
	}
	if err != nil {
		// Unrecoverable preflight failure: halt pipeline gracefully.
		if b.bus != nil {
			b.bus.Publish(events.NewPreflightFailed("", "preflight failed at barrier", err))
			b.bus.Publish(events.NewExecutionFailed(events.FailurePermanent, err, "preflight"))
		}
		return snap, err
	}
	if b.bus != nil {
		b.bus.Publish(events.NewActivity("[loop] observing -> deciding"))
	}
	return snap, nil
}

// Inner returns the underlying preflight barrier (for worker wiring).
func (b *Barrier) Inner() *preflight.PreflightSyncBarrier {
	if b == nil {
		return nil
	}
	return b.inner
}

// Done reports whether the barrier has been notified.
func (b *Barrier) Done() bool {
	if b == nil || b.inner == nil {
		return false
	}
	return b.inner.Done()
}
