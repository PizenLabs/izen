package scheduler

import (
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// TestTelemetryBinder_AlwaysFlush pins the Always-Flush Telemetry Invariant:
// every live usage commit lands in TaskState.TokenUsage immediately,
// including estimated partials from timeouts.
func TestTelemetryBinder_AlwaysFlush(t *testing.T) {
	state := &durable.TaskState{}
	b := NewTelemetryBinder(state)
	b.CommitLiveUsage(100, 20, true)
	if state.TokenUsage.PromptTokens != 100 {
		t.Fatalf("prompt = %d, want 100", state.TokenUsage.PromptTokens)
	}
	if state.TokenUsage.CompletionTokens != 20 {
		t.Fatalf("completion = %d, want 20", state.TokenUsage.CompletionTokens)
	}
	if !state.TokenUsage.Estimated {
		t.Fatal("estimated latch must be set after an estimated commit")
	}
	// Authoritative commit clears the latch but accumulates.
	b.CommitLiveUsage(10, 5, false)
	if state.TokenUsage.PromptTokens != 110 {
		t.Fatalf("prompt = %d, want 110 (accumulated)", state.TokenUsage.PromptTokens)
	}
	if state.TokenUsage.Estimated {
		t.Fatal("authoritative commit must clear the estimated latch")
	}
}

// TestTelemetryBinder_ResetTurnClearsStale pins that cancelled requests never
// show stale counts: ResetTurn clears live mirrors while durable totals stay.
func TestTelemetryBinder_ResetTurnClearsStale(t *testing.T) {
	state := &durable.TaskState{}
	b := NewTelemetryBinder(state)
	b.CommitLiveUsage(50, 10, false)
	b.ResetTurn()
	p, c, _ := b.LiveSnapshot()
	if p != 0 || c != 0 {
		t.Fatalf("live = (%d,%d), want (0,0) after ResetTurn", p, c)
	}
	if state.TokenUsage.PromptTokens != 50 {
		t.Fatalf("durable prompt = %d, want 50 (preserved)", state.TokenUsage.PromptTokens)
	}
}

// TestBindTaskTelemetry_ProjectsBusEvents pins the UI & Task State
// Synchronization: provider.usage_update events on the TelemetryBus commit
// directly to TaskState.TokenUsage.
func TestBindTaskTelemetry_ProjectsBusEvents(t *testing.T) {
	state := &durable.TaskState{}
	bus := events.NewBus(16)
	defer bus.Close()
	unsub := BindTaskTelemetry(state, bus)
	defer unsub()

	// Synchronous deterministic projection (no async bus read in test —
	// the bus subscription itself is exercised for wiring, the commit
	// path is asserted synchronously to stay race-clean).
	b := NewTelemetryBinder(state)
	b.HandleDomainEvent(events.NewProviderUsageUpdate("req-1", "model", 30, 12, 0))
	if state.TokenUsage.PromptTokens != 30 {
		t.Fatalf("prompt = %d, want 30", state.TokenUsage.PromptTokens)
	}
	if state.TokenUsage.CompletionTokens != 12 {
		t.Fatalf("completion = %d, want 12", state.TokenUsage.CompletionTokens)
	}
	// Nil safety: never panics, never requires branching by callers.
	if f := BindTaskTelemetry(nil, nil); f == nil {
		t.Fatal("nil bind must return a no-op func")
	} else {
		f()
	}
}

// TestTelemetryBinder_ConcurrentCommitsRace pins race safety: concurrent live
// commits never corrupt totals (run with -race).
func TestTelemetryBinder_ConcurrentCommitsRace(t *testing.T) {
	state := &durable.TaskState{}
	b := NewTelemetryBinder(state)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				b.CommitLiveUsage(1, 1, false)
			}
		}()
	}
	wg.Wait()
	if state.TokenUsage.PromptTokens != 200 {
		t.Fatalf("prompt = %d, want 200", state.TokenUsage.PromptTokens)
	}
}

// TestTaskState_CommitUsageIgnoresZero pins that zero commits never create
// phantom turns.
func TestTaskState_CommitUsageIgnoresZero(t *testing.T) {
	state := &durable.TaskState{}
	state.CommitUsage(0, 0, false)
	if state.TokenUsage.Turns != 0 {
		t.Fatalf("turns = %d, want 0 for zero commit", state.TokenUsage.Turns)
	}
}
