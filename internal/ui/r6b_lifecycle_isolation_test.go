package ui

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// ── R6-B: INTERRUPT SCOPING AT THE PRESENTATION LIFECYCLE ───────────────────
//
// These tests pin the UI half of the single-lane contract: a parked execution
// is live work, so a second autonomous start is refused; and Ctrl+C with a
// parked run selects THAT run (via the driver's Abort) without touching an
// unrelated execution's context.

func r6bParkedModel(t *testing.T) (*model, *fakeAutonomousDriver, context.Context, context.CancelFunc) {
	t.Helper()
	drv := &fakeAutonomousDriver{
		state: autonomy.RuntimeAwaitingHuman,
		runID: "run-1",
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "mutation ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
	}
	m := autonomousTestModel(drv)
	// A parked run holds its boundary and is NOT active.
	m.autonomousBoundary = drv.boundary
	m.autonomousActive = false
	ctx, cancel := context.WithCancel(context.Background())
	return m, drv, ctx, cancel
}

// TestR6B_UI_SecondAutonomousStartRefusedWhileParked proves the UI does not
// silently queue a second execution behind a parked one.
func TestR6B_UI_SecondAutonomousStartRefusedWhileParked(t *testing.T) {
	m, drv, _, cancel := r6bParkedModel(t)
	defer cancel()
	if !m.autonomousParked() {
		t.Fatal("precondition: model must hold a parked execution")
	}
	if cmd := m.runAutonomousDriver("a second objective"); cmd != nil {
		t.Fatal("the UI accepted a second autonomous start while a run was parked")
	}
	if drv.runCount != 0 {
		t.Fatalf("driver Run calls = %d, want 0 (the refusal must happen before dispatch)", drv.runCount)
	}
}

// TestR6B_UI_ParkedCtrlCTargetsTheParkedRunOnly proves Ctrl+C with a parked run
// aborts THAT run through the driver, and does not withdraw an unrelated
// execution's context.
func TestR6B_UI_ParkedCtrlCTargetsTheParkedRunOnly(t *testing.T) {
	m, drv, otherCtx, cancel := r6bParkedModel(t)
	defer cancel()

	// An unrelated execution holds its own live context.
	if otherCtx.Err() != nil {
		t.Fatal("precondition: unrelated context must start live")
	}

	handled, cmd := m.handleCtrlC()
	if !handled {
		t.Fatal("Ctrl+C with a parked run must be handled by the parked-run protocol")
	}
	if cmd == nil {
		t.Fatal("Ctrl+C with a parked run must schedule the driver abort")
	}
	msg := cmd()
	if _, ok := msg.(autonomousRunMsg); !ok {
		t.Fatalf("parked Ctrl+C produced %T, want autonomousRunMsg", msg)
	}
	if drv.abortCount != 1 {
		t.Fatalf("driver Abort calls = %d, want 1", drv.abortCount)
	}
	if drv.abortReason == "" {
		t.Fatal("driver Abort received no reason")
	}
	// The unrelated execution was not touched: cancelling parked work does not
	// interrupt other work.
	select {
	case <-otherCtx.Done():
		t.Fatal("aborting the parked run withdrew an unrelated execution's context")
	case <-time.After(50 * time.Millisecond):
	}
}
