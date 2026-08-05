package telemetry

import (
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
)

func collectEnvelopes(t *testing.T, bus *events.Bus, typ string, want int, timeout time.Duration) []events.Envelope {
	t.Helper()
	got := make(chan events.Envelope, 64)
	sub := bus.Subscribe(typ, func(ev events.DomainEvent) {
		env, ok := events.EnvelopeFromEvent(ev)
		if !ok {
			t.Fatalf("received non-envelope: %T", ev)
		}
		got <- env
	})
	defer sub.Cancel()

	var out []events.Envelope
	deadline := time.After(timeout)
	for len(out) < want {
		select {
		case env := <-got:
			out = append(out, env)
		case <-deadline:
			t.Fatalf("timed out: got %d/%d envelopes", len(out), want)
		}
	}
	return out
}

func TestTelemetryAdapter_BridgesAllLayerEvents(t *testing.T) {
	tel := NewEventBus(64)
	domain := events.NewBus(64)
	defer tel.Close()
	defer domain.Close()

	adapter := NewTelemetryAdapter(tel, domain, "telemetry.adapter")
	if err := adapter.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer adapter.Stop()

	// Publish one event from every Layer 0-4 source plus a control-loop event.
	tel.Publish(NewKnowledgeResolved(nil, time.Millisecond))                                                                          // layer0
	tel.Publish(NewCapabilityDetected(nil, time.Millisecond))                                                                         // layer1
	tel.Publish(NewContextGoverned(layer2.ContextRequest{}, nil, time.Millisecond))                                                   // layer2
	tel.Publish(NewPipelineStepDone("run1", layer3.Intent("fix"), layer3.Route(0), layer3.Stage("build"), 0, 1, 2, time.Millisecond)) // layer3
	tel.Publish(NewValidationDAG(nil, time.Millisecond))                                                                              // layer4
	tel.Publish(NewControlIteration("run1", nil, nil))                                                                                // control

	const want = 6
	envs := collectEnvelopes(t, domain, "envelope.telemetry", want, 3*time.Second)

	types := map[string]bool{}
	for _, env := range envs {
		if env.Kind != events.DomainKindTelemetry {
			t.Fatalf("Kind = %q, want telemetry", env.Kind)
		}
		if env.Source != "telemetry.adapter" {
			t.Fatalf("Source = %q", env.Source)
		}
		if env.ID == "" || env.Timestamp.IsZero() {
			t.Fatalf("envelope missing identity fields: %+v", env)
		}
		if ev, ok := env.Payload.(Event); !ok {
			t.Fatalf("payload is not a telemetry Event: %T", env.Payload)
		} else {
			types[string(ev.Type())] = true
		}
	}

	// All six distinct telemetry event types must have crossed the bridge.
	for _, et := range []string{
		string(EventKnowledgeResolved),
		string(EventCapabilityDetected),
		string(EventContextGoverned),
		string(EventPipelineStep),
		string(EventValidationDAG),
		string(EventControlIteration),
	} {
		if !types[et] {
			t.Fatalf("telemetry event %q not bridged; got %v", et, types)
		}
	}
}

func TestTelemetryAdapter_IdempotentStartStop(t *testing.T) {
	tel := NewEventBus(64)
	domain := events.NewBus(64)
	defer tel.Close()
	defer domain.Close()

	adapter := NewTelemetryAdapter(tel, domain, "telemetry.adapter")
	if err := adapter.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatalf("second Start should be a no-op, got %v", err)
	}
	adapter.Stop()
	adapter.Stop() // double-stop safe

	// Restart works.
	if err := adapter.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	adapter.Stop()
}

func TestTelemetryAdapter_NilBuses(t *testing.T) {
	if a := NewTelemetryAdapter(nil, nil, "src"); a.Start() != nil {
		t.Fatal("nil bus Start should return nil")
	}
	domain := events.NewBus(64)
	defer domain.Close()
	if a := NewTelemetryAdapter(nil, domain, "src"); a.Start() != nil {
		t.Fatal("nil telemetry bus Start should return nil")
	}
	tel := NewEventBus(64)
	defer tel.Close()
	if a := NewTelemetryAdapter(tel, nil, "src"); a.Start() != nil {
		t.Fatal("nil domain bus Start should return nil")
	}
}

func TestTelemetryAdapter_NonBlockingUnderSlowConsumer(t *testing.T) {
	tel := NewEventBus(1)
	domain := events.NewBus(1)
	defer tel.Close()
	defer domain.Close()

	adapter := NewTelemetryAdapter(tel, domain, "telemetry.adapter")
	if err := adapter.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer adapter.Stop()

	// A domain-bus consumer that never drains forces buffer-full drops on the
	// domain side; this must not stall the telemetry producer through the
	// adapter.
	sub := domain.Subscribe("envelope.telemetry", func(ev events.DomainEvent) {
		time.Sleep(10 * time.Millisecond)
	})
	defer sub.Cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			tel.Publish(NewKnowledgeResolved(nil, time.Millisecond))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("telemetry producer stalled behind a slow domain-bus consumer")
	}
}
