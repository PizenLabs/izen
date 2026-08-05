package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/domain/signal"
)

func TestEnvelope_TypeDiscriminator(t *testing.T) {
	cases := []struct {
		name string
		env  Envelope
		want string
	}{
		{"signal dep.missing", NewSignalEnvelope(signal.New(signal.SignalDepMissing, "inv", nil), "inv"), "envelope.signal.dep.missing"},
		{"signal import.mismatch", NewSignalEnvelope(signal.New(signal.SignalImportMismatch, "inv", nil), "inv"), "envelope.signal.import.mismatch"},
		{"telemetry", NewEnvelope(DomainKindTelemetry, "telemetry", "raw"), "envelope.telemetry"},
		{"system", NewEnvelope(DomainKindSystem, "system", "raw"), "envelope.system"},
		{"signal non-signal payload", NewEnvelope(DomainKindSignal, "x", "not a signal"), "envelope.signal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.env.Type(); got != c.want {
				t.Fatalf("Type() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNewEnvelope_Fields(t *testing.T) {
	env := NewEnvelope(DomainKindTelemetry, "telemetry.adapter", "payload")
	if env.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if env.Source != "telemetry.adapter" {
		t.Fatalf("Source = %q", env.Source)
	}
	if env.Timestamp.IsZero() {
		t.Fatal("expected timestamp")
	}
	// UTC-normalized.
	if _, offset := env.Timestamp.Zone(); offset != 0 {
		t.Fatalf("expected UTC timestamp, got offset %d", offset)
	}
	if env.Kind != DomainKindTelemetry {
		t.Fatalf("Kind = %q", env.Kind)
	}
	if env.Payload != "payload" {
		t.Fatalf("Payload = %v", env.Payload)
	}
}

func TestEnvelope_UniqueIDs(t *testing.T) {
	a := NewEnvelope(DomainKindTelemetry, "s", nil)
	b := NewEnvelope(DomainKindTelemetry, "s", nil)
	if a.ID == b.ID {
		t.Fatalf("expected unique IDs, both %q", a.ID)
	}
}

func TestEnvelope_JSONShape(t *testing.T) {
	env := NewEnvelope(DomainKindTelemetry, "src", map[string]string{"stage": "layer0"})
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != env.ID || back.Source != env.Source || back.Kind != env.Kind {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if back.Timestamp.Unix() != env.Timestamp.Unix() {
		t.Fatalf("timestamp round-trip mismatch")
	}
}

func TestWrapAndUnwrap(t *testing.T) {
	env := NewSignalEnvelope(signal.New(signal.SignalSymbolUndefined, "inv", map[string]string{"symbol": "Log"}), "inv")
	ev := WrapEnvelope(env)
	if ev.Type() != "envelope.signal.symbol.undefined" {
		t.Fatalf("type = %q", ev.Type())
	}
	if ev.Timestamp().Unix() != env.Timestamp.Unix() {
		t.Fatal("timestamp mismatch")
	}
	back, ok := EnvelopeFromEvent(ev)
	if !ok {
		t.Fatal("expected envelope extraction to succeed")
	}
	if back.ID != env.ID {
		t.Fatalf("extraction ID mismatch: %q != %q", back.ID, env.ID)
	}
	if s, ok2 := back.Payload.(signal.Signal); !ok2 || s.Kind != signal.SignalSymbolUndefined {
		t.Fatalf("extraction payload mismatch: %+v", back.Payload)
	}

	// Non-envelope domain events extract to ok=false.
	if _, ok := EnvelopeFromEvent(NewCommandReceived("x", "plan")); ok {
		t.Fatal("expected typed event to extract as not-an-envelope")
	}
}

func TestBus_PublishEnvelopeRoutesToDerivedType(t *testing.T) {
	bus := NewBus(64)
	got := make(chan Envelope, 16)
	sub := bus.Subscribe("envelope.signal.dep.missing", func(ev DomainEvent) {
		env, ok := EnvelopeFromEvent(ev)
		if !ok {
			t.Fatalf("subscriber received non-envelope: %T", ev)
		}
		got <- env
	})
	defer sub.Cancel()

	sig := signal.New(signal.SignalDepMissing, "inv", nil)
	bus.PublishEnvelope(NewSignalEnvelope(sig, "inv"))

	select {
	case env := <-got:
		if env.Kind != DomainKindSignal {
			t.Fatalf("Kind = %q", env.Kind)
		}
		if env.Payload.(signal.Signal).Kind != signal.SignalDepMissing {
			t.Fatalf("wrapped signal kind mismatch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for envelope delivery")
	}
}

func TestBus_PublishEnvelopeDoesNotRouteToTypedSubscribers(t *testing.T) {
	bus := NewBus(64)
	count := 0
	sub := bus.Subscribe(EventCommandReceived, func(ev DomainEvent) {
		count++
	})
	defer sub.Cancel()

	bus.PublishEnvelope(NewEnvelope(DomainKindTelemetry, "telemetry", nil))
	// Typed subscribers must not receive envelopes.
	time.Sleep(50 * time.Millisecond)
	if count != 0 {
		t.Fatalf("typed subscriber received %d envelope events", count)
	}
}

func TestBus_PublishEnvelopeNonBlocking(t *testing.T) {
	bus := NewBus(1)
	sub := bus.Subscribe("envelope.telemetry", func(ev DomainEvent) {
		// Never drain: forces buffer-full drops.
		time.Sleep(10 * time.Millisecond)
	})
	defer sub.Cancel()

	// Rapid-fire publishes must never block even with a full buffer.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			bus.PublishEnvelope(NewEnvelope(DomainKindTelemetry, "telemetry", i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishEnvelope blocked with a full consumer buffer")
	}
}
