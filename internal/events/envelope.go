package events

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/PizenLabs/izen/internal/domain/signal"
)

// DomainKind is the coarse classification of an Envelope's payload. It answers
// "what class of thing crossed the boundary" without exposing the concrete
// payload type to the bus.
type DomainKind string

const (
	// DomainKindSignal wraps a canonical internal/domain/signal.Signal.
	DomainKindSignal DomainKind = "signal"
	// DomainKindTelemetry wraps a telemetry event from the layered pipeline
	// engine (pkg/engine/telemetry), bridged onto the unified bus by the
	// TelemetryAdapter.
	DomainKindTelemetry DomainKind = "telemetry"
	// DomainKindSystem wraps system-level payloads that are not domain events
	// (e.g. process lifecycle notices).
	DomainKindSystem DomainKind = "system"
)

// envelopeTypePrefix is the bus type-discriminator prefix for all Envelope
// values so projections can distinguish them from typed DomainEvents.
const envelopeTypePrefix = "envelope."

// Envelope is the standardized transport wrapper for events that cross
// engine/package boundaries (e.g. telemetry bridged from pkg/engine/telemetry).
// It is a value struct with a stable JSON shape: consumers may persist,
// replay, or forward envelopes without knowing the concrete payload type.
type Envelope struct {
	ID        string     `json:"id"`
	Timestamp time.Time  `json:"timestamp"`
	Source    string     `json:"source"`
	Kind      DomainKind `json:"kind"`
	Payload   any        `json:"payload"`
	// SessionID is the originating session correlation (INV-SESSION-10). It is
	// stamped by the AuditLogger from the active session at persistence time so
	// every line of the NDJSON audit log maps to the session that produced it.
	// Empty when no session authority is wired (harness/headless).
	SessionID string `json:"session_id,omitempty"`
}

// Type derives the granular bus discriminator for the envelope:
//
//	envelope.signal.dep.missing
//	envelope.signal.import.mismatch
//	envelope.telemetry
//	envelope.system
//
// Signal envelopes derive the discriminator from the wrapped SignalKind so
// projections can subscribe per signal kind.
func (e Envelope) Type() string {
	if e.Kind == DomainKindSignal {
		if s, ok := e.Payload.(signal.Signal); ok {
			return envelopeTypePrefix + string(DomainKindSignal) + "." + string(s.Kind)
		}
	}
	return envelopeTypePrefix + string(e.Kind)
}

// NewEnvelope constructs an Envelope with a unique ID and a UTC timestamp.
// Payload may be any value; the Kind must describe it.
func NewEnvelope(kind DomainKind, source string, payload any) Envelope {
	return Envelope{
		ID:        newEnvelopeID(),
		Timestamp: time.Now().UTC(),
		Source:    source,
		Kind:      kind,
		Payload:   payload,
	}
}

// NewSignalEnvelope wraps a canonical signal into a DomainKindSignal envelope
// with a granular envelope.signal.<kind> type discriminator.
func NewSignalEnvelope(s signal.Signal, source string) Envelope {
	return NewEnvelope(DomainKindSignal, source, s)
}

// envelopeEvent adapts an Envelope to the DomainEvent interface for bus
// transport. Envelope itself keeps the plain value-struct shape from the
// envelope spec (ID/Timestamp/Source/Kind/Payload) and therefore cannot also
// declare a Timestamp() method next to its Timestamp field; the wrapper
// resolves the collision while keeping routing on the derived type
// discriminator.
type envelopeEvent struct {
	env Envelope
}

func (e envelopeEvent) Type() string         { return e.env.Type() }
func (e envelopeEvent) Timestamp() time.Time { return e.env.Timestamp }
func (e envelopeEvent) Payload() interface{} { return e.env.Payload }

// WrapEnvelope adapts an Envelope into a DomainEvent for bus transport. Use it
// with Publish, or PublishEnvelope for the convenience form.
func WrapEnvelope(env Envelope) DomainEvent {
	return envelopeEvent{env: env}
}

// EnvelopeFromEvent extracts the Envelope carried by a bus-delivered envelope
// event. ok is false when ev is not an envelope (e.g. it is a typed domain
// event).
func EnvelopeFromEvent(ev DomainEvent) (Envelope, bool) {
	ee, ok := ev.(envelopeEvent)
	if !ok {
		return Envelope{}, false
	}
	return ee.env, true
}

// NewEnvelopeID returns a fresh unique envelope identifier (16 hex chars). It
// is the exported form of the internal generator so downstream projects
// (e.g. the audit logger) can mint stable envelope ids when wrapping typed
// domain events.
func NewEnvelopeID() string { return newEnvelopeID() }

// newEnvelopeID returns a random 16-hex-character identifier. On the
// practically-impossible crypto/rand failure it falls back to a
// timestamp-derived id so uniqueness is preserved.
func newEnvelopeID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
