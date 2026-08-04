package telemetry

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
)

// TimelineOption configures a Timeline at construction time.
type TimelineOption func(*Timeline)

// WithMaxEvents caps the number of events retained per session. When the cap
// is reached the oldest events are evicted so memory stays bounded under long
// sessions. A non-positive value (the default) keeps every event.
func WithMaxEvents(n int) TimelineOption {
	return func(t *Timeline) {
		if n > 0 {
			t.maxEvents = n
		}
	}
}

// Timeline records the ordered telemetry events of a single request session.
// Record is safe for concurrent use; Events and Snapshot return defensive
// copies so readers never observe or corrupt in-flight appends. A Timeline
// doubles as an EventHandler and can be wired directly into an EventBus via
// SubscribeAll, which preserves per-subscription delivery order.
type Timeline struct {
	mu        sync.RWMutex
	sessionID string
	startedAt time.Time
	endedAt   time.Time
	maxEvents int
	events    []Event
}

// NewTimeline returns an empty timeline for the given session id.
func NewTimeline(sessionID string, opts ...TimelineOption) *Timeline {
	t := &Timeline{
		sessionID: sessionID,
		startedAt: time.Now(),
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// SessionID returns the session the timeline belongs to.
func (t *Timeline) SessionID() string { return t.sessionID }

// Record appends an event to the timeline in arrival order. A nil event is
// ignored. Record satisfies EventHandler, so it can be passed directly to
// EventBus.SubscribeAll.
func (t *Timeline) Record(ev Event) {
	if ev == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.maxEvents > 0 && len(t.events) >= t.maxEvents {
		copy(t.events, t.events[1:])
		t.events[len(t.events)-1] = ev
		return
	}
	t.events = append(t.events, ev)
}

// Close finalizes the timeline by stamping the end time. It is idempotent.
func (t *Timeline) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.endedAt.IsZero() {
		t.endedAt = time.Now()
	}
}

// Len returns the number of recorded events.
func (t *Timeline) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.events)
}

// Events returns a defensive copy of the recorded events in arrival order.
func (t *Timeline) Events() []Event {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Event(nil), t.events...)
}

// Snapshot returns the structured, exportable trace of the session.
func (t *Timeline) Snapshot() *Trace {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tr := &Trace{
		SessionID: t.sessionID,
		StartedAt: t.startedAt,
		EndedAt:   t.endedAt,
	}
	if !t.endedAt.IsZero() && t.startedAt.Before(t.endedAt) {
		tr.Duration = t.endedAt.Sub(t.startedAt)
	}
	tr.Events = make([]TraceEvent, 0, len(t.events))
	for i, ev := range t.events {
		tr.Events = append(tr.Events, TraceEvent{
			Index:     i,
			Layer:     ev.Type().Layer(),
			Type:      ev.Type(),
			Timestamp: ev.Timestamp(),
			Duration:  durationOf(ev.Payload()),
			Payload:   ev.Payload(),
		})
	}
	return tr
}

// ExportJSON marshals the session trace as pretty-printed JSON, suitable for
// audit stores and replay tooling.
func (t *Timeline) ExportJSON() ([]byte, error) {
	return json.MarshalIndent(t.Snapshot(), "", "  ")
}

// MarshalJSON implements json.Marshaler so a Timeline serializes as its trace.
func (t *Timeline) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Snapshot())
}

// RenderCompact returns a terminal-friendly, one-line-per-event rendering of
// the session's reconstructed decision path.
func (t *Timeline) RenderCompact() string {
	replay := ReplayTimeline(t.Events())
	var b strings.Builder
	for _, s := range replay.Steps {
		detail := s.Outcome
		if detail == "" {
			detail = s.Decision
		} else {
			detail = s.Decision + " -> " + detail
		}
		fmt.Fprintf(&b, "[%s] %s %s\n", s.Layer, s.Type, detail)
	}
	return b.String()
}

// Trace is the structured, JSON-exportable form of a timeline.
type Trace struct {
	SessionID string        `json:"session_id"`
	StartedAt time.Time     `json:"started_at"`
	EndedAt   time.Time     `json:"ended_at,omitempty"`
	Duration  time.Duration `json:"duration_ns,omitempty"`
	Events    []TraceEvent  `json:"events"`
}

// TraceEvent is one entry in an exported trace.
type TraceEvent struct {
	Index     int           `json:"index"`
	Layer     string        `json:"layer"`
	Type      EventType     `json:"type"`
	Timestamp time.Time     `json:"timestamp"`
	Duration  time.Duration `json:"duration_ns,omitempty"`
	Payload   interface{}   `json:"payload"`
}

// durationOf extracts the stage duration from a telemetry payload, when the
// payload carries one.
func durationOf(p interface{}) time.Duration {
	switch v := p.(type) {
	case *KnowledgeResolvedPayload:
		return v.Duration
	case *CapabilityDetectedPayload:
		return v.Duration
	case *ContextGovernedPayload:
		return v.Duration
	case *PipelineStepPayload:
		return v.Duration
	case *ValidationDAGPayload:
		return v.Duration
	default:
		return 0
	}
}

// Replay reconstructs the decision path of a session's ordered event stream.
type Replay struct {
	StartedAt time.Time     `json:"started_at,omitempty"`
	EndedAt   time.Time     `json:"ended_at,omitempty"`
	Duration  time.Duration `json:"duration_ns,omitempty"`
	Steps     []Step        `json:"steps"`
	Passed    int           `json:"passed"`
	Failed    int           `json:"failed"`
	Decisions int           `json:"decisions"`
}

// Step is one reconstructed decision in a replayed timeline.
type Step struct {
	Index    int               `json:"index"`
	Layer    string            `json:"layer"`
	Type     EventType         `json:"type"`
	Decision string            `json:"decision"`
	Outcome  string            `json:"outcome,omitempty"`
	Metrics  map[string]string `json:"metrics,omitempty"`
	At       time.Time         `json:"at,omitempty"`
	Duration time.Duration     `json:"duration_ns,omitempty"`
}

// ReplayTimeline reconstructs the decision path of an ordered event stream.
// It maps each event to a human- and machine-readable Step and aggregates the
// pass/fail totals for the session. Events must be passed in the order they
// were recorded (as returned by Timeline.Events).
func ReplayTimeline(events []Event) *Replay {
	replay := &Replay{Steps: make([]Step, 0, len(events))}
	var first, last Event
	for i, ev := range events {
		if ev == nil {
			continue
		}
		desc := describe(ev)
		step := Step{
			Index:    i,
			Layer:    ev.Type().Layer(),
			Type:     ev.Type(),
			Decision: desc.decision,
			Outcome:  desc.outcome,
			Metrics:  desc.metrics,
			At:       ev.Timestamp(),
			Duration: durationOf(ev.Payload()),
		}
		replay.Steps = append(replay.Steps, step)
		if first == nil {
			first = ev
		}
		last = ev
		switch desc.sign {
		case signPass:
			replay.Passed++
		case signFail:
			replay.Failed++
		}
	}
	if first != nil && last != nil {
		replay.StartedAt = first.Timestamp()
		replay.EndedAt = last.Timestamp()
		if replay.EndedAt.After(replay.StartedAt) {
			replay.Duration = replay.EndedAt.Sub(replay.StartedAt)
		}
	}
	replay.Decisions = len(replay.Steps)
	return replay
}

// sign classifies whether a step represents a passed, failed or neutral
// outcome for session-level aggregation.
type sign int

const (
	signNeutral sign = iota
	signPass
	signFail
)

// stepDesc is the decision-path projection of one event.
type stepDesc struct {
	decision string
	outcome  string
	metrics  map[string]string
	sign     sign
}

// describe projects one event into its decision-path form.
func describe(ev Event) stepDesc {
	d := stepDesc{decision: string(ev.Type()), sign: signNeutral}
	switch p := ev.Payload().(type) {
	case *KnowledgeResolvedPayload:
		d.decision = "resolved workspace knowledge"
		d.outcome = "primary_manager=" + p.PrimaryManager
		d.metrics = map[string]string{
			"managers":    strconv.Itoa(p.Managers),
			"conventions": strconv.Itoa(p.Conventions),
			"constraints": strconv.Itoa(p.Constraints),
			"conflicts":   strconv.Itoa(p.Conflicts),
		}
		d.sign = signPass
	case *CapabilityDetectedPayload:
		d.decision = "detected workspace capabilities"
		d.outcome = "stack=" + p.Stack
		d.metrics = map[string]string{
			"capabilities": strings.Join(p.Capabilities, ","),
		}
		d.sign = signPass
	case *ContextGovernedPayload:
		d.decision = "assembled governed context"
		d.outcome = "budget_met=" + strconv.FormatBool(p.BudgetMet)
		d.metrics = map[string]string{
			"files":             strconv.Itoa(p.Files),
			"symbols":           strconv.Itoa(p.Symbols),
			"tokens_used":       strconv.Itoa(p.TokensUsed),
			"token_budget":      strconv.Itoa(p.TokenBudget),
			"compression_ratio": strconv.FormatFloat(p.CompressionRatio, 'g', -1, 64),
		}
		if p.BudgetMet {
			d.sign = signPass
		} else {
			d.sign = signFail
		}
	case *PipelineStepPayload:
		d.decision = "pipeline step " + p.Stage
		d.metrics = map[string]string{
			"intent":   p.Intent,
			"strategy": p.Strategy,
			"patches":  strconv.Itoa(p.Patches),
			"tokens":   strconv.Itoa(p.Tokens),
		}
		if layer3.State(p.State) == layer3.StateFailed || layer3.State(p.State) == layer3.StateCancelled {
			d.outcome = p.State
			d.sign = signFail
			if p.Err != "" {
				d.metrics["error"] = p.Err
			}
		} else {
			d.outcome = "done"
			d.sign = signPass
		}
	case *ValidationDAGPayload:
		d.decision = "ran validation DAG"
		d.metrics = map[string]string{
			"nodes_total":   strconv.Itoa(p.NodesTotal),
			"nodes_passed":  strconv.Itoa(p.NodesPassed),
			"nodes_failed":  strconv.Itoa(p.NodesFailed),
			"nodes_skipped": strconv.Itoa(p.NodesSkipped),
		}
		if p.ShortCircuited {
			d.metrics["short_circuited"] = "true"
		}
		if p.OK {
			d.outcome = "passed"
			d.sign = signPass
		} else {
			d.outcome = "failed"
			d.sign = signFail
			if p.Err != "" {
				d.metrics["error"] = p.Err
			}
		}
	}
	return d
}
