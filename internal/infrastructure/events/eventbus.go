// Package events implements the Infrastructure event publisher (RFC v1.0
// section 2): an in-memory, thread-safe pub/sub bus that asynchronously
// delivers typed events to subscribers.
//
// EventBus is generic over the event type T so the same primitive serves any
// event contract (domain events, telemetry, presentation events). Delivery is
// non-blocking: each subscription owns a buffered channel and a dedicated
// goroutine that drains it into the handler. A slow consumer never stalls the
// publisher; events dropped on a full buffer are counted.
package events

import (
	"sync"
	"sync/atomic"
)

// DefaultBufferSize is the per-subscription channel capacity used when no
// explicit buffer is provided.
const DefaultBufferSize = 256

// subscription binds a handler to a buffered channel and a dispatch goroutine.
type subscription[T any] struct {
	id      uint64
	handler func(T)
	ch      chan T
	done    chan struct{}
	once    sync.Once
	dropped uint64
}

// cancel stops the dispatch goroutine. It is idempotent.
func (s *subscription[T]) cancel() {
	s.once.Do(func() { close(s.done) })
}

// Subscription is a handle returned by EventBus.Subscribe. It cancels delivery
// precisely and reports dropped events for that subscription.
type Subscription[T any] struct {
	bus *EventBus[T]
	sub *subscription[T]
}

// Cancel unsubscribes this subscription and stops its dispatch goroutine. It
// is safe to call multiple times.
func (s *Subscription[T]) Cancel() {
	if s == nil || s.bus == nil || s.sub == nil {
		return
	}
	s.bus.remove(s.sub.id)
}

// Dropped returns the number of events dropped for this subscription because
// its buffer was full. A non-zero value means the consumer lags production.
func (s *Subscription[T]) Dropped() uint64 {
	if s == nil || s.sub == nil {
		return 0
	}
	return atomic.LoadUint64(&s.sub.dropped)
}

// EventBus is a thread-safe, non-blocking in-memory pub/sub bus for events of
// type T.
//
// Publish never blocks on subscribers: when a subscriber's buffer is full the
// event is dropped (and counted) instead of stalling the publisher. The zero
// value is not usable; construct with New.
type EventBus[T any] struct {
	mu     sync.RWMutex
	buffer int
	closed bool
	nextID uint64
	subs   map[uint64]*subscription[T]
	wg     sync.WaitGroup
}

// New constructs a bus with the given per-subscription buffer capacity. A
// non-positive buffer falls back to DefaultBufferSize.
func New[T any](buffer int) *EventBus[T] {
	if buffer <= 0 {
		buffer = DefaultBufferSize
	}
	return &EventBus[T]{
		buffer: buffer,
		subs:   make(map[uint64]*subscription[T]),
	}
}

// Subscribe registers handler to receive every published event of type T. The
// returned Subscription can be used to cancel delivery. A nil handler returns
// nil, and subscribing to a closed bus returns nil.
func (b *EventBus[T]) Subscribe(handler func(T)) *Subscription[T] {
	if handler == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}

	sub := &subscription[T]{
		id:      b.nextID,
		handler: handler,
		ch:      make(chan T, b.buffer),
		done:    make(chan struct{}),
	}
	b.nextID++
	b.subs[sub.id] = sub

	b.wg.Add(1)
	go b.dispatchLoop(sub)

	return &Subscription[T]{bus: b, sub: sub}
}

// Publish delivers a copy of the event to every subscription. Delivery is
// non-blocking: when a consumer buffer is full the event is dropped and
// counted on that subscription. Publishing to a closed bus is a no-op.
func (b *EventBus[T]) Publish(ev T) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	subs := make([]*subscription[T], 0, len(b.subs))
	for _, sub := range b.subs {
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

// Close stops the bus: all subscriptions are cancelled, dispatch goroutines
// are joined, and subsequent Subscribe calls return nil. Publish after Close
// is a no-op. Close is idempotent and safe for concurrent use.
func (b *EventBus[T]) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = make(map[uint64]*subscription[T])
	b.mu.Unlock()

	for _, sub := range subs {
		sub.cancel()
	}
	b.wg.Wait()
}

// remove cancels and detaches the subscription with the given id.
func (b *EventBus[T]) remove(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.subs[id]; ok {
		sub.cancel()
		delete(b.subs, id)
	}
}

// dispatchLoop drains a subscription channel into its handler. It exits as
// soon as the subscription is cancelled and re-checks cancellation before each
// handler invocation so events buffered before a cancel are not delivered.
func (b *EventBus[T]) dispatchLoop(sub *subscription[T]) {
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
