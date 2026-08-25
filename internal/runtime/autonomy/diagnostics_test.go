package autonomy

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestDiagnosticfDiscardsByDefault is the zero-leak guarantee: with no sink
// installed (headless/CLI or forgotten wiring) boundary telemetry must be
// silently discarded — never printed to process stdio.
func TestDiagnosticfDiscardsByDefault(t *testing.T) {
	SetDiagnosticLog(nil)
	defer SetDiagnosticLog(nil)

	// Must not panic, block, or emit anywhere.
	diagnosticf("[boundary2] nothing %d", 1)
}

// TestSetDiagnosticLogRoutesTelemetry proves the injected sink receives the
// formatted lines (compose.Wire routes it onto the event bus in production).
func TestSetDiagnosticLogRoutesTelemetry(t *testing.T) {
	var mu sync.Mutex
	var got []string
	SetDiagnosticLog(func(format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, strings.TrimSpace(fmt.Sprintf(format, args...)))
	})
	defer SetDiagnosticLog(nil)

	diagnosticf("[boundary5] workspace_drift request=%s", "run-1")
	diagnosticf("[boundary2] DECOMPOSITION_PROPOSAL staged sub_tasks=%d", 3)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("sink received %d lines, want 2: %+v", len(got), got)
	}
	if !strings.Contains(got[0], "workspace_drift request=run-1") ||
		!strings.Contains(got[1], "staged sub_tasks=3") {
		t.Errorf("sink lines malformed: %+v", got)
	}
}

// TestConcurrentDiagnosticsIsRaceFree exercises concurrent emitters under
// -race (the driver and adapter run on separate goroutines).
func TestConcurrentDiagnosticsIsRaceFree(t *testing.T) {
	var mu sync.Mutex
	count := 0
	SetDiagnosticLog(func(format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		count++
	})
	defer SetDiagnosticLog(nil)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				diagnosticf("[boundary2] concurrent line %d", j)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if count != 800 {
		t.Fatalf("sink saw %d lines, want 800", count)
	}
}
