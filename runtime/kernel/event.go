package kernel

import (
	"encoding/json"
	"fmt"
	"sync"
)

// EventKind is the closed vocabulary of transitions the reducer accepts.
//
// The rule the vocabulary enforces is blunt: an event may only exist for a fact
// that already happened. There is no "about to", no "should", and no "hoping to"
// kind, so a projection reading the log can never display progress that the
// runtime has not actually made.
type EventKind string

const (
	// EventExecutionStarted: the spec was admitted and the program began.
	EventExecutionStarted EventKind = "execution.started"
	// EventStepStarted: a step's capability was about to be invoked.
	EventStepStarted EventKind = "step.started"
	// EventCapabilityInvoked: a capability was invoked and returned.
	EventCapabilityInvoked EventKind = "capability.invoked"
	// EventProviderStarted: a reasoning or process backend invocation began.
	EventProviderStarted EventKind = "provider.started"
	// EventProviderFinished: that invocation returned, with its axis value.
	EventProviderFinished EventKind = "provider.finished"
	// EventEvidenceProduced: one or more observations entered the evidence log.
	EventEvidenceProduced EventKind = "evidence.produced"
	// EventArtifactProduced: the parser extracted at least one valid artifact.
	EventArtifactProduced EventKind = "artifact.produced"
	// EventMutationApplied: the filesystem boundary was durably crossed.
	EventMutationApplied EventKind = "mutation.applied"
	// EventMutationRolledBack: the filesystem boundary was undone.
	EventMutationRolledBack EventKind = "mutation.rolled_back"
	// EventVerificationStarted: a verification check began.
	EventVerificationStarted EventKind = "verification.started"
	// EventVerificationPassed: that check returned a satisfied verdict.
	EventVerificationPassed EventKind = "verification.passed"
	// EventVerificationFailed: that check returned an unsatisfied verdict.
	EventVerificationFailed EventKind = "verification.failed"
	// EventVerificationSkipped: that check was provably not applicable.
	EventVerificationSkipped EventKind = "verification.skipped"
	// EventStepBlocked: a step was refused. Authorization, missing capability,
	// capability failure and budget exhaustion all land here, distinguished by
	// the event's Block.
	EventStepBlocked EventKind = "step.blocked"
	// EventExecutionSuspended: the execution stopped at a bound and may
	// continue from its recorded cursor.
	EventExecutionSuspended EventKind = "execution.suspended"
	// EventExecutionResumed: a suspended execution continued from its cursor.
	EventExecutionResumed EventKind = "execution.resumed"
	// EventExecutionFailed: the execution stopped with a failure class.
	EventExecutionFailed EventKind = "execution.failed"
	// EventExecutionFinished: the execution reached terminal truth. Emitted
	// only after the reducer has adjudicated, never before.
	EventExecutionFinished EventKind = "execution.finished"
)

// allEventKinds is the canonical ordered vocabulary.
var allEventKinds = []EventKind{
	EventExecutionStarted,
	EventStepStarted,
	EventCapabilityInvoked,
	EventProviderStarted,
	EventProviderFinished,
	EventEvidenceProduced,
	EventArtifactProduced,
	EventMutationApplied,
	EventMutationRolledBack,
	EventVerificationStarted,
	EventVerificationPassed,
	EventVerificationFailed,
	EventVerificationSkipped,
	EventStepBlocked,
	EventExecutionSuspended,
	EventExecutionResumed,
	EventExecutionFailed,
	EventExecutionFinished,
}

// AllEventKinds returns the canonical ordered event vocabulary.
func AllEventKinds() []EventKind {
	out := make([]EventKind, len(allEventKinds))
	copy(out, allEventKinds)
	return out
}

// Event is one durable record of something that happened.
//
// Events are the kernel's write-ahead log. State is derived from them, so the
// log is the durable artifact and the state is a projection — which means an
// execution's history can be reconstructed, audited, and replayed without
// trusting any in-memory structure that happened to be alive at the time.
type Event struct {
	// Seq is the position of this event in the log, starting at 1.
	Seq uint64
	// Revision is the state revision this event produced.
	Revision uint64
	// Kind is the closed-vocabulary classification.
	Kind EventKind
	// ExecutionID is the execution this event belongs to.
	ExecutionID string
	// Step is the step the event concerns, when it concerns one.
	Step string
	// Capability is the capability the event concerns.
	Capability CapabilityID
	// Target is the concrete path the event concerns.
	Target string
	// Axis carries the axis value for provider, artifact, mutation and
	// verification events. It is a string rather than a union because the log
	// is serialized and read by consumers that must not import every axis type
	// to understand a log line.
	Axis string
	// Block carries the attributable stop for step.blocked and execution.failed.
	Block *Block
	// Evidence carries the observation records for evidence.produced.
	Evidence []Evidence
	// Terminal carries the adjudicated verdict for execution.finished.
	Terminal *Terminal
	// Detail is an optional human-facing line. No transition reads it.
	Detail string
}

// String renders the event for a log line.
func (e Event) String() string {
	out := fmt.Sprintf("seq=%d rev=%d %s", e.Seq, e.Revision, e.Kind)
	if e.Step != "" {
		out += " step=" + e.Step
	}
	if e.Capability != "" {
		out += " capability=" + string(e.Capability)
	}
	if e.Target != "" {
		out += " target=" + e.Target
	}
	if e.Axis != "" {
		out += " axis=" + e.Axis
	}
	if e.Block != nil {
		out += " block={" + e.Block.Error() + "}"
	}
	if e.Terminal != nil {
		out += " outcome=" + string(e.Terminal.Outcome)
	}
	return out
}

// JSON renders the event as a single NDJSON record. The encoding is stable: the
// same event always produces the same bytes, which is what makes the log
// diffable and a digest over it meaningful.
func (e Event) JSON() ([]byte, error) {
	type wire struct {
		Seq        uint64     `json:"seq"`
		Revision   uint64     `json:"revision"`
		Kind       string     `json:"kind"`
		Execution  string     `json:"execution_id"`
		Step       string     `json:"step,omitempty"`
		Capability string     `json:"capability,omitempty"`
		Target     string     `json:"target,omitempty"`
		Axis       string     `json:"axis,omitempty"`
		Block      *Block     `json:"block,omitempty"`
		Evidence   []Evidence `json:"evidence,omitempty"`
		Terminal   *Terminal  `json:"terminal,omitempty"`
		Detail     string     `json:"detail,omitempty"`
	}
	return json.Marshal(wire{
		Seq:        e.Seq,
		Revision:   e.Revision,
		Kind:       string(e.Kind),
		Execution:  e.ExecutionID,
		Step:       e.Step,
		Capability: string(e.Capability),
		Target:     e.Target,
		Axis:       e.Axis,
		Block:      e.Block,
		Evidence:   e.Evidence,
		Terminal:   e.Terminal,
		Detail:     e.Detail,
	})
}

// EventLog is the durable, append-only record of an execution's transitions.
//
// It is the kernel's write-ahead log and the single place a consumer reads
// runtime truth from. It is safe for concurrent use because a consumer reading
// from a UI goroutine while the engine appends is the normal case, not an edge
// case.
type EventLog struct {
	mu        sync.RWMutex
	events    []Event
	nextSeq   uint64
	nextSubID uint64
	subs      []subscription
	closed    bool
}

// subscription is one consumer plus the identity token that makes it removable.
// Go does not permit comparing function values, so removal keys on the token.
type subscription struct {
	id uint64
	fn func(Event)
}

// NewEventLog builds an empty log.
func NewEventLog() *EventLog {
	return &EventLog{nextSeq: 1}
}

// Append records an event, assigning its sequence and returning the stored copy.
// Sequence assignment happens under the same lock as the append, so a log has
// exactly one order even under concurrent producers.
func (l *EventLog) Append(e Event) Event {
	if l == nil {
		return e
	}
	l.mu.Lock()
	e.Seq = l.nextSeq
	l.nextSeq++
	l.events = append(l.events, e)
	subs := append([]subscription(nil), l.subs...)
	closed := l.closed
	l.mu.Unlock()

	// Subscribers are notified outside the lock so a slow or reentrant consumer
	// cannot stall the engine. A panicking subscriber is contained: the kernel's
	// execution truth must not depend on a projection's robustness.
	if !closed {
		for _, sub := range subs {
			if sub.fn == nil {
				continue
			}
			notifySafely(sub.fn, e)
		}
	}
	return e
}

func notifySafely(fn func(Event), e Event) {
	defer func() { _ = recover() }()
	fn(e)
}

// Subscribe registers a consumer. The returned function unsubscribes, so a
// consumer that goes away — a closed UI, a finished headless run — does not leak
// into the log.
//
// Subscribers are held by identity token rather than by comparing function
// values, which Go does not permit and which would make removal silently wrong.
func (l *EventLog) Subscribe(fn func(Event)) (unsubscribe func()) {
	if l == nil || fn == nil {
		return func() {}
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return func() {}
	}
	l.nextSubID++
	id := l.nextSubID
	l.subs = append(l.subs, subscription{id: id, fn: fn})
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { l.removeSub(id) })
	}
}

func (l *EventLog) removeSub(id uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.subs[:0]
	for _, s := range l.subs {
		if s.id != id {
			kept = append(kept, s)
		}
	}
	l.subs = kept
}

// Events returns a copy of the log in sequence order.
func (l *EventLog) Events() []Event {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

// Len reports how many events the log holds.
func (l *EventLog) Len() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.events)
}

// LastRevision reports the revision of the most recent event, or 0 for an empty
// log. A consumer uses it to detect that it has missed a transition rather than
// silently continuing from a state that never existed.
func (l *EventLog) LastRevision() uint64 {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.events) == 0 {
		return 0
	}
	return l.events[len(l.events)-1].Revision
}

// OfKinds returns the log's events filtered to the given kinds, in sequence
// order. It is how a consumer answers "what actually happened" without holding
// the state.
func (l *EventLog) OfKinds(kinds ...EventKind) []Event {
	if l == nil {
		return nil
	}
	want := make(map[EventKind]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	var out []Event
	for _, e := range l.Events() {
		if want[e.Kind] {
			out = append(out, e)
		}
	}
	return out
}

// Close stops accepting subscribers. Appends after Close still record, because
// losing the tail of a log is exactly the failure mode a durable log exists to
// prevent.
func (l *EventLog) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.subs = nil
}
