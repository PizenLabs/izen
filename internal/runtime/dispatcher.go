package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Sentinel errors returned by the command dispatcher.
var (
	// ErrNilCommand is returned when a nil RuntimeCommand is dispatched.
	ErrNilCommand = errors.New("runtime: nil command")
	// ErrUnhandledCommand is returned when no handler is registered for a
	// command type.
	ErrUnhandledCommand = errors.New("runtime: no handler registered for command")
)

// CommandHandler processes a RuntimeCommand dispatched by the
// CommandDispatcher. Implementations must be safe for concurrent invocation:
// the dispatcher never serializes handler calls.
type CommandHandler interface {
	Handle(ctx context.Context, cmd RuntimeCommand) error
}

// HandlerFunc adapts a plain function to the CommandHandler interface.
type HandlerFunc func(ctx context.Context, cmd RuntimeCommand) error

// Handle implements CommandHandler.
func (f HandlerFunc) Handle(ctx context.Context, cmd RuntimeCommand) error {
	return f(ctx, cmd)
}

// CommandDispatcher routes each RuntimeCommand to the CommandHandler
// registered for its CommandType. It is safe for concurrent use: registration
// and dispatch are guarded by an internal RWMutex.
type CommandDispatcher struct {
	mu       sync.RWMutex
	handlers map[CommandType]CommandHandler
}

// NewCommandDispatcher returns an empty dispatcher with no registered
// handlers.
func NewCommandDispatcher() *CommandDispatcher {
	return &CommandDispatcher{handlers: make(map[CommandType]CommandHandler)}
}

// Register binds a handler to a command type. It returns an error when the
// command type is empty, the handler is nil, or a handler is already
// registered for the type (to prevent silent handler replacement).
func (d *CommandDispatcher) Register(typ CommandType, h CommandHandler) error {
	if typ == "" {
		return errors.New("runtime: empty command type")
	}
	if h == nil {
		return errors.New("runtime: nil handler")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.handlers[typ]; exists {
		return fmt.Errorf("runtime: duplicate handler for command %q", typ)
	}
	d.handlers[typ] = h
	return nil
}

// Dispatch routes cmd to the handler registered for its CommandType. It
// returns ErrUnhandledCommand when no handler is registered. The handler is
// resolved under a read lock and invoked outside of it so concurrent handlers
// never contend with the dispatcher.
func (d *CommandDispatcher) Dispatch(ctx context.Context, cmd RuntimeCommand) error {
	if d == nil {
		return errors.New("runtime: nil dispatcher")
	}
	if cmd == nil {
		return ErrNilCommand
	}
	d.mu.RLock()
	h, ok := d.handlers[cmd.Type()]
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnhandledCommand, cmd.Type())
	}
	return h.Handle(ctx, cmd)
}
