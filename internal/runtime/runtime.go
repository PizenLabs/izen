package runtime

import (
	"context"
	"errors"

	"github.com/PizenLabs/izen/internal/events"
)

// Runtime is the single entry point of the system (RFC v1.0 section 1). It is
// an extremely thin facade: it only routes RuntimeCommands to the
// CommandDispatcher and publishes lifecycle telemetry to the event bus. All
// workflow logic lives downstream in the command handlers and the domain
// layer.
type Runtime struct {
	dispatcher *CommandDispatcher
	bus        *events.Bus
}

// Option configures a Runtime at construction time.
type Option func(*Runtime)

// WithEventBus wires the event bus so command lifecycle events
// (CommandReceived) are published on every Execute. A nil bus is ignored and
// disables emission.
func WithEventBus(bus *events.Bus) Option {
	return func(r *Runtime) {
		if bus != nil {
			r.bus = bus
		}
	}
}

// NewRuntime initializes a Runtime bound to the given CommandDispatcher. The
// dispatcher must be fully registered before use; NewRuntime does not install
// any default handlers (wiring lives in the composition root).
func NewRuntime(dispatcher *CommandDispatcher, opts ...Option) *Runtime {
	r := &Runtime{dispatcher: dispatcher}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Dispatcher returns the underlying CommandDispatcher, or nil when the runtime
// is uninitialized. It is read-only instrumentation for the composition root.
func (r *Runtime) Dispatcher() *CommandDispatcher {
	if r == nil {
		return nil
	}
	return r.dispatcher
}

// Execute routes cmd to its registered handler. It returns ErrNilCommand for a
// nil command, ErrUnhandledCommand when no handler is registered for the
// command type, and the handler error otherwise.
//
// When an event bus is wired, a CommandReceived event is published before the
// command is dispatched so projections observe the command entering the
// system.
func (r *Runtime) Execute(ctx context.Context, cmd RuntimeCommand) error {
	if r == nil || r.dispatcher == nil {
		return errors.New("runtime: not initialized")
	}
	if cmd == nil {
		return ErrNilCommand
	}
	if r.bus != nil {
		r.bus.Publish(events.NewCommandReceived(cmd.Type().String(), targetMode(cmd)))
	}
	return r.dispatcher.Dispatch(ctx, cmd)
}

// targetMode extracts the optional workflow mode carried by a command, falling
// back to an empty string for commands without one.
func targetMode(cmd RuntimeCommand) string {
	if mc, ok := cmd.(modeCarrier); ok {
		return mc.TargetMode()
	}
	return ""
}
