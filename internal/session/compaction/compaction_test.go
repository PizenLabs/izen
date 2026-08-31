package compaction

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/session"
)

func turn(role, content string) session.Message {
	return session.Message{Role: role, Content: content, Timestamp: time.Now()}
}

func turns(n int, role, content string) []session.Message {
	out := make([]session.Message, n)
	for i := range out {
		out[i] = turn(role, content)
	}
	return out
}

func baseHistory(n int) []session.Message {
	var out []session.Message
	for i := 0; i < n; i++ {
		out = append(out, turn("user", "turn"))
		out = append(out, turn("assistant", "ok"))
	}
	return out
}

// TestCompactFullBuildsGenerationOne verifies a nil base yields a sealed
// generation 1 carrying the whole (short) history as Recent.
func TestCompactFullBuildsGenerationOne(t *testing.T) {
	e := New(DefaultPolicy())
	hist := baseHistory(3)

	cc, sealed := e.Compact(nil, hist)
	if !sealed {
		t.Fatal("first compact must seal a generation")
	}
	if cc.Generation != 1 {
		t.Errorf("Generation = %d, want 1", cc.Generation)
	}
	if cc.EventCount != len(hist) {
		t.Errorf("EventCount = %d, want %d", cc.EventCount, len(hist))
	}
	if len(cc.Recent) != len(hist) {
		t.Errorf("Recent = %d entries, want %d", len(cc.Recent), len(hist))
	}
	if cc.CompactedAt.IsZero() {
		t.Error("CompactedAt must be set on a sealed generation")
	}
}

// TestCompactIncrementalBelowThresholdKeepsGeneration verifies the adaptive
// core: a small append below every threshold does NOT seal a new generation —
// it refreshes the Recent window only.
func TestCompactIncrementalBelowThresholdKeepsGeneration(t *testing.T) {
	policy := DefaultPolicy() // TurnThreshold=10, EventThreshold=20
	e := New(policy)
	base, _ := e.Compact(nil, baseHistory(3))

	appended := baseHistory(2) // 2 new turns, 4 events — well under thresholds
	next, sealed := e.Compact(base, append(append([]session.Message(nil), base.Recent...), appended...))

	if sealed {
		t.Fatal("sub-threshold append must not seal a new generation")
	}
	if next.Generation != base.Generation {
		t.Errorf("Generation = %d, want %d (no checkpoint)", next.Generation, base.Generation)
	}
	if next.EventCount != base.EventCount {
		t.Errorf("EventCount = %d, want %d (unfolded events stay pending)", next.EventCount, base.EventCount)
	}
	if len(next.Recent) > policy.RecentWindow {
		t.Errorf("Recent = %d, want <= %d", len(next.Recent), policy.RecentWindow)
	}
}

// TestCompactTurnThresholdSealsGeneration verifies crossing the adaptive turn
// threshold advances the generation and folds the previous window into the
// summary.
func TestCompactTurnThresholdSealsGeneration(t *testing.T) {
	policy := DefaultPolicy()
	e := New(policy)
	hist := baseHistory(3)
	base, _ := e.Compact(nil, hist)
	base.EventCount = 3 // treat the 3 as folded

	// Append enough user turns to cross TurnThreshold=10.
	full := append(append([]session.Message(nil), hist...), baseHistory(policy.TurnThreshold+1)...)
	next, sealed := e.Compact(base, full)

	if !sealed {
		t.Fatal("crossing the turn threshold must seal a new generation")
	}
	if next.Generation != base.Generation+1 {
		t.Errorf("Generation = %d, want %d", next.Generation, base.Generation+1)
	}
	if next.EventCount != len(full) {
		t.Errorf("EventCount = %d, want %d", next.EventCount, len(full))
	}
	if next.Summary == "" {
		t.Error("a sealed generation must fold a summary")
	}
}

// TestCompactEventThresholdTriggersCheckpoint verifies the event-driven
// fallback fires even when user-turn volume stays low (assistant-heavy runs).
func TestCompactEventThresholdTriggersCheckpoint(t *testing.T) {
	policy := DefaultPolicy()
	policy.EventThreshold = 4
	policy.TurnThreshold = 100 // turn volume stays far below
	e := New(policy)
	hist := baseHistory(1)
	base, _ := e.Compact(nil, hist)
	base.EventCount = 2

	// 6 assistant-only events (3 messages × 2... use assistant turns).
	extra := turns(8, "assistant", "trace output")
	next, sealed := e.Compact(base, append(append([]session.Message(nil), hist...), extra...))

	if !sealed {
		t.Fatal("crossing the event threshold must seal a generation even with low turn volume")
	}
	if next.Generation != base.Generation+1 {
		t.Errorf("Generation = %d, want %d", next.Generation, base.Generation+1)
	}
}

// TestCompactMinGapPreventsThrash verifies the minimum-gap guard: a burst too
// close to the last checkpoint never seals prematurely.
func TestCompactMinGapPreventsThrash(t *testing.T) {
	policy := DefaultPolicy()
	policy.MinGapTurns = 5
	policy.TurnThreshold = 3
	e := New(policy)
	base, _ := e.Compact(nil, baseHistory(2))

	// 2 new turns cross TurnThreshold=3 but not MinGapTurns=5.
	next, sealed := e.Compact(base, baseHistory(4))
	if sealed {
		t.Fatal("minimum-gap guard must prevent a premature checkpoint")
	}
	if next.Generation != base.Generation {
		t.Errorf("Generation = %d, want %d", next.Generation, base.Generation)
	}
}

// TestCompactTokenGrowthTriggersCheckpoint verifies token-growth adaptation:
// a single turn that doubles the summary forces a checkpoint below the turn
// threshold.
func TestCompactTokenGrowthTriggersCheckpoint(t *testing.T) {
	policy := DefaultPolicy()
	policy.TurnThreshold = 100
	policy.EventThreshold = 100
	policy.TokenGrowthFactor = 1.5
	e := New(policy)
	base, _ := e.Compact(nil, []session.Message{turn("user", "small goal")})

	full := append([]session.Message{turn("user", "small goal")}, turn("user", repeat("x", 4000)))
	next, sealed := e.Compact(base, full)

	if !sealed {
		t.Fatal("summary token growth past the factor must seal a generation")
	}
	if next.Generation != base.Generation+1 {
		t.Errorf("Generation = %d, want %d", next.Generation, base.Generation+1)
	}
}

// TestRebuildFromLogIncremental verifies the INV-SESSION-14 incremental
// rebuild: a base generation plus a longer log preserves the generation
// lineage, refreshes Recent and never loses the summary. Under a default
// (high-threshold) policy the extra events are below every adaptive threshold,
// so the rebuild stays on the same generation — folding only happens when a
// threshold actually crosses.
func TestRebuildFromLogIncremental(t *testing.T) {
	e := New(DefaultPolicy())
	hist := baseHistory(4) // 8 events
	base, _ := e.Compact(nil, hist)
	base.EventCount = 6 // two turns already folded by an earlier generation

	log := append(baseHistory(4), turns(6, "assistant", "tail")...) // 14 events
	meta := GenerationMeta{SessionID: "s-1", Objective: "objective", Mode: "plan", Checkpoint: "cp-1"}
	rebuilt, err := e.RebuildFromLog(base, log, meta)
	if err != nil {
		t.Fatalf("RebuildFromLog: %v", err)
	}
	if rebuilt.SessionID != "s-1" {
		t.Errorf("SessionID = %q, want s-1", rebuilt.SessionID)
	}
	if rebuilt.Checkpoint != "cp-1" {
		t.Errorf("Checkpoint = %q, want cp-1", rebuilt.Checkpoint)
	}
	if rebuilt.EventCount != 6 {
		t.Errorf("EventCount = %d, want 6 (sub-threshold rebuild stays incremental)", rebuilt.EventCount)
	}
	if rebuilt.Summary == "" {
		t.Error("incremental rebuild must preserve a folded summary")
	}
	if len(rebuilt.Recent) == 0 {
		t.Error("incremental rebuild must refresh the Recent window")
	}
}

// TestRebuildFromLogSealsWhenThresholdCrossed verifies the rebuild seals a new
// generation when the appended events cross the adaptive event threshold.
func TestRebuildFromLogSealsWhenThresholdCrossed(t *testing.T) {
	policy := DefaultPolicy()
	policy.EventThreshold = 4
	policy.TurnThreshold = 100
	policy.MinGapTurns = 1
	e := New(policy)
	hist := baseHistory(2) // 4 events
	base, _ := e.Compact(nil, hist)
	base.EventCount = 4

	log := append(baseHistory(2), turns(8, "assistant", "tail")...) // 12 events, 8 new
	rebuilt, err := e.RebuildFromLog(base, log, GenerationMeta{SessionID: "s-3"})
	if err != nil {
		t.Fatalf("RebuildFromLog: %v", err)
	}
	if rebuilt.Generation != base.Generation+1 {
		t.Errorf("Generation = %d, want %d (event threshold crossed)", rebuilt.Generation, base.Generation+1)
	}
	if rebuilt.EventCount != len(log) {
		t.Errorf("EventCount = %d, want %d", rebuilt.EventCount, len(log))
	}
}

// TestRebuildFromLogFullRebuildsWhenBaseStale verifies that a base claiming
// more events than the log actually holds is rejected in favor of a full
// rebuild (never trusting a truncated base).
func TestRebuildFromLogFullRebuildsWhenBaseStale(t *testing.T) {
	e := New(DefaultPolicy())
	base, _ := e.Compact(nil, baseHistory(10))
	base.EventCount = 50 // stale claim: log only has 2

	rebuilt, err := e.RebuildFromLog(base, baseHistory(2), GenerationMeta{SessionID: "s-2"})
	if err != nil {
		t.Fatalf("RebuildFromLog: %v", err)
	}
	if rebuilt.EventCount != 4 { // 2 turns × 2 events
		t.Errorf("EventCount = %d, want 4 (full rebuild, not trusting the stale base)", rebuilt.EventCount)
	}
}

// TestRunnerAsyncNonBlocking verifies the runner processes jobs on a
// background goroutine: Submit never blocks and Close drains the queue.
func TestRunnerAsyncNonBlocking(t *testing.T) {
	var mu sync.Mutex
	var got []session.SlotID
	r := NewRunner(DefaultPolicy(), func(_ context.Context, j Job, cc *session.CompactContext) error {
		mu.Lock()
		got = append(got, j.Slot)
		mu.Unlock()
		return nil
	})
	r.Start()
	defer r.Close()

	const jobs = 50
	for i := 0; i < jobs; i++ {
		r.Submit(Job{Slot: session.SlotA, SessionID: "s", History: baseHistory(2)})
	}

	// Submit returned immediately (non-blocking); the worker must eventually
	// process every job.
	deadline := time.Now().Add(5 * time.Second)
	for r.Processed() < jobs {
		if time.Now().After(deadline) {
			t.Fatalf("processed %d/%d jobs before timeout", r.Processed(), jobs)
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != jobs {
		t.Errorf("sink saw %d jobs, want %d", n, jobs)
	}
}

// TestRunnerSinkErrorIsSurfacedNotPropagated verifies a failing sink never
// panics the worker and is observable via LastError.
func TestRunnerSinkErrorIsSurfacedNotPropagated(t *testing.T) {
	r := NewRunner(DefaultPolicy(), func(context.Context, Job, *session.CompactContext) error {
		return errors.New("sink boom")
	})
	r.Start()
	r.Submit(Job{Slot: session.SlotB, History: baseHistory(1)})

	deadline := time.Now().Add(5 * time.Second)
	for r.Processed() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("job never processed")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if r.LastError() == nil {
		t.Fatal("LastError must surface the sink failure")
	}
}

// TestRunnerDropOnFullQueue verifies Submit never blocks when the queue is
// saturated: jobs are dropped and counted, never stalling the caller.
func TestRunnerDropOnFullQueue(t *testing.T) {
	r := NewRunner(DefaultPolicy(), func(context.Context, Job, *session.CompactContext) error { return nil },
		WithRunnerQueueSize(2))
	r.Start()

	// Saturate the queue with a worker that blocks until released.
	release := make(chan struct{})
	r.sink = func(context.Context, Job, *session.CompactContext) error {
		<-release
		return nil
	}
	for i := 0; i < 8; i++ {
		r.Submit(Job{Slot: session.SlotA, History: baseHistory(1)})
	}
	close(release)
	r.Close()
	if r.Dropped() == 0 {
		t.Error("expected dropped jobs when the queue saturated")
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
