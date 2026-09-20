package components

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTUI_IndependentTickerRender blocks backend event emission for 2
// seconds and asserts the spinner keeps generating frame ticks without
// freezing: frames advance on the local 10Hz ticker while the atomic
// snapshot stays stale, and concurrent Publish/Update/View is race-clean.
func TestTUI_IndependentTickerRender(t *testing.T) {
	state := &atomic.Pointer[ExecutionStateSnapshot]{}
	state.Store(&ExecutionStateSnapshot{Phase: PhaseExecuting, StepID: "step-1", Message: "working"})
	sp := NewSpinner(state)

	// Freeze the backend: no Publish for the whole window (stale snapshot).
	start := sp.Frame()
	deadline := time.Now().Add(2 * time.Second)
	ticks := 0
	for time.Now().Before(deadline) {
		cmd := sp.Update(SpinnerTickMsg(time.Now()))
		if cmd == nil {
			t.Fatal("spinner tick must re-arm the 10Hz ticker even with a frozen backend")
		}
		_ = sp.View() // must never block on backend I/O
		ticks++
		time.Sleep(SpinnerTickInterval / 2) // drive faster than 10Hz; cadence floor asserted below
	}
	if sp.Frame() <= start {
		t.Fatal("spinner frame counter did not advance during backend freeze")
	}
	if ticks < 10 {
		t.Fatalf("ticks = %d during 2s freeze, want >= 10 (10Hz liveness)", ticks)
	}
	first := sp.View()
	second := sp.View()
	if first == "" || second == "" {
		t.Fatal("spinner view must render during backend freeze")
	}

	// Race drill: concurrent publishers (Event Bus subscribers) vs ticker
	// reads. Run with -race: any shared-mutable aliasing fails here.
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// Immutable publish: wholly-new snapshot per Store.
				sp.Publish(&ExecutionStateSnapshot{Phase: PhaseExecuting, StepID: "step-1", Message: "tick"})
			}
		}()
	}
	for i := 0; i < 200; i++ {
		sp.Update(SpinnerTickMsg(time.Now()))
		_ = sp.View()
		_ = sp.Snapshot()
	}
	wg.Wait()
}
