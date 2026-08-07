package event

import "sync"

// DefaultBufferSize is the per-subscription channel capacity used when no
// explicit buffer is provided.
const DefaultBufferSize = 256

// EventBus routes events from publishers to observers. Publish never blocks:
// delivery to a slow observer is dropped rather than stalling the publisher.
type EventBus interface {
	Publish(e Event)
	Subscribe(types []EventType, observer Observer) func()
}

// subscription binds a set of event types to an observer. It owns a buffered
// channel and a dispatch goroutine that drains the channel into the observer.
type subscription struct {
	all      bool
	types    map[EventType]struct{}
	observer Observer
	id       uint64
	ch       chan Event
	done     chan struct{}
	once     sync.Once
}

// cancel stops the dispatch goroutine. It is idempotent.
func (s *subscription) cancel() {
	s.once.Do(func() { close(s.done) })
}

// MemoryEventBus is a thread-safe, non-blocking in-memory pub/sub bus.
//
// Each subscription owns a buffered channel drained by a dedicated goroutine,
// so a slow observer never blocks a publisher. When an observer's buffer is
// full the event is dropped rather than stalling the publisher.
//
// The zero value is not usable; construct with NewMemoryEventBus.
type MemoryEventBus struct {
	mu         sync.RWMutex
	bufferSize int
	closed     bool
	nextID     uint64
	subs       map[EventType]map[uint64]*subscription
	allSubs    map[uint64]*subscription
	wg         sync.WaitGroup
}

// NewMemoryEventBus constructs a bus with the given per-subscription buffer
// capacity. A non-positive buffer falls back to DefaultBufferSize.
func NewMemoryEventBus(bufferSize int) *MemoryEventBus {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}
	return &MemoryEventBus{
		bufferSize: bufferSize,
		subs:       make(map[EventType]map[uint64]*subscription),
		allSubs:    make(map[uint64]*subscription),
	}
}

// Subscribe registers observer to receive events of the given types. A nil or
// empty types slice subscribes to every published event. The returned function
// unsubscribes and is idempotent. Subscribing to a closed bus returns a no-op.
func (b *MemoryEventBus) Subscribe(types []EventType, observer Observer) func() {
	if observer == nil {
		return func() {}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return func() {}
	}

	sub := &subscription{
		observer: observer,
		id:       b.nextID,
		ch:       make(chan Event, b.bufferSize),
		done:     make(chan struct{}),
	}
	b.nextID++

	if len(types) == 0 {
		sub.all = true
		b.allSubs[sub.id] = sub
	} else {
		sub.types = make(map[EventType]struct{}, len(types))
		for _, t := range types {
			sub.types[t] = struct{}{}
			if b.subs[t] == nil {
				b.subs[t] = make(map[uint64]*subscription)
			}
			b.subs[t][sub.id] = sub
		}
	}

	b.wg.Add(1)
	go b.dispatchLoop(sub)

	var once sync.Once
	return func() {
		once.Do(func() { b.remove(sub) })
	}
}

// remove cancels the subscription and removes it from every type index.
func (b *MemoryEventBus) remove(sub *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub.cancel()
	if sub.all {
		delete(b.allSubs, sub.id)
		return
	}
	for t := range sub.types {
		if byID := b.subs[t]; byID != nil {
			delete(byID, sub.id)
			if len(byID) == 0 {
				delete(b.subs, t)
			}
		}
	}
}

// Publish delivers a copy of the event to every subscription registered for its
// type (and to every all-event subscription). Delivery is non-blocking: when a
// consumer buffer is full the event is dropped. Publishing to a closed bus is a
// no-op.
func (b *MemoryEventBus) Publish(e Event) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	byType := b.subs[e.Type]
	total := len(b.allSubs)
	if byType != nil {
		total += len(byType)
	}
	subs := make([]*subscription, 0, total)
	for _, sub := range b.allSubs {
		subs = append(subs, sub)
	}
	for _, sub := range byType {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()

	for _, sub := range subs {
		select {
		case sub.ch <- e:
		default:
		}
	}
}

// Close stops the bus: all subscriptions are cancelled and their dispatch
// goroutines are joined. Publish and Subscribe after Close are no-ops. Close is
// idempotent and safe for concurrent use.
func (b *MemoryEventBus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	allSubs := b.allSubs
	b.subs = make(map[EventType]map[uint64]*subscription)
	b.allSubs = make(map[uint64]*subscription)
	b.mu.Unlock()

	for _, byID := range subs {
		for _, sub := range byID {
			sub.cancel()
		}
	}
	for _, sub := range allSubs {
		sub.cancel()
	}
	b.wg.Wait()
}

// dispatchLoop drains a subscription channel into its observer. It exits as
// soon as the subscription is cancelled and re-checks cancellation before each
// observer invocation so events buffered before a cancel are not delivered.
func (b *MemoryEventBus) dispatchLoop(sub *subscription) {
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
			sub.observer(ev)
		}
	}
}
