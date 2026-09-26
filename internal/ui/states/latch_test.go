package states

import (
	"sync"
	"testing"
)

// The Latch is the StateTablePending hysteresis. These tests are the DoD for the
// property the whole mechanism exists to guarantee: once the hold is ON, the
// input that arrives dozens of times per second CANNOT turn it off.

// ── Zero value / nil ────────────────────────────────────────────────────────

func TestZeroValueLatchIsReleased(t *testing.T) {
	var l Latch
	if l.On() {
		t.Fatal("the zero-value latch is engaged")
	}
	if _, released := l.Counts(); released != 0 {
		t.Fatal("the zero-value latch has a release history")
	}
}

func TestNilLatchIsReleasedAndInert(t *testing.T) {
	var l *Latch
	if l.On() {
		t.Error("a nil latch is engaged")
	}
	if l.Engage() {
		t.Error("a nil latch engaged")
	}
	if l.Release() {
		t.Error("a nil latch released")
	}
	if l.Set(true) {
		t.Error("a nil latch changed")
	}
	if a, b := l.Counts(); a != 0 || b != 0 {
		t.Error("a nil latch reported history")
	}
}

// ── One-way hold ────────────────────────────────────────────────────────────

func TestEngageIsIdempotent(t *testing.T) {
	var l Latch
	if !l.Engage() {
		t.Fatal("the first Engage reported no transition")
	}
	if !l.On() {
		t.Fatal("the latch did not turn ON")
	}
	// Re-engaging while held is a no-op, and that is load-bearing: the mount
	// reconciliation runs on EVERY frame, so a non-idempotent Engage would bump
	// a counter sixty times a second for a hold that began once.
	for i := 0; i < 1000; i++ {
		if l.Engage() {
			t.Fatalf("frame %d re-engaged an already-held latch", i)
		}
	}
	if engaged, _ := l.Counts(); engaged != 1 {
		t.Fatalf("engaged %d times, want 1 — the hold began once", engaged)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	var l Latch
	if l.Release() {
		t.Fatal("releasing an open latch reported a transition")
	}
	l.Engage()
	if !l.Release() {
		t.Fatal("the first Release reported no transition")
	}
	for i := 0; i < 1000; i++ {
		if l.Release() {
			t.Fatalf("frame %d re-released a released latch", i)
		}
	}
	if _, released := l.Counts(); released != 1 {
		t.Fatalf("released %d times, want 1", released)
	}
}

// TestDuplicateReleaseSignalsAreHarmless is the realistic stale-signal case: a
// terminal message that arrives after the block was already released must not
// disturb anything. A release is idempotent, so a DUPLICATE release is free.
//
// (A release that is stale because a NEW hold has since been installed is a
// different problem, and is keyed by epoch at the Machine level — Snapshot.Epoch
// is exactly the handle for "release only if this is still the hold I mounted".
// The Latch is deliberately unkeyed: it is the hysteretic hold itself, and the
// caller reconciles it from the authoritative holdback, which cannot be stale
// because it is read on the same turn it is written.)
func TestDuplicateReleaseSignalsAreHarmless(t *testing.T) {
	var l Latch
	l.Engage()
	if !l.Release() {
		t.Fatal("the first release reported no transition")
	}
	// The duplicate terminal message.
	for i := 0; i < 100; i++ {
		if l.Release() {
			t.Fatalf("duplicate release %d reported a transition", i)
		}
	}
	if l.On() {
		t.Fatal("a duplicate release engaged the latch")
	}
	if _, released := l.Counts(); released != 1 {
		t.Fatalf("released %d times, want 1", released)
	}
}

// TestSetReconcilesWithoutInventing is the derived-latch contract: Set mirrors an
// authoritative source and reports only real transitions, so reconciling every
// tick produces no history at all when the source is steady.
func TestSetReconcilesWithoutInventing(t *testing.T) {
	var l Latch
	for i := 0; i < 500; i++ {
		if l.Set(false) {
			t.Fatalf("reconcile %d reported a transition on a steady source", i)
		}
	}
	if engaged, released := l.Counts(); engaged != 0 || released != 0 {
		t.Fatalf("steady reconcile wrote history: %d on / %d off", engaged, released)
	}
	if !l.Set(true) {
		t.Fatal("the first Set(true) reported no transition")
	}
	if l.Set(true) {
		t.Fatal("Set(true) on a held latch reported a transition")
	}
	if !l.Set(false) {
		t.Fatal("Set(false) on a held latch reported no transition")
	}
	if engaged, released := l.Counts(); engaged != 1 || released != 1 {
		t.Fatalf("history = %d on / %d off, want 1/1", engaged, released)
	}
}

// TestLatchNeverOscillatesUnderRepeatedObservation is the anti-flicker DoD
// stated as a property: thousands of consecutive observations of a held
// construct produce exactly one engage and zero releases. A per-chunk derivation
// would produce thousands of both.
func TestLatchNeverOscillatesUnderRepeatedObservation(t *testing.T) {
	var l Latch
	l.Engage()
	for i := 0; i < 10_000; i++ {
		if l.On() {
			continue
		}
		l.Engage()
	}
	engaged, released := l.Counts()
	if engaged != 1 {
		t.Errorf("engaged %d times over 10k observations, want 1", engaged)
	}
	if released != 0 {
		t.Errorf("released %d times over 10k observations, want 0", released)
	}
}

// ── Concurrency ─────────────────────────────────────────────────────────────

// TestLatchIsSafeForConcurrentObservation matches how the runtime actually uses
// it: the render path reads the latch from the UI goroutine while a backend
// worker may be staging a transition. Run under -race, this is the proof that
// the read path is not a data race.
func TestLatchIsSafeForConcurrentObservation(t *testing.T) {
	var l Latch
	l.Engage()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				_ = l.On()
				_, _ = l.Counts()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 2000; j++ {
			l.Set(j%2 == 0)
		}
	}()
	wg.Wait()
}
