package events

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// DefaultBufferSize is the per-subscription channel capacity used when no
// explicit buffer is provided. It bounds how many events a slow consumer may
// queue before the bus starts dropping.
const DefaultBufferSize = 256

// EventHandler processes a single published domain event. Handlers run on a
// dedicated per-subscription goroutine, so a slow handler can never block the
// publisher.
type EventHandler func(event DomainEvent)

// subscription binds an event type to a handler. It owns a buffered channel
// and a dispatch goroutine that drains the channel into the handler.
type subscription struct {
	eventType  string
	handler    EventHandler
	id         uint64
	handlerKey uintptr
	ch         chan DomainEvent
	done       chan struct{}
	once       sync.Once
	dropped    uint64
}

// cancel stops the dispatch goroutine. It is idempotent.
func (s *subscription) cancel() {
	s.once.Do(func() { close(s.done) })
}

// Subscription is a handle returned by Bus.Subscribe. It can be used to cancel
// delivery precisely, independent of handler identity.
type Subscription struct {
	bus *Bus
	sub *subscription
}

// Cancel unsubscribes this subscription and stops its dispatch goroutine.
// It is safe to call multiple times.
func (s *Subscription) Cancel() {
	if s == nil || s.bus == nil || s.sub == nil {
		return
	}
	s.bus.remove(s.sub.eventType, s.sub.id)
}

// Dropped returns the number of events dropped for this subscription because
// its buffer was full. A non-zero value indicates the consumer is slower than
// the production rate.
func (s *Subscription) Dropped() uint64 {
	if s == nil || s.sub == nil {
		return 0
	}
	return atomic.LoadUint64(&s.sub.dropped)
}

// Bus is a thread-safe, non-blocking in-memory pub/sub event bus.
//
// Publish never blocks on consumers: each subscription owns a buffered channel
// and a goroutine that drains it. When a consumer's buffer is full the event is
// dropped (and counted) instead of stalling the engine that published it. This
// is the hard guarantee that engine execution stays headless under load.
//
// The zero value is not usable; construct with NewBus.
type Bus struct {
	mu         sync.RWMutex
	bufferSize int
	closed     bool
	nextID     uint64
	subs       map[string]map[uint64]*subscription
	wg         sync.WaitGroup
}

// NewBus constructs a bus with the given per-subscription buffer capacity. A
// non-positive buffer falls back to DefaultBufferSize.
func NewBus(bufferSize int) *Bus {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}
	return &Bus{
		bufferSize: bufferSize,
		subs:       make(map[string]map[uint64]*subscription),
	}
}

// Subscribe registers handler to receive every event of the given type. The
// returned Subscription can be used to cancel delivery. A nil handler returns
// nil, and subscribing to a closed bus returns nil.
func (b *Bus) Subscribe(eventType string, handler EventHandler) *Subscription {
	if handler == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}

	sub := &subscription{
		eventType:  eventType,
		handler:    handler,
		id:         b.nextID,
		handlerKey: handlerPointer(handler),
		ch:         make(chan DomainEvent, b.bufferSize),
		done:       make(chan struct{}),
	}
	b.nextID++

	if b.subs[eventType] == nil {
		b.subs[eventType] = make(map[uint64]*subscription)
	}
	b.subs[eventType][sub.id] = sub

	b.wg.Add(1)
	go b.dispatchLoop(sub)

	return &Subscription{bus: b, sub: sub}
}

// Unsubscribe removes every subscription that matches the given event type and
// handler identity. It is a best-effort removal by function pointer; for
// precise per-subscription cancellation prefer the returned *Subscription's
// Cancel method. No-op when the handler is nil.
func (b *Bus) Unsubscribe(eventType string, handler EventHandler) {
	if handler == nil {
		return
	}
	key := handlerPointer(handler)

	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subs[eventType]
	for id, sub := range subs {
		if sub.handlerKey == key {
			sub.cancel()
			delete(subs, id)
		}
	}
	if len(subs) == 0 {
		delete(b.subs, eventType)
	}
}

// remove cancels the subscription with the given event type and id.
func (b *Bus) remove(eventType string, id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.subs[eventType][id]; ok {
		sub.cancel()
		delete(b.subs[eventType], id)
	}
	if len(b.subs[eventType]) == 0 {
		delete(b.subs, eventType)
	}
}

// Publish delivers a copy of the event to every subscription registered for
// its type. Delivery is non-blocking: when a consumer buffer is full the event
// is dropped and counted on that subscription. Publishing to a closed bus is a
// no-op. A nil event is ignored.
func (b *Bus) Publish(ev DomainEvent) {
	if ev == nil {
		return
	}

	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	subs := make([]*subscription, 0, len(b.subs[ev.Type()]))
	for _, sub := range b.subs[ev.Type()] {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()

	for _, sub := range subs {
		select {
		case sub.ch <- ev:
		default:
			atomic.AddUint64(&sub.dropped, 1)
		}
	}
}

// Close stops the bus: all subscriptions are cancelled, dispatch goroutines are
// joined, and subsequent Subscribe calls return nil. Publish after Close is a
// no-op. Close is idempotent and safe for concurrent use.
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = make(map[string]map[uint64]*subscription)
	b.mu.Unlock()

	for _, byType := range subs {
		for _, sub := range byType {
			sub.cancel()
		}
	}
	b.wg.Wait()
}

// dispatchLoop drains a subscription channel into its handler. It exits as soon
// as the subscription is cancelled and re-checks cancellation before each
// handler invocation so events buffered before a cancel are not delivered.
func (b *Bus) dispatchLoop(sub *subscription) {
	defer b.wg.Done()
	for {
		select {
		case <-sub.done:
			return
		case ev := <-sub.ch:
			select {
			case <-sub.done:
				return
			default:
			}
			sub.handler(ev)
		}
	}
}

// handlerPointer returns a comparable identity for a handler function. It uses
// the function's code pointer, which is sufficient to match named/package-level
// handlers and repeated method values; distinct closures created from the same
// literal may share a code pointer.
func handlerPointer(h EventHandler) uintptr {
	return reflect.ValueOf(h).Pointer()
}
