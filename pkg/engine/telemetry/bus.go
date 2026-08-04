package telemetry

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// DefaultBufferSize is the per-subscription channel capacity used when no
// explicit buffer is provided. It bounds how many events a slow consumer may
// queue before the bus starts dropping.
const DefaultBufferSize = 256

// EventHandler processes a single published telemetry event. Handlers run on
// a dedicated per-subscription goroutine, so a slow handler can never block
// the publisher.
type EventHandler func(ev Event)

// subscription binds one or more event types to a handler. It owns a buffered
// channel and a worker goroutine that drains the channel into the handler.
type subscription struct {
	all        bool
	eventType  EventType
	handler    EventHandler
	id         uint64
	handlerKey uintptr
	ch         chan Event
	done       chan struct{}
	once       sync.Once
	dropped    atomic.Uint64
}

// cancel stops the dispatch goroutine. It is idempotent.
func (s *subscription) cancel() {
	s.once.Do(func() { close(s.done) })
}

// Subscription is a handle returned by EventBus.Subscribe. It can be used to
// cancel delivery precisely, independent of handler identity.
type Subscription struct {
	bus *EventBus
	sub *subscription
}

// Cancel unsubscribes this subscription and stops its dispatch goroutine.
// It is safe to call multiple times.
func (s *Subscription) Cancel() {
	if s == nil || s.bus == nil || s.sub == nil {
		return
	}
	s.bus.remove(s.sub)
}

// Dropped returns the number of events dropped for this subscription because
// its buffer was full. A non-zero value indicates the consumer is slower than
// the production rate.
func (s *Subscription) Dropped() uint64 {
	if s == nil || s.sub == nil {
		return 0
	}
	return s.sub.dropped.Load()
}

// EventBus is a thread-safe, non-blocking in-memory pub/sub bus for telemetry
// events.
//
// Publish never blocks on consumers: each subscription owns a buffered channel
// and a worker goroutine that drains it. When a consumer's buffer is full the
// event is dropped (and counted) instead of stalling the engine layer that
// published it. This is the hard guarantee that telemetry observes the
// execution pipelines without ever blocking them.
//
// The zero value is not usable; construct with NewEventBus.
type EventBus struct {
	mu         sync.RWMutex
	bufferSize int
	closed     bool
	nextID     uint64
	topicSubs  map[EventType]map[uint64]*subscription
	allSubs    map[uint64]*subscription
	wg         sync.WaitGroup
}

// NewEventBus constructs a bus with the given per-subscription buffer
// capacity. A non-positive buffer falls back to DefaultBufferSize.
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}
	return &EventBus{
		bufferSize: bufferSize,
		topicSubs:  make(map[EventType]map[uint64]*subscription),
		allSubs:    make(map[uint64]*subscription),
	}
}

// Subscribe registers handler to receive every event of the given type. The
// returned Subscription can be used to cancel delivery. A nil handler returns
// nil, and subscribing to a closed bus returns nil.
func (b *EventBus) Subscribe(t EventType, handler EventHandler) *Subscription {
	return b.subscribe(t, false, handler)
}

// SubscribeAll registers handler to receive every published event regardless
// of type. It is the natural wiring for audit loggers, replay timelines and
// terminal UI projections.
func (b *EventBus) SubscribeAll(handler EventHandler) *Subscription {
	return b.subscribe(EventType(""), true, handler)
}

func (b *EventBus) subscribe(t EventType, all bool, handler EventHandler) *Subscription {
	if handler == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}

	sub := &subscription{
		all:        all,
		eventType:  t,
		handler:    handler,
		id:         b.nextID,
		handlerKey: handlerPointer(handler),
		ch:         make(chan Event, b.bufferSize),
		done:       make(chan struct{}),
	}
	b.nextID++

	if all {
		b.allSubs[sub.id] = sub
	} else {
		if b.topicSubs[t] == nil {
			b.topicSubs[t] = make(map[uint64]*subscription)
		}
		b.topicSubs[t][sub.id] = sub
	}

	b.wg.Add(1)
	go b.dispatchLoop(sub)

	return &Subscription{bus: b, sub: sub}
}

// Unsubscribe removes every subscription matching the given event type and
// handler identity. It is a best-effort removal by function pointer; for
// precise per-subscription cancellation prefer the returned *Subscription's
// Cancel method. No-op when the handler is nil.
func (b *EventBus) Unsubscribe(t EventType, handler EventHandler) {
	b.unsubscribe(t, false, handler)
}

// UnsubscribeAll removes every all-event subscription matching the handler.
func (b *EventBus) UnsubscribeAll(handler EventHandler) {
	b.unsubscribe(EventType(""), true, handler)
}

func (b *EventBus) unsubscribe(t EventType, all bool, handler EventHandler) {
	if handler == nil {
		return
	}
	key := handlerPointer(handler)

	b.mu.Lock()
	defer b.mu.Unlock()
	if all {
		for id, sub := range b.allSubs {
			if sub.handlerKey == key {
				sub.cancel()
				delete(b.allSubs, id)
			}
		}
		return
	}
	subs := b.topicSubs[t]
	for id, sub := range subs {
		if sub.handlerKey == key {
			sub.cancel()
			delete(subs, id)
		}
	}
	if len(subs) == 0 {
		delete(b.topicSubs, t)
	}
}

// remove cancels and detaches one subscription.
func (b *EventBus) remove(sub *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub.all {
		delete(b.allSubs, sub.id)
	} else {
		subs := b.topicSubs[sub.eventType]
		delete(subs, sub.id)
		if len(subs) == 0 {
			delete(b.topicSubs, sub.eventType)
		}
	}
	sub.cancel()
}

// Publish delivers a copy of the event to every matching subscription.
// Delivery is non-blocking: when a consumer buffer is full the event is
// dropped and counted on that subscription. Publishing to a closed bus is a
// no-op. A nil event is ignored.
func (b *EventBus) Publish(ev Event) {
	if ev == nil {
		return
	}

	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	targets := make([]*subscription, 0, len(b.allSubs)+len(b.topicSubs[ev.Type()]))
	for _, sub := range b.allSubs {
		targets = append(targets, sub)
	}
	for _, sub := range b.topicSubs[ev.Type()] {
		targets = append(targets, sub)
	}
	b.mu.RUnlock()

	for _, sub := range targets {
		select {
		case sub.ch <- ev:
		default:
			sub.dropped.Add(1)
		}
	}
}

// Close stops the bus: all subscriptions are cancelled, dispatch goroutines
// are joined, and subsequent Subscribe calls return nil. Publish after Close
// is a no-op. Close is idempotent and safe for concurrent use.
func (b *EventBus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	topicSubs := b.topicSubs
	allSubs := b.allSubs
	b.topicSubs = make(map[EventType]map[uint64]*subscription)
	b.allSubs = make(map[uint64]*subscription)
	b.mu.Unlock()

	for _, subs := range topicSubs {
		for _, sub := range subs {
			sub.cancel()
		}
	}
	for _, sub := range allSubs {
		sub.cancel()
	}
	b.wg.Wait()
}

// dispatchLoop drains a subscription channel into its handler. It exits as
// soon as the subscription is cancelled and re-checks cancellation before each
// handler invocation so events buffered before a cancel are not delivered.
func (b *EventBus) dispatchLoop(sub *subscription) {
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
