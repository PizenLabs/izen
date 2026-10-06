package autonomy

// ── R6-D: AUTONOMOUS MUTATION SEAM WIRING ──────────────────────────────────
//
// These tests prove the R6-D integration at the REAL autonomous boundary: the
// Driver, driven by the real RuntimeExecutor, dispatches a durable pre-digest
// cursor before the mutation and a durable commit marker (with the observed
// post-digest) once the mutation lands — independent of objective PROVEN. A
// FRESH driver over a replayed store then reconciles the surviving evidence.
//
// Both sides are asserted: the durable journal/cursor AND the workspace bytes.

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

const r6dObjective = "change bar to qux @note.txt"

func r6dPreDigest(t *testing.T, root string) string {
	t.Helper()
	d, err := durable.ComputeTreeDigest(root, "note.txt")
	if err != nil {
		t.Fatalf("pre digest: %v", err)
	}
	return d
}

func r6dLedgerPayload(t *testing.T, events []durable.LedgerEvent, typ durable.EventType) map[string]any {
	t.Helper()
	for _, ev := range events {
		if ev.EventType == typ {
			return ev.Payload
		}
	}
	t.Fatalf("journal has no %s (types: %v)", typ, eventTypes(events))
	return nil
}

func r6dOnlyTask(t *testing.T, d *Driver) string {
	t.Helper()
	id := d.ledgerTaskID(r6dObjective)
	if id == "" {
		t.Fatal("driver holds no durable task id")
	}
	return id
}

// TestR6D_AutonomousMutationDispatchesAndCommitsDurably is the core wiring
// proof: a mutation that lands through the autonomous path leaves a durable
// pre-digest and a durable commit marker carrying the observed post-digest.
func TestR6D_AutonomousMutationDispatchesAndCommitsDurably(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	pre := r6dPreDigest(t, root)

	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))
	if _, err := d.Run(context.Background(), r6dObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Parked at the approval gate: the pre-digest must already be durable, and
	// no commit marker may exist because no mutation has landed yet.
	events := ledgerEvents(t, store.LedgerPath())
	dispatch := r6dLedgerPayload(t, events, durable.EventCursorDispatched)
	if got := dispatch["preconditionDigest"]; got != pre {
		t.Fatalf("dispatched pre-digest = %v, want %s", got, pre)
	}
	if hasEvent(events, durable.EventExecutionCommitted) {
		t.Fatal("a commit marker exists before the mutation was approved")
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("workspace = %q, want unchanged before approval", got)
	}

	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	mutated := readTarget(t, root, "note.txt")
	if mutated == sampleOriginal {
		t.Fatal("approval did not mutate the workspace")
	}
	post, err := durable.ComputeTreeDigest(root, "note.txt")
	if err != nil {
		t.Fatalf("post digest: %v", err)
	}
	events = ledgerEvents(t, store.LedgerPath())
	commit := r6dLedgerPayload(t, events, durable.EventExecutionCommitted)
	if got := commit["postconditionDigest"]; got != post {
		t.Fatalf("committed post-digest = %v, want the observed post-state %s", got, post)
	}
}

// TestR6D_AutonomousCrashAfterCommitReconciles models the exact crash window
// the mission names: the mutation has landed through the real executor and the
// driver's durable commit helper has run, but the process dies BEFORE the
// terminal result. A FRESH runtime over the replayed journal must return
// ALREADY_COMMITTED and must not write again.
func TestR6D_AutonomousCrashAfterCommitReconciles(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))
	if _, err := d.Run(context.Background(), r6dObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Apply the real held mutation and record the same durable commit marker
	// the approval path records, then STOP (no terminal result). This is the
	// post-mutation / pre-terminal crash boundary.
	pid, err := d.approvalPatchID()
	if err != nil {
		t.Fatalf("approval patch id: %v", err)
	}
	if _, err := d.adapter.Approve(context.Background(), pid); err != nil {
		t.Fatalf("apply mutation: %v", err)
	}
	committed := readTarget(t, root, "note.txt")
	if committed == sampleOriginal {
		t.Fatal("the mutation was not applied")
	}
	d.ledgerMutationCommitted(d.objectiveTargets())

	// The interrupting process's memory is now irrelevant.
	reopened := openLedger(t, root)
	fresh := NewDriver(a, nil, WithLedger(reopened, "session-1"))
	got, err := fresh.ReconcileInterrupted()
	if err != nil {
		t.Fatalf("ReconcileInterrupted: %v", err)
	}
	insp := r6dFind(t, got, r6dOnlyTask(t, d))
	if insp.Decision != durable.DecisionAlreadyCommitted {
		t.Fatalf("fresh decision = %s, want ALREADY_COMMITTED (insp=%+v scopes=%v targets=%v)", insp.Decision, insp, reopened.TaskScopes(), d.objectiveTargets())
	}
	if !insp.Committed {
		t.Fatal("fresh runtime did not observe the durable commit marker")
	}
	if after := readTarget(t, root, "note.txt"); after != committed {
		t.Fatalf("workspace = %q after reconciliation, want the committed %q (no duplicate write)", after, committed)
	}
}

// TestR6D_AutonomousPreMutationParkIsSafeRetry pins the D1/D6 case on the real
// path: a run parked at approval has dispatched the pre-digest but never
// mutated, so a fresh runtime reports SAFE_RETRY and the workspace is intact.
func TestR6D_AutonomousPreMutationParkIsSafeRetry(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))
	if _, err := d.Run(context.Background(), r6dObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (no mutation yet)", d.State())
	}

	reopened := openLedger(t, root)
	fresh := NewDriver(a, nil, WithLedger(reopened, "session-1"))
	got, err := fresh.ReconcileInterrupted()
	if err != nil {
		t.Fatalf("ReconcileInterrupted: %v", err)
	}
	insp := r6dFind(t, got, r6dOnlyTask(t, d))
	if insp.Decision != durable.DecisionSafeRetry {
		t.Fatalf("fresh decision = %s, want SAFE_RETRY (pre-state intact, no mutation)", insp.Decision)
	}
	if after := readTarget(t, root, "note.txt"); after != sampleOriginal {
		t.Fatalf("workspace = %q, want the untouched original", after)
	}
}

// TestR6D_ReconcileIsReadOnly proves reconciliation never resumes, retries or
// mutates: it appends no journal event, changes no workspace byte, and invokes
// no provider.
func TestR6D_ReconcileIsReadOnly(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))
	if _, err := d.Run(context.Background(), r6dObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}

	reopened := openLedger(t, root)
	fresh := NewDriver(a, nil, WithLedger(reopened, "session-1"))
	before := readTarget(t, root, "note.txt")
	beforeEvents := len(ledgerEvents(t, reopened.LedgerPath()))
	beforeCalls := mock.calls()

	if _, err := fresh.ReconcileInterrupted(); err != nil {
		t.Fatalf("ReconcileInterrupted: %v", err)
	}

	if after := readTarget(t, root, "note.txt"); after != before {
		t.Fatalf("reconciliation mutated the workspace: %q -> %q", before, after)
	}
	if after := len(ledgerEvents(t, reopened.LedgerPath())); after != beforeEvents {
		t.Fatalf("reconciliation appended %d journal event(s)", after-beforeEvents)
	}
	if after := mock.calls(); after != beforeCalls {
		t.Fatalf("reconciliation invoked the provider %d time(s)", after-beforeCalls)
	}
}

func r6dFind(t *testing.T, insp []durable.CursorInspection, taskID string) durable.CursorInspection {
	t.Helper()
	for _, i := range insp {
		if i.TaskID == taskID {
			return i
		}
	}
	t.Fatalf("inspection has no task %q (got %+v)", taskID, insp)
	return durable.CursorInspection{}
}
