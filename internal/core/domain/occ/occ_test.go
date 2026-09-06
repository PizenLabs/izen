package occ_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

func TestStateVersionNext(t *testing.T) {
	var v occ.StateVersion
	if v.Next() != 1 {
		t.Fatalf("Next() = %d, want 1", v.Next())
	}
	v = 41
	if v.Next() != 42 {
		t.Fatalf("Next() = %d, want 42", v.Next())
	}
}

func TestVersionedEntityJSON(t *testing.T) {
	e := occ.VersionedEntity{Version: 5}
	if e.Version != 5 {
		t.Fatalf("Version = %d, want 5", e.Version)
	}
}

func TestOCCGateValidateAndAdvance_Success(t *testing.T) {
	var gate occ.OCCGate
	var cur occ.StateVersion = 3
	next, err := gate.ValidateAndAdvance(&cur, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next != 4 || cur != 4 {
		t.Fatalf("next = %d cur = %d, want 4", next, cur)
	}
}

func TestOCCGateValidateAndAdvance_Stale(t *testing.T) {
	var gate occ.OCCGate
	var cur occ.StateVersion = 5
	_, err := gate.ValidateAndAdvance(&cur, 4)
	if err == nil {
		t.Fatal("expected ErrStaleDependency")
	}
	if !errors.Is(err, occ.ErrStaleDependency) {
		t.Fatalf("error = %v, want ErrStaleDependency", err)
	}
	if cur != 5 {
		t.Fatalf("cur = %d, want 5 (unchanged on stale)", cur)
	}
}

func TestOCCGateConcurrentExactlyOneSuccess(t *testing.T) {
	var gate occ.OCCGate
	var cur occ.StateVersion
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	success := 0
	stale := 0
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := gate.ValidateAndAdvance(&cur, 0)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, occ.ErrStaleDependency):
				stale++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("success = %d, want exactly 1", success)
	}
	if stale != n-1 {
		t.Fatalf("stale = %d, want %d", stale, n-1)
	}
	if cur != 1 {
		t.Fatalf("cur = %d, want 1", cur)
	}
}

func TestOCCGateSequentialMonotonic(t *testing.T) {
	var gate occ.OCCGate
	var cur occ.StateVersion
	for i := 0; i < 10; i++ {
		expected := cur
		next, err := gate.ValidateAndAdvance(&cur, expected)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if next != occ.StateVersion(i+1) {
			t.Fatalf("iteration %d: next = %d, want %d", i, next, i+1)
		}
	}
}
