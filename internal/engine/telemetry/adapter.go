package telemetry

import (
	"sync/atomic"

	"github.com/PizenLabs/izen/internal/events"
)

// TelemetryAdapter bridges the legacy telemetry EventBus (Layer 0-5 pipeline
// telemetry, audit/replay timelines, strategy optimization) onto the unified
// internal/events.Bus as standardized events.Envelope values.
//
// Migration strategy — Adapter First, Delete Later: the legacy EventBus is NOT
// removed. The adapter subscribes to it (SubscribeAll) and translates every
// telemetry event into an Envelope published on the internal/events.Bus, so
// new projections (UI, logging, control plane) consume one unified event
// stream while the legacy telemetry consumers (Timeline, StrategyOptimizer,
// tests) keep working unchanged.
//
// The bridge is non-blocking end to end: telemetry Publish never blocks on its
// per-subscription buffers, and the domain bus Publish is likewise
// non-blocking, so a slow UI consumer can never stall a Layer 0-5 pipeline.
// Drop accounting happens per-subscription on the domain bus (see the
// domain-bus Subscription.Dropped counter).
type TelemetryAdapter struct {
	telemetryBus *EventBus
	domainBus    *events.Bus
	source       string

	sub   *Subscription
	start atomic.Bool
}

// NewTelemetryAdapter creates an adapter that forwards every event published
// on telemetryBus to domainBus as an events.Envelope with the given source
// label. A nil telemetryBus or nil domainBus disables forwarding (the adapter
// becomes a no-op).
func NewTelemetryAdapter(telemetryBus *EventBus, domainBus *events.Bus, source string) *TelemetryAdapter {
	return &TelemetryAdapter{
		telemetryBus: telemetryBus,
		domainBus:    domainBus,
		source:       source,
	}
}

// Start subscribes the adapter to every telemetry event. It is idempotent: a
// second Start while already running is a no-op. It returns nil (the adapter
// never fails to start; forwarding is best-effort by design).
func (a *TelemetryAdapter) Start() error {
	if a == nil || a.telemetryBus == nil || a.domainBus == nil {
		return nil
	}
	if !a.start.CompareAndSwap(false, true) {
		return nil
	}
	a.sub = a.telemetryBus.SubscribeAll(a.handle)
	if a.sub == nil {
		a.start.Store(false)
	}
	return nil
}

// Stop unsubscribes the adapter. Safe to call when never started or already
// stopped.
func (a *TelemetryAdapter) Stop() {
	if a == nil {
		return
	}
	if a.sub != nil {
		a.sub.Cancel()
		a.sub = nil
	}
	a.start.Store(false)
}

// Source returns the source label attached to every forwarded envelope.
func (a *TelemetryAdapter) Source() string {
	if a == nil {
		return ""
	}
	return a.source
}

// handle forwards a single telemetry event onto the unified domain bus. It
// runs on the telemetry bus's dispatch goroutine; the domain bus Publish is
// non-blocking, so this can never stall the pipeline that produced the event.
func (a *TelemetryAdapter) handle(ev Event) {
	if a == nil || a.domainBus == nil || ev == nil {
		return
	}
	a.domainBus.PublishEnvelope(events.NewEnvelope(events.DomainKindTelemetry, a.source, ev))
}
