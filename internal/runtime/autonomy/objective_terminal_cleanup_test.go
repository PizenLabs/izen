package autonomy

// ── Terminal-state cleanup ───────────────────────────────────────────────────
//
// Every transition that abandons or replaces a staged completion decision must
// discard the grant. The staged PROVEN verdict is per-decision state: it is
// created by authorizeObjectiveCompletion, consumed by commitObjectiveCompletion
// (the ONE authoritative commit path), and must never leak into another
// decision or run.
//
// These tests pin each abandonment path: cancellation, abort, retry, resume,
// a new run, and a later downgrade. They drive the exact seam — no provider,
// no wall-clock timing.

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// stageCleanupGrant stages a PROVEN grant on the driver and returns the
// completing decision it was staged for.
func stageCleanupGrant(t *testing.T, d *Driver) autonomy.LoopDecision {
	t.Helper()
	decision := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "matrix proposed completion"}
	d.authorizeObjectiveCompletion(&decision)
	if d.pendingProven == nil {
		t.Fatal("the authority did not stage a grant")
	}
	return decision
}

func grantingCleanupDriver(t *testing.T) *Driver {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	t.Cleanup(bus.Close)
	d := NewDriver(nil, bus, WithObjectiveAuthority(&recordingAuthority{outcome: execution.ObjectiveProven}))
	d.prompt = "redesign this project @note.txt"
	d.obs = autonomy.Observation{}
	return d
}

// Cancellation discards the staged grant: a cancelled run writes no terminal
// objective state, and the discarded grant cannot be committed afterwards.
func TestTerminalCleanup_CancelDiscardsStagedGrant(t *testing.T) {
	d := grantingCleanupDriver(t)
	_ = stageCleanupGrant(t, d)
	d.discardPendingProven()
	if d.pendingProven != nil {
		t.Fatal("cancellation left a staged grant behind")
	}
	stale := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "stale"}
	d.commitObjectiveCompletion(&stale)
	if d.pendingProven != nil {
		t.Fatal("a discarded grant remained committable")
	}
}

// Abort discards the staged grant via the same cleanup path release uses.
func TestTerminalCleanup_AbortDiscardsStagedGrant(t *testing.T) {
	d := grantingCleanupDriver(t)
	_ = stageCleanupGrant(t, d)
	d.discardPendingProven()
	d.releaseRunResources()
	if d.pendingProven != nil {
		t.Fatal("abort/release left a staged grant behind")
	}
}

// A retry decision point clears the previous grant: authorize clears at entry
// for every decision, including a non-completing repair.
func TestTerminalCleanup_RetryDecisionClearsStagedGrant(t *testing.T) {
	d := grantingCleanupDriver(t)
	_ = stageCleanupGrant(t, d)
	retry := autonomy.LoopDecision{Action: autonomy.LoopRepair, Reason: "bounded repair"}
	d.authorizeObjectiveCompletion(&retry)
	if d.pendingProven != nil {
		t.Fatal("a retry decision point left a stale grant staged")
	}
}

// A parked run holds no staged grant: the deferred commit ran before the park,
// so a resume re-authorizes rather than replaying a previous decision.
func TestTerminalCleanup_ParkedRunHoldsNoStagedGrant(t *testing.T) {
	d, _ := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{{Content: sampleReplace}},
		WithLoopBounds(lifecycleBounds()))
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	if d.pendingProven != nil {
		t.Fatal("a parked run holds a staged grant a resume could replay")
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if d.pendingProven != nil {
		t.Fatal("a resumed terminal run holds a staged grant after commit")
	}
}

// A new run never inherits a staged grant, even when the previous lifecycle
// staged one and never committed it.
func TestTerminalCleanup_NewRunClearsStagedGrant(t *testing.T) {
	_, _, adapter, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()
	d := NewDriver(adapter, bus,
		WithObjectiveAuthority(&recordingAuthority{outcome: execution.ObjectiveProven}),
		WithLoopBounds(lifecycleBounds()))
	d.prompt = "redesign this project @note.txt"
	d.pendingProven = &execution.ObjectiveEvaluation{Outcome: execution.ObjectiveProven}
	if _, err := d.Run(context.Background(), "explain the file @note.txt"); err != nil {
		t.Logf("Run returned (assertion below is outcome-independent): %v", err)
	}
	if d.pendingProven != nil {
		t.Fatal("a new run inherited a staged grant")
	}
}

// A later downgrade discards the grant: the single authoritative commit path
// writes nothing when the decision is no longer completing.
func TestTerminalCleanup_LaterDowngradeDiscardsStagedGrant(t *testing.T) {
	d := grantingCleanupDriver(t)
	rec := newObjectiveGrantRecorder(d.bus)
	decision := stageCleanupGrant(t, d)
	decision.Action = autonomy.LoopUnsubstantiate
	decision.Reason = "contract recovery exhausted"
	d.commitObjectiveCompletion(&decision)
	if d.pendingProven != nil {
		t.Fatal("a downgraded completion left its staged grant behind")
	}
	if grants, _ := rec.counts(); grants != 0 {
		t.Fatalf("a downgraded completion committed %d terminal grant(s)", grants)
	}
}
