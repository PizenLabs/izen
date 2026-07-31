package events

import (
	"sync"
	"testing"
)

func TestNewActivityPayload(t *testing.T) {
	ev := NewActivity("[ OK ] search: 3 results")
	if ev.Type() != EventActivity {
		t.Errorf("type = %q, want %q", ev.Type(), EventActivity)
	}
	if ev.Timestamp().IsZero() {
		t.Error("timestamp is zero")
	}
	p, ok := ev.Payload().(ActivityPayload)
	if !ok {
		t.Fatalf("payload = %T, want ActivityPayload", ev.Payload())
	}
	if p.Line != "[ OK ] search: 3 results" {
		t.Errorf("Line = %q", p.Line)
	}
}

func TestNewEngineTelemetryPayload(t *testing.T) {
	type fakeEngineEvent struct {
		Hits int
	}
	fe := fakeEngineEvent{Hits: 7}
	ev := NewEngineTelemetry(fe)
	if ev.Type() != EventEngineTelemetry {
		t.Errorf("type = %q, want %q", ev.Type(), EventEngineTelemetry)
	}
	p, ok := ev.Payload().(EngineTelemetryPayload)
	if !ok {
		t.Fatalf("payload = %T, want EngineTelemetryPayload", ev.Payload())
	}
	got, ok := p.Event.(fakeEngineEvent)
	if !ok || got.Hits != 7 {
		t.Errorf("wrapped event = %#v, want fakeEngineEvent{Hits:7}", p.Event)
	}
}

func TestNewEngineTelemetryNilWrapped(t *testing.T) {
	ev := NewEngineTelemetry(nil)
	p := ev.Payload().(EngineTelemetryPayload)
	if p.Event != nil {
		t.Errorf("wrapped event = %v, want nil", p.Event)
	}
}

func TestNewReasoningStreamPayload(t *testing.T) {
	ev := NewReasoningStream("step one", false)
	if ev.Type() != EventReasoningStream {
		t.Errorf("type = %q, want %q", ev.Type(), EventReasoningStream)
	}
	if ev.Timestamp().IsZero() {
		t.Error("timestamp is zero")
	}
	p, ok := ev.Payload().(ReasoningPayload)
	if !ok {
		t.Fatalf("payload = %T, want ReasoningPayload", ev.Payload())
	}
	if p.Chunk != "step one" || p.IsComplete {
		t.Errorf("payload = %+v, want chunk %q incomplete", p, "step one")
	}

	done := NewReasoningStream("", true)
	dp := done.Payload().(ReasoningPayload)
	if dp.Chunk != "" || !dp.IsComplete {
		t.Errorf("done payload = %+v, want empty chunk + complete", dp)
	}
}

func TestSelfHealingEventConstructors(t *testing.T) {
	attempt := NewSelfHealingAttempt(3, "worker.go", "SYNTAX_ERROR")
	if attempt.Type() != EventSelfHealingAttempt {
		t.Errorf("attempt type = %q", attempt.Type())
	}
	ap := attempt.Payload().(SelfHealingAttemptPayload)
	if ap.Retry != 3 || ap.File != "worker.go" || ap.Category != "SYNTAX_ERROR" {
		t.Errorf("attempt payload = %+v", ap)
	}

	exhausted := NewSelfHealingExhausted(3, "worker.go:5: syntax error\n")
	if exhausted.Type() != EventSelfHealingExhausted {
		t.Errorf("exhausted type = %q", exhausted.Type())
	}
	ep := exhausted.Payload().(SelfHealingExhaustedPayload)
	if ep.Attempts != 3 || ep.Output == "" {
		t.Errorf("exhausted payload = %+v", ep)
	}
}

func TestTelemetryEventsRoundTripThroughBus(t *testing.T) {
	bus := NewBus(4)
	defer bus.Close()

	var mu sync.Mutex
	var got []DomainEvent
	bus.Subscribe(EventActivity, collectHandler(t, &mu, &got))
	bus.Subscribe(EventEngineTelemetry, collectHandler(t, &mu, &got))

	bus.Publish(NewActivity("line one"))
	bus.Publish(NewEngineTelemetry("typed payload"))

	if !waitFor(t, func() bool {
		return countEvents(&mu, &got) == 2
	}) {
		t.Fatalf("delivered %d events, want 2", countEvents(&mu, &got))
	}
}

func TestReasoningStreamRoundTripsThroughBus(t *testing.T) {
	bus := NewBus(4)
	defer bus.Close()

	var mu sync.Mutex
	var got []DomainEvent
	bus.Subscribe(EventReasoningStream, collectHandler(t, &mu, &got))

	bus.Publish(NewReasoningStream("first", false))
	bus.Publish(NewReasoningStream("second", false))
	bus.Publish(NewReasoningStream("", true))

	if !waitFor(t, func() bool {
		return countEvents(&mu, &got) == 3
	}) {
		t.Fatalf("delivered %d events, want 3", countEvents(&mu, &got))
	}

	mu.Lock()
	defer mu.Unlock()
	chunks := make([]string, 0, len(got))
	for _, ev := range got {
		p := ev.Payload().(ReasoningPayload)
		chunks = append(chunks, p.Chunk)
		if ev.Type() != EventReasoningStream {
			t.Errorf("type = %q, want %q", ev.Type(), EventReasoningStream)
		}
	}
	joined := ""
	for _, c := range chunks {
		joined += c
	}
	if joined != "firstsecond" {
		t.Errorf("joined chunks = %q, want firstsecond", joined)
	}
}
