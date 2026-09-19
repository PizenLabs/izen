package scheduler

import (
	"sync"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ── Phase 6.4.4 Always-Flush Telemetry & Live Token Accounting ────────────
//
// TelemetryBinder binds the UI token counter (↑X ↓Y) directly to
// TaskState.TokenUsage updates emitted by the TelemetryBus (events.Bus).
// Every provider.usage_update / stream usage event commits immediately to
// the durable task state — on success, failure, or timeout — so a prompt
// that times out at 15s still reflects its consumed prompt and partial
// completion tokens, and a cancelled request never shows stale counts from
// a previous command (the live turn mirrors reset per turn, the durable
// totals only ever accumulate via CommitUsage).
//
// The binder is thread-safe: provider streams publish from worker
// goroutines while the scheduler mutates task state on the orchestration
// goroutine. All commits serialize on the binder mutex, and the TaskState
// pointer itself is never swapped — only its TokenUsage totals advance, so
// JSON persistence in snapshot.json stays consistent.

// TelemetryBinder subscribes a durable TaskState to live usage events.
type TelemetryBinder struct {
	mu    sync.Mutex
	state *durable.TaskState

	livePrompt     int
	liveCompletion int
	liveEstimated  bool
}

// NewTelemetryBinder binds state to future usage commits. state may be nil
// (all commits become no-ops) so headless/CLI fallbacks stay untouched.
func NewTelemetryBinder(state *durable.TaskState) *TelemetryBinder {
	return &TelemetryBinder{state: state}
}

// CommitLiveUsage records one live usage delta into the turn mirrors and
// always-flushes it to TaskState.TokenUsage. It is the single accounting
// entry point for streaming turns: prompt estimates registered at dispatch,
// real-time completion increments per chunk, and terminal authoritative
// counts all converge here.
func (b *TelemetryBinder) CommitLiveUsage(prompt, completion int, estimated bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if prompt < 0 {
		prompt = 0
	}
	if completion < 0 {
		completion = 0
	}
	if prompt == 0 && completion == 0 {
		return
	}
	// Turn mirrors hold the latest live values (not deltas): the UI footer
	// reads LivePrompt/LiveCompletion for ↑X ↓Y during streaming.
	if prompt > 0 {
		b.livePrompt = prompt
	}
	if completion > 0 {
		b.liveCompletion = completion
	}
	b.liveEstimated = estimated
	if b.state != nil {
		b.state.CommitUsage(prompt, completion, estimated)
	}
}

// LiveSnapshot returns the current turn's live (↑prompt, ↓completion).
func (b *TelemetryBinder) LiveSnapshot() (prompt, completion int, estimated bool) {
	if b == nil {
		return 0, 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.livePrompt, b.liveCompletion, b.liveEstimated
}

// ResetTurn clears the per-turn live mirrors so a cancelled request never
// leaks stale counts into the next command. Durable totals are preserved.
func (b *TelemetryBinder) ResetTurn() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.livePrompt = 0
	b.liveCompletion = 0
	b.liveEstimated = false
}

// HandleDomainEvent projects a TelemetryBus usage event directly into the
// bound task state. It accepts both the live provider.usage_update payload
// and the terminal stream usage payload, so the UI counter stays bound to
// the same TaskState the scheduler persists.
func (b *TelemetryBinder) HandleDomainEvent(ev events.DomainEvent) {
	if b == nil || ev == nil {
		return
	}
	switch p := ev.Payload().(type) {
	case events.ProviderUsageUpdatePayload:
		b.CommitLiveUsage(p.InputTokens, p.OutputTokens, false)
	case events.StreamUsagePayload:
		b.CommitLiveUsage(p.InputTokens, p.OutputTokens, p.Interrupted)
	}
}

// BindTaskTelemetry subscribes state to usage events on bus and returns an
// unsubscribe function. A nil bus or nil state yields a no-op unsubscribe
// so callers never branch. The returned function unsubscribes both event
// types.
func BindTaskTelemetry(state *durable.TaskState, bus *events.Bus) (unsubscribe func()) {
	noop := func() {}
	if state == nil || bus == nil {
		return noop
	}
	b := NewTelemetryBinder(state)
	sub1 := bus.Subscribe(events.EventProviderUsageUpdate, func(ev events.DomainEvent) {
		b.HandleDomainEvent(ev)
	})
	// EventStreamUsage is terminal-only; subscribe when the bus knows it.
	// Subscribe is safe for unknown event types (it creates the topic).
	sub2 := bus.Subscribe(events.EventStreamUsage, func(ev events.DomainEvent) {
		b.HandleDomainEvent(ev)
	})
	return func() {
		sub1.Cancel()
		sub2.Cancel()
	}
}
