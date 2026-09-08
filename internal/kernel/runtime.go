package kernel

import (
	"context"
	"sync/atomic"

	"github.com/PizenLabs/izen/internal/events"
)

// Runtime is the execution environment handed to a task. It exposes the
// task-scoped context, the shared event bus, and explicit cancellation.
type Runtime interface {
	Context() context.Context
	Emit(ev events.DomainEvent)
	IsCanceled() bool
	Cancel(reason error)
}

// KernelRuntime is the concrete Runtime implementation. It encapsulates the
// task-scoped context, its cancel cause function, and the shared event bus.
type KernelRuntime struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	bus      *events.Bus
	canceled atomic.Bool
}

// NewRuntime builds a KernelRuntime from an existing task-scoped context and
// its cancel cause function. The bus may be nil to disable emission.
func NewRuntime(ctx context.Context, cancel context.CancelCauseFunc, bus *events.Bus) *KernelRuntime {
	return &KernelRuntime{ctx: ctx, cancel: cancel, bus: bus}
}

// Context returns the task-scoped context.
func (r *KernelRuntime) Context() context.Context { return r.ctx }

// Emit publishes an event to the bus. It is a no-op when the runtime has no
// bus.
func (r *KernelRuntime) Emit(ev events.DomainEvent) {
	if r.bus != nil {
		r.bus.Publish(ev)
	}
}

// IsCanceled reports whether Cancel was invoked on this runtime. A context
// deadline or parent cancellation does not set this flag.
func (r *KernelRuntime) IsCanceled() bool {
	return r.canceled.Load()
}

// Cancel marks the runtime canceled and cancels the task-scoped context with
// the given reason. A nil reason is replaced with context.Canceled.
func (r *KernelRuntime) Cancel(reason error) {
	if reason == nil {
		reason = context.Canceled
	}
	r.canceled.Store(true)
	r.cancel(reason)
}
