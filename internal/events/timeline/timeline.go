// Package timeline implements the Unified Execution Timeline: the single
// abstraction Metrics, Tracing, and Audit Replay all consume to answer exactly
// one question — "What happened chronologically across the system during this
// session?"
//
// A Timeline consumes an events.Envelope stream and groups envelopes into
// chronological spans by (source, kind). Playback replays every envelope in
// strict timestamp order, so span aggregation and replay are both derived from
// one unified record of the session.
//
// Dependency rule: the package depends on internal/events (and transitively
// internal/domain) only — dependency strictly flows DOWN. It performs no
// policy evaluation, no planning, and no routing.
package timeline

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// spanBreak is the maximum inter-envelope gap within one span. When a new
// envelope of the same source/kind arrives after a longer gap, the current
// span is closed and a fresh one is opened, so spans stay chronologically
// bounded within one contiguous burst of activity.
const spanBreak = 5 * time.Second

// eventSource is the subscription surface the Timeline consumes. The concrete
// infrastructure bus (internal/events.Bus) satisfies it structurally, so the
// timeline stays decoupled from any concrete publisher.
type eventSource interface {
	SubscribeAll(handler events.EventHandler) *events.Subscription
}

// Timeline is the session-scoped execution timeline projection. It is safe
// for concurrent use: the bus delivers envelopes on its own goroutines while
// TUI/Metrics consumers read via Snapshot and Playback.
type Timeline struct {
	mu        sync.RWMutex
	SessionID string `json:"session_id"`
	Spans     []Span `json:"spans"`

	// openByKey maps a source/kind grouping key to the index of its current
	// open span in Spans.
	openByKey map[string]int
	started   bool
	sub       *events.Subscription
}

// NewTimeline constructs an empty session timeline for the given session ID.
func NewTimeline(sessionID string) *Timeline {
	return &Timeline{
		SessionID: sessionID,
		Spans:     make([]Span, 0),
		openByKey: make(map[string]int),
	}
}

// Ingest folds one envelope into the timeline. Envelopes are grouped into
// chronological spans by (source, kind): consecutive envelopes of the same
// group accumulate into one span until a gap larger than spanBreak closes it.
// It is safe for concurrent use.
func (t *Timeline) Ingest(env events.Envelope) {
	if t == nil || env.Timestamp.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openByKey == nil {
		t.openByKey = make(map[string]int)
	}

	key := spanKey(env)
	if idx, ok := t.openByKey[key]; ok && idx < len(t.Spans) {
		sp := &t.Spans[idx]
		if env.Timestamp.Sub(sp.EndTime) > spanBreak {
			t.openSpan(env, key)
			return
		}
		sp.Events = append(sp.Events, env)
		if env.Timestamp.After(sp.EndTime) {
			sp.EndTime = env.Timestamp
		}
		return
	}
	t.openSpan(env, key)
}

// openSpan appends a new span seeded with env and records its index under key.
// The caller must hold the write lock.
func (t *Timeline) openSpan(env events.Envelope, key string) {
	t.Spans = append(t.Spans, newSpan(env))
	t.openByKey[key] = len(t.Spans) - 1
}

// Playback replays every ingested envelope in strict timestamp order. Equal
// timestamps are ordered deterministically by envelope ID so replay is stable
// regardless of ingestion order. It is safe for concurrent use.
func (t *Timeline) Playback() []events.Envelope {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	total := 0
	for i := range t.Spans {
		total += len(t.Spans[i].Events)
	}
	out := make([]events.Envelope, 0, total)
	for i := range t.Spans {
		out = append(out, t.Spans[i].Events...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].ID < out[j].ID
		}
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out
}

// Snapshot returns a deep copy of the current spans for race-free reads.
// It is safe for concurrent use.
func (t *Timeline) Snapshot() []Span {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Span, len(t.Spans))
	for i, s := range t.Spans {
		out[i] = s
		out[i].Events = append([]events.Envelope(nil), s.Events...)
	}
	return out
}

// SpanCount returns the number of aggregated spans. It is safe for concurrent
// use.
func (t *Timeline) SpanCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.Spans)
}

// EventCount returns the total number of ingested envelopes across all spans.
// It is safe for concurrent use.
func (t *Timeline) EventCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	total := 0
	for i := range t.Spans {
		total += len(t.Spans[i].Events)
	}
	return total
}

// Start subscribes the timeline to every event on the bus and begins building
// the live session timeline. It is idempotent and safe for concurrent use. It
// returns an error when the source cannot be subscribed (e.g. it is closed).
func (t *Timeline) Start(source eventSource) error {
	if t == nil || source == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		return nil
	}
	t.sub = source.SubscribeAll(t.handle)
	if t.sub == nil {
		return errors.New("timeline: failed to subscribe to the event bus")
	}
	t.started = true
	return nil
}

// Stop unsubscribes the timeline from the bus, freezing the session timeline
// for replay. It is idempotent and safe for concurrent use.
func (t *Timeline) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return
	}
	if t.sub != nil {
		t.sub.Cancel()
		t.sub = nil
	}
	t.started = false
}

// handle runs on the bus dispatch goroutine. It filters the all-event stream
// down to envelope-carrying events and folds them into the timeline.
func (t *Timeline) handle(ev events.DomainEvent) {
	if ev == nil {
		return
	}
	env, ok := events.EnvelopeFromEvent(ev)
	if !ok {
		return
	}
	t.Ingest(env)
}

// spanKey derives the grouping key: a span belongs to one source/kind pair.
func spanKey(env events.Envelope) string {
	return env.Source + "\x00" + string(env.Kind)
}
