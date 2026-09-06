package architecture

import (
	"errors"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/artifact"
	"github.com/PizenLabs/izen/internal/core/domain/execution"
	"github.com/PizenLabs/izen/internal/core/domain/occ"
	"github.com/PizenLabs/izen/internal/core/domain/workflow"
	"github.com/PizenLabs/izen/internal/runtime"
)

// TestPhase3OCCLock verifies monotonic OCC across all core domain entities:
// concurrent mutation requests targeting the same entity result in exactly one
// successful commit and N-1 ErrStaleDependency rejections, and UI projections
// rejecting stale versions trigger CmdRefreshProjection rather than panicking
// or rendering uncommitted data.
func TestPhase3OCCLock(t *testing.T) {
	t.Run("artifact_store_concurrent_OCC", func(t *testing.T) {
		store := artifact.NewArtifactStore()
		baseVer := store.Version()
		const n = 16
		var wg sync.WaitGroup
		wg.Add(n)
		var mu sync.Mutex
		success, stale := 0, 0
		for i := 0; i < n; i++ {
			go func(idx int) {
				defer wg.Done()
				_, err := store.StagePatch("patch-1", "content", baseVer)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					success++
				} else if errors.Is(err, occ.ErrStaleDependency) {
					stale++
				} else {
					t.Errorf("unexpected error: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if success != 1 {
			t.Fatalf("artifact concurrent: success=%d want 1", success)
		}
		if stale != n-1 {
			t.Fatalf("artifact concurrent: stale=%d want %d", stale, n-1)
		}
		if store.Version() != baseVer.Next() {
			t.Fatalf("artifact version = %d want %d", store.Version(), baseVer.Next())
		}
	})

	t.Run("workflow_state_concurrent_OCC", func(t *testing.T) {
		ws := workflow.NewVersionedWorkflowState()
		_, baseVer := ws.State()
		const n = 16
		var wg sync.WaitGroup
		wg.Add(n)
		var mu sync.Mutex
		success, stale := 0, 0
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				// All goroutines attempt to transition from Idle to Planning with same expected version.
				_, err := ws.Transition(domain.StatePlanning, baseVer)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					success++
				} else if errors.Is(err, occ.ErrStaleDependency) {
					stale++
				} else {
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		wg.Wait()
		if success != 1 {
			t.Fatalf("workflow concurrent: success=%d want 1", success)
		}
		if stale != n-1 {
			t.Fatalf("workflow concurrent: stale=%d want %d", stale, n-1)
		}
	})

	t.Run("execution_state_concurrent_OCC", func(t *testing.T) {
		es := execution.NewExecutionState()
		baseVer := es.Version()
		const n = 16
		var wg sync.WaitGroup
		wg.Add(n)
		var mu sync.Mutex
		success, stale := 0, 0
		for i := 0; i < n; i++ {
			go func(idx int) {
				defer wg.Done()
				_, err := es.RecordStep("step-1", "result", baseVer)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					success++
				} else if errors.Is(err, occ.ErrStaleDependency) {
					stale++
				} else {
					t.Errorf("unexpected error: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if success != 1 {
			t.Fatalf("execution concurrent: success=%d want 1", success)
		}
		if stale != n-1 {
			t.Fatalf("execution concurrent: stale=%d want %d", stale, n-1)
		}
	})

	t.Run("occ_gate_direct_concurrent", func(t *testing.T) {
		var gate occ.OCCGate
		var cur occ.StateVersion
		const n = 24
		var wg sync.WaitGroup
		wg.Add(n)
		var mu sync.Mutex
		success, stale := 0, 0
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				_, err := gate.ValidateAndAdvance(&cur, 0)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					success++
				} else if errors.Is(err, occ.ErrStaleDependency) {
					stale++
				} else {
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		wg.Wait()
		if success != 1 {
			t.Fatalf("gate concurrent: success=%d want 1", success)
		}
		if stale != n-1 {
			t.Fatalf("gate concurrent: stale=%d want %d", stale, n-1)
		}
	})

	t.Run("projection_stale_triggers_refresh_not_panic", func(t *testing.T) {
		proj := runtime.NewVersionedProjection(5)
		// Stale observed version 3 should be rejected.
		err := proj.Validate(3)
		if err == nil {
			t.Fatal("expected ErrStaleProjection for stale version")
		}
		if !errors.Is(err, runtime.ErrStaleProjection) {
			t.Fatalf("error = %v, want ErrStaleProjection", err)
		}
		if !errors.Is(err, occ.ErrStaleDependency) {
			t.Fatalf("error should wrap ErrStaleDependency, got %v", err)
		}
		if !proj.NeedsRefresh(3) {
			t.Fatal("NeedsRefresh should be true for stale version")
		}
		// Must not panic and must return CmdRefreshProjection.
		res := proj.HandleStale(3)
		if !res.Refresh {
			t.Fatal("HandleStale.Refresh = false, want true")
		}
		if res.Command != runtime.CmdRefreshProjection {
			t.Fatalf("HandleStale.Command = %q, want %q", res.Command, runtime.CmdRefreshProjection)
		}
		if res.Current != 5 {
			t.Fatalf("HandleStale.Current = %d, want 5", res.Current)
		}
		// Fresh version must not trigger refresh.
		if err := proj.Validate(5); err != nil {
			t.Fatalf("fresh Validate should succeed, got %v", err)
		}
		if proj.NeedsRefresh(5) {
			t.Fatal("NeedsRefresh should be false for committed version")
		}
		res2 := proj.HandleStale(5)
		if res2.Refresh {
			t.Fatal("HandleStale should not refresh for committed version")
		}

		// Ensure no panic on concurrent projection checks.
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(v occ.StateVersion) {
				defer wg.Done()
				_ = proj.Validate(v)
				_ = proj.HandleStale(v)
			}(occ.StateVersion(i))
		}
		wg.Wait()
	})
}
