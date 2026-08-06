package timeline

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/domain/signal"
	"github.com/PizenLabs/izen/internal/events"
)

// env builds an envelope with a deterministic timestamp and ID.
func env(id, source string, kind events.DomainKind, ts time.Time) events.Envelope {
	return events.Envelope{
		ID:        id,
		Timestamp: ts,
		Source:    source,
		Kind:      kind,
		Payload:   id,
	}
}

func TestNewTimeline(t *testing.T) {
	tl := NewTimeline("sess-1")
	if tl == nil {
		t.Fatal("NewTimeline returned nil")
	}
	if tl.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", tl.SessionID)
	}
	if len(tl.Spans) != 0 {
		t.Errorf("Spans = %d, want 0", len(tl.Spans))
	}
}

func TestIngestGroupsBySourceKind(t *testing.T) {
	tl := NewTimeline("sess-1")
	base := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	tl.Ingest(env("a", "execution", events.DomainKindSignal, base))
	tl.Ingest(env("b", "execution", events.DomainKindSignal, base.Add(time.Millisecond)))
	tl.Ingest(env("c", "telemetry", events.DomainKindTelemetry, base.Add(2*time.Millisecond)))
	tl.Ingest(env("d", "execution", events.DomainKindSignal, base.Add(3*time.Millisecond)))

	if got := tl.SpanCount(); got != 2 {
		t.Fatalf("SpanCount = %d, want 2", got)
	}
	snap := tl.Snapshot()
	for _, s := range snap {
		switch s.Source {
		case "execution":
			if len(s.Events) != 3 {
				t.Errorf("execution span events = %d, want 3", len(s.Events))
			}
			if s.Name != "signal" {
				t.Errorf("execution span name = %q, want signal", s.Name)
			}
			if !s.StartTime.Equal(base) || !s.EndTime.Equal(base.Add(3*time.Millisecond)) {
				t.Errorf("execution span window = [%v, %v]", s.StartTime, s.EndTime)
			}
		case "telemetry":
			if len(s.Events) != 1 {
				t.Errorf("telemetry span events = %d, want 1", len(s.Events))
			}
			if s.Name != "telemetry" {
				t.Errorf("telemetry span name = %q, want telemetry", s.Name)
			}
		default:
			t.Errorf("unexpected span source %q", s.Source)
		}
	}
}

func TestIngestSplitsSpanAfterGap(t *testing.T) {
	tl := NewTimeline("sess-1")
	base := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	tl.Ingest(env("a", "execution", events.DomainKindSignal, base))
	// Gap exceeds spanBreak -> closes the first span, opens a fresh one.
	tl.Ingest(env("b", "execution", events.DomainKindSignal, base.Add(spanBreak+time.Second)))

	if got := tl.SpanCount(); got != 2 {
		t.Fatalf("SpanCount = %d, want 2 (gap must split the span)", got)
	}
	snap := tl.Snapshot()
	if len(snap[0].Events) != 1 || len(snap[1].Events) != 1 {
		t.Fatalf("span events = [%d, %d], want [1, 1]", len(snap[0].Events), len(snap[1].Events))
	}
	// A gap under the threshold keeps accumulating into the same open span.
	tl.Ingest(env("c", "execution", events.DomainKindSignal, base.Add(spanBreak+2*time.Second)))
	if got := tl.SpanCount(); got != 2 {
		t.Errorf("SpanCount = %d, want 2 (sub-break gap keeps the span open)", got)
	}
	if len(tl.Snapshot()[1].Events) != 2 {
		t.Errorf("reopened span events = %d, want 2", len(tl.Snapshot()[1].Events))
	}
}

func TestPlaybackStrictTimestampOrder(t *testing.T) {
	tl := NewTimeline("sess-1")
	base := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	// Ingest out of timestamp order across two sources to prove Playback sorts.
	tl.Ingest(env("late", "telemetry", events.DomainKindTelemetry, base.Add(2*time.Second)))
	tl.Ingest(env("early", "execution", events.DomainKindSignal, base))
	tl.Ingest(env("middle", "system", events.DomainKindSystem, base.Add(time.Second)))

	got := tl.Playback()
	if len(got) != 3 {
		t.Fatalf("Playback returned %d envelopes, want 3", len(got))
	}
	wantOrder := []string{"early", "middle", "late"}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("Playback[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestPlaybackDeterministicOnEqualTimestamps(t *testing.T) {
	tl := NewTimeline("sess-1")
	ts := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	// Ingest z-first, a-last; equal timestamps must order by ID (a before z).
	tl.Ingest(env("z", "execution", events.DomainKindSignal, ts))
	tl.Ingest(env("a", "telemetry", events.DomainKindTelemetry, ts))

	got := tl.Playback()
	if len(got) != 2 {
		t.Fatalf("Playback returned %d envelopes, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "z" {
		t.Errorf("Playback order = [%s, %s], want [a, z]", got[0].ID, got[1].ID)
	}
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	tl := NewTimeline("sess-1")
	ts := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	tl.Ingest(env("a", "execution", events.DomainKindSignal, ts))

	snap := tl.Snapshot()
	snap[0].Events[0].Source = "mutated"
	snap[0].Source = "mutated"
	snap[0].Events = append(snap[0].Events, env("b", "execution", events.DomainKindSignal, ts))

	live := tl.Snapshot()
	if len(live[0].Events) != 1 {
		t.Fatalf("snapshot mutation leaked: live span events = %d, want 1", len(live[0].Events))
	}
	if live[0].Source != "execution" {
		t.Errorf("snapshot mutation leaked: source = %q, want execution", live[0].Source)
	}
}

func TestCounts(t *testing.T) {
	tl := NewTimeline("sess-1")
	base := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	tl.Ingest(env("a", "execution", events.DomainKindSignal, base))
	tl.Ingest(env("b", "execution", events.DomainKindSignal, base.Add(time.Millisecond)))
	tl.Ingest(env("c", "telemetry", events.DomainKindTelemetry, base.Add(2*time.Millisecond)))

	if got := tl.EventCount(); got != 3 {
		t.Errorf("EventCount = %d, want 3", got)
	}
	if got := tl.SpanCount(); got != 2 {
		t.Errorf("SpanCount = %d, want 2", got)
	}
}

func TestIngestSkipsZeroTimestamp(t *testing.T) {
	tl := NewTimeline("sess-1")
	tl.Ingest(env("a", "execution", events.DomainKindSignal, time.Time{}))
	if got := tl.EventCount(); got != 0 {
		t.Errorf("EventCount = %d, want 0 (zero-timestamp envelope skipped)", got)
	}
	if got := tl.SpanCount(); got != 0 {
		t.Errorf("SpanCount = %d, want 0", got)
	}
}

func TestNilReceiverSafety(t *testing.T) {
	var tl *Timeline
	if got := tl.Snapshot(); got != nil {
		t.Errorf("Snapshot on nil = %v, want nil", got)
	}
	if got := tl.Playback(); got != nil {
		t.Errorf("Playback on nil = %v, want nil", got)
	}
	if got := tl.SpanCount(); got != 0 {
		t.Errorf("SpanCount on nil = %d, want 0", got)
	}
	if got := tl.EventCount(); got != 0 {
		t.Errorf("EventCount on nil = %d, want 0", got)
	}
	tl.Ingest(events.Envelope{ID: "x", Timestamp: time.Now()})
	if err := tl.Start(nil); err != nil {
		t.Errorf("Start on nil = %v, want nil", err)
	}
	tl.Stop()
}

// fakeSource is a minimal eventSource stub used to test Start/Stop without a
// live bus.
type fakeSource struct {
	handler events.EventHandler
	sub     *events.Subscription
	calls   int32
}

func (f *fakeSource) SubscribeAll(handler events.EventHandler) *events.Subscription {
	f.handler = handler
	atomic.AddInt32(&f.calls, 1)
	return f.sub
}

func TestStartStopSubscription(t *testing.T) {
	src := &fakeSource{sub: &events.Subscription{}}
	tl := NewTimeline("sess-1")

	if err := tl.Start(src); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := atomic.LoadInt32(&src.calls); got != 1 {
		t.Errorf("SubscribeAll calls = %d, want 1", got)
	}
	// Start is idempotent.
	if err := tl.Start(src); err != nil {
		t.Fatalf("Start second time: %v", err)
	}
	if got := atomic.LoadInt32(&src.calls); got != 1 {
		t.Errorf("SubscribeAll calls after idempotent Start = %d, want 1", got)
	}
	tl.Stop()
	// Stop is idempotent.
	tl.Stop()
}

func TestStartFailsWhenSourceClosed(t *testing.T) {
	src := &fakeSource{sub: nil}
	tl := NewTimeline("sess-1")
	if err := tl.Start(src); err == nil {
		t.Fatal("Start with nil subscription should error")
	}
}

func TestHandleFiltersToEnvelopes(t *testing.T) {
	src := &fakeSource{sub: &events.Subscription{}}
	tl := NewTimeline("sess-1")
	if err := tl.Start(src); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tl.Stop()

	// Typed domain events must be ignored by the timeline.
	src.handler(events.NewCommandReceived("/plan x", "plan"))
	if got := tl.EventCount(); got != 0 {
		t.Fatalf("EventCount = %d, want 0 after typed event", got)
	}

	// Envelope events must be ingested.
	src.handler(events.WrapEnvelope(env("a", "execution", events.DomainKindSignal, time.Now().UTC())))
	if got := tl.EventCount(); got != 1 {
		t.Fatalf("EventCount = %d, want 1 after envelope", got)
	}
}

func TestConcurrentIngestAndRead(t *testing.T) {
	tl := NewTimeline("sess-1")
	base := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				tl.Ingest(env(
					"id", "src",
					events.DomainKindSignal,
					base.Add(time.Duration(g*1000+i)*time.Millisecond),
				))
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = tl.Snapshot()
			_ = tl.Playback()
			_ = tl.EventCount()
			_ = tl.SpanCount()
		}
	}()
	wg.Wait()

	if got := tl.EventCount(); got != 1000 {
		t.Errorf("EventCount = %d, want 1000", got)
	}
	if got := tl.Playback(); len(got) != 1000 {
		t.Errorf("Playback length = %d, want 1000", len(got))
	}
}

func TestJSONShape(t *testing.T) {
	tl := NewTimeline("sess-1")
	tl.Ingest(env("a", "execution", events.DomainKindSignal, time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)))

	data, err := json.Marshal(tl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"session_id":"sess-1"`) {
		t.Errorf("marshal missing session_id: %s", data)
	}
	if !strings.Contains(string(data), `"spans"`) {
		t.Errorf("marshal missing spans: %s", data)
	}
}

func TestSignalEnvelopeNameAndType(t *testing.T) {
	s := signal.New(signal.SignalBuildHalted, "execution", nil)
	envv := events.NewSignalEnvelope(s, "execution")
	tl := NewTimeline("sess-1")
	tl.Ingest(envv)

	snap := tl.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("SpanCount = %d, want 1", len(snap))
	}
	if snap[0].Name != "signal" {
		t.Errorf("span name = %q, want signal", snap[0].Name)
	}
	if snap[0].ID != envv.ID {
		t.Errorf("span ID = %q, want envelope ID %q (deterministic)", snap[0].ID, envv.ID)
	}
	if got := snap[0].Events[0].Type(); got != "envelope.signal.build.halted" {
		t.Errorf("envelope Type = %q, want envelope.signal.build.halted", got)
	}
}
