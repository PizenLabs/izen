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

// EventPriority partitions delivery into the two bus classes.
type EventPriority int

const (
	// PriorityTelemetry covers high-frequency, drop-tolerant events: tool
	// output chunks, metrics, status updates, progress TUI ticks. Delivery is
	// non-blocking over a bounded channel; saturation drops and counts.
	PriorityTelemetry EventPriority = iota
	// PriorityControl covers state-machine control events with GUARANTEED
	// delivery: task lifecycle terminals, clarification requests and state
	// checkpoints. Control events never traverse the select/default drop
	// path — losing one would let the state machine drift silently.
	PriorityControl
)

// IsControlEventType reports whether an event type discriminator belongs to
// the guaranteed-delivery control class. Every other type is drop-tolerant
// telemetry. Control types are the canonical task lifecycle and checkpoint
// events (internal/events/events.go); telemetry is everything else.
func IsControlEventType(eventType string) bool {
	switch eventType {
	case EventTaskStarted, EventTaskCompleted, EventTaskFailed, EventTaskCanceled,
		EventClarificationRequired, EventStateCheckpoint:
		return true
	}
	return false
}

// EventHandler processes a single published domain event. Handlers run on a
// dedicated per-subscription goroutine, so a slow handler can never block the
// publisher.
type EventHandler func(event DomainEvent)

// subscription binds an event type to a handler. Telemetry rides a bounded
// channel (drop-tolerant); control events ride an explicit unbounded FIFO
// drained with priority by a single dispatch goroutine — guaranteed delivery
// that never blocks the publisher.
type subscription struct {
	eventType   string
	handler     EventHandler
	id          uint64
	handlerKey  uintptr
	ch          chan DomainEvent // telemetry: bounded, non-blocking delivery
	controlMu   sync.Mutex       // guards the unbounded control queue
	controlQ    []DomainEvent    // control: unbounded FIFO, guaranteed delivery
	controlHead int              // queue front; compacted on drain
	controlWake chan bool        // 1-slot nudge: control queue needs service
	done        chan struct{}
	once        sync.Once
	dropped     uint64
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
// Publish never blocks on consumers for telemetry: each subscription owns a
// bounded channel and a goroutine that drains it. When a consumer's buffer is
// full the event is dropped (and counted) instead of stalling the engine that
// published it. This is the hard guarantee that engine execution stays
// headless under load.
//
// Control events (task.* lifecycle terminals, clarification.required,
// state.checkpoint) are partitioned onto a per-subscription control channel
// drained with priority and delivered via blocking dispatch: the publisher
// waits for queue space instead of dropping, so subscribers MUST receive
// every control event while their subscription is live. A control event can
// only be abandoned at shutdown, when the subscription is cancelled.
//
// The zero value is not usable; construct with NewBus.
type Bus struct {
	mu         sync.RWMutex
	bufferSize int
	closed     bool
	nextID     uint64
	subs       map[string]map[uint64]*subscription
	allSubs    map[uint64]*subscription
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
		allSubs:    make(map[uint64]*subscription),
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
		eventType:   eventType,
		handler:     handler,
		id:          b.nextID,
		handlerKey:  handlerPointer(handler),
		ch:          make(chan DomainEvent, b.bufferSize),
		controlQ:    make([]DomainEvent, 0, 4),
		controlWake: make(chan bool, 1),
		done:        make(chan struct{}),
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

// SubscribeAll registers handler to receive every published event regardless
// of type. It is the natural wiring for audit loggers, session replay and
// terminal UI projections that must observe the whole stream. Delivery and
// cancellation semantics are identical to Subscribe.
func (b *Bus) SubscribeAll(handler EventHandler) *Subscription {
	if handler == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}

	sub := &subscription{
		eventType:   "",
		handler:     handler,
		id:          b.nextID,
		handlerKey:  handlerPointer(handler),
		ch:          make(chan DomainEvent, b.bufferSize),
		controlQ:    make([]DomainEvent, 0, 4),
		controlWake: make(chan bool, 1),
		done:        make(chan struct{}),
	}
	b.nextID++

	b.allSubs[sub.id] = sub

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
	if eventType == "" {
		if sub, ok := b.allSubs[id]; ok {
			sub.cancel()
			delete(b.allSubs, id)
		}
		return
	}
	if sub, ok := b.subs[eventType][id]; ok {
		sub.cancel()
		delete(b.subs[eventType], id)
	}
	if len(b.subs[eventType]) == 0 {
		delete(b.subs, eventType)
	}
}

// Publish delivers a copy of the event to every subscription registered for
// its type (and to every all-event subscription). Delivery is partitioned by
// event priority: telemetry is non-blocking (a full consumer buffer drops the
// event and counts it); control events use blocking dispatch on the
// per-subscription control channel — the publisher waits for queue space, so
// a subscriber ALWAYS receives every control event while its subscription is
// live. Control delivery is abandoned only when the subscription is cancelled
// (bus shutdown), never through the drop path. Publishing to a closed bus is
// a no-op. A nil event is ignored.
func (b *Bus) Publish(ev DomainEvent) {
	if ev == nil {
		return
	}
	isControl := IsControlEventType(ev.Type())

	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	subs := make([]*subscription, 0, len(b.allSubs)+len(b.subs[ev.Type()]))
	for _, sub := range b.allSubs {
		subs = append(subs, sub)
	}
	for _, sub := range b.subs[ev.Type()] {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()

	for _, sub := range subs {
		if isControl {
			// Guaranteed delivery: every control event is appended to the
			// subscription's unbounded control queue — no capacity bound, no
			// select/default drop path, no publisher backpressure. A control
			// event can only be abandoned at shutdown, when the subscription
			// is cancelled.
			sub.enqueueControl(ev)
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			atomic.AddUint64(&sub.dropped, 1)
		}
	}
}

// PublishEnvelope delivers an Envelope to every subscription registered for its
// derived type discriminator (e.g. "envelope.signal.dep.missing" or
// "envelope.telemetry"). It routes through the same non-blocking delivery as
// Publish, so a slow consumer drops rather than stalls the publisher.
func (b *Bus) PublishEnvelope(env Envelope) {
	b.Publish(WrapEnvelope(env))
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
	allSubs := b.allSubs
	b.subs = make(map[string]map[uint64]*subscription)
	b.allSubs = make(map[uint64]*subscription)
	b.mu.Unlock()

	for _, byType := range subs {
		for _, sub := range byType {
			sub.cancel()
		}
	}
	for _, sub := range allSubs {
		sub.cancel()
	}
	b.wg.Wait()
}

// enqueueControl appends one control event to the subscription's unbounded
// control queue and nudges the dispatch goroutine. It never blocks and never
// drops; the nudge is a 1-slot hint whose loss is harmless (a pending nudge or
// the drain loop itself covers the queue).
func (s *subscription) enqueueControl(ev DomainEvent) {
	s.controlMu.Lock()
	s.controlQ = append(s.controlQ, ev)
	s.controlMu.Unlock()
	select {
	case s.controlWake <- true:
	default:
	}
}

// popControl removes the oldest queued control event, or nil when the queue is
// drained (and compacted).
func (s *subscription) popControl() DomainEvent {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.controlHead >= len(s.controlQ) {
		s.controlQ = nil
		s.controlHead = 0
		return nil
	}
	ev := s.controlQ[s.controlHead]
	s.controlHead++
	return ev
}

// drainControl delivers every queued control event, re-checking cancellation
// before each handler invocation so buffered events are not delivered after a
// cancel. Returns when the queue is empty or the subscription was cancelled
// (the caller's loop then exits on the closed done channel).
func drainControl(sub *subscription) {
	for ev := sub.popControl(); ev != nil; ev = sub.popControl() {
		select {
		case <-sub.done:
			return
		default:
		}
		sub.handler(ev)
	}
}

// dispatchLoop drains a subscription's queues into its handler, control first:
// every wake path empties the unbounded control queue BEFORE the next
// telemetry event is handled, so a state-machine checkpoint is dispatched even
// when hundreds of telemetry items are queued ahead of it. Control delivery is
// guaranteed while the subscription is live. The loop exits as soon as the
// subscription is cancelled.
func (b *Bus) dispatchLoop(sub *subscription) {
	defer b.wg.Done()
	for {
		select {
		case <-sub.done:
			return
		case <-sub.controlWake:
			drainControl(sub)
		default:
			// Control queue quiet — service the bounded telemetry queue, but
			// keep the control wake in this select so a control event that
			// lands while we wait is never invisible to the blocked goroutine.
			select {
			case <-sub.done:
				return
			case <-sub.controlWake:
				drainControl(sub)
			case ev := <-sub.ch:
				select {
				case <-sub.done:
					return
				case <-sub.controlWake:
					drainControl(sub)
				default:
				}
				sub.handler(ev)
			}
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
