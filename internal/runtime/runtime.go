package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/PizenLabs/izen/internal/events"
)

// Runtime is the single entry point of the system (RFC v1.0 section 1). It is
// an extremely thin facade: it only routes RuntimeCommands to the
// CommandDispatcher and publishes lifecycle telemetry to the event bus. All
// workflow logic lives downstream in the command handlers and the domain
// layer.
//
// In addition to routing commands, the Runtime owns the Presentation-event
// projection: it subscribes to the domain event bus through an EventTranslator
// and fans the translated, UI-ready PresentationEvents out to every registered
// presentation listener. This keeps the presentation layer decoupled from the
// domain vocabulary while still receiving every state change it renders.
type Runtime struct {
	dispatcher *CommandDispatcher
	bus        *events.Bus
	translator *EventTranslator

	presMu    sync.Mutex
	presSubs  []presSub
	presNext  int
	transSubs []*events.Subscription
	started   bool
}

// presSub binds a presentation listener to a stable subscription id.
type presSub struct {
	id      int
	handler func(PresentationEvent)
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

// WithEventTranslator overrides the default EventTranslator used for the
// Presentation-event projection.
func WithEventTranslator(t *EventTranslator) Option {
	return func(r *Runtime) {
		if t != nil {
			r.translator = t
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

// SubscribePresentation registers handler to receive every translated
// PresentationEvent. It returns a cancel func that stops delivery. The
// translation projection is started lazily on the first subscription, so
// wiring order in the composition root never matters.
func (r *Runtime) SubscribePresentation(handler func(PresentationEvent)) func() {
	if r == nil || handler == nil {
		return func() {}
	}
	r.presMu.Lock()
	r.presNext++
	id := r.presNext
	r.presSubs = append(r.presSubs, presSub{id: id, handler: handler})
	r.presMu.Unlock()

	r.Start()

	return func() {
		r.presMu.Lock()
		defer r.presMu.Unlock()
		for i, s := range r.presSubs {
			if s.id == id {
				r.presSubs = append(r.presSubs[:i], r.presSubs[i+1:]...)
				return
			}
		}
	}
}

// Start subscribes the EventTranslator to the domain event bus so domain
// events are translated into PresentationEvents and fanned out to registered
// listeners. It is idempotent and safe for concurrent use.
func (r *Runtime) Start() {
	if r == nil {
		return
	}
	r.presMu.Lock()
	defer r.presMu.Unlock()
	if r.started || r.bus == nil {
		return
	}
	if r.translator == nil {
		r.translator = &EventTranslator{}
	}
	trans := r.translator
	bus := r.bus
	for _, typ := range TranslatedEventTypes() {
		if sub := bus.Subscribe(typ, func(ev events.DomainEvent) {
			out, ok := trans.Translate(ev)
			if !ok {
				return
			}
			r.presMu.Lock()
			subs := make([]presSub, len(r.presSubs))
			copy(subs, r.presSubs)
			r.presMu.Unlock()
			for _, s := range subs {
				s.handler(out)
			}
		}); sub != nil {
			r.transSubs = append(r.transSubs, sub)
		}
	}
	r.started = true
}

// Close cancels the translation projection and detaches every presentation
// listener. It is idempotent.
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.presMu.Lock()
	defer r.presMu.Unlock()
	for _, sub := range r.transSubs {
		sub.Cancel()
	}
	r.transSubs = nil
	r.presSubs = nil
	r.started = false
}

// targetMode extracts the optional workflow mode carried by a command, falling
// back to an empty string for commands without one.
func targetMode(cmd RuntimeCommand) string {
	if mc, ok := cmd.(modeCarrier); ok {
		return mc.TargetMode()
	}
	return ""
}
