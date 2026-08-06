package timeline

import (
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// Span is a chronological grouping of envelopes that share a source and kind.
// It answers "what happened, in order, within one source/kind stream" and is
// the unit of aggregation Metrics and Tracing consume.
type Span struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parent_id,omitempty"`
	Name      string            `json:"name"`
	Source    string            `json:"source"`
	StartTime time.Time         `json:"start_time"`
	EndTime   time.Time         `json:"end_time"`
	Events    []events.Envelope `json:"events,omitempty"`
}

// spanName derives a human-readable label from an envelope kind.
func spanName(kind events.DomainKind) string {
	switch kind {
	case events.DomainKindSignal:
		return "signal"
	case events.DomainKindTelemetry:
		return "telemetry"
	case events.DomainKindSystem:
		return "system"
	default:
		return string(kind)
	}
}

// newSpan opens a span seeded with the first envelope of a source/kind group.
// The span ID derives from the first envelope's unique ID so a span is
// deterministic across replays.
func newSpan(env events.Envelope) Span {
	return Span{
		ID:        env.ID,
		Name:      spanName(env.Kind),
		Source:    env.Source,
		StartTime: env.Timestamp,
		EndTime:   env.Timestamp,
		Events:    []events.Envelope{env},
	}
}
