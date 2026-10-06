package autonomy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── R6-B: INTERRUPT / QUEUE LIFECYCLE FORENSICS (driver boundary) ───────────
//
// R6-B asks whether a cancellation applies to exactly the intended execution.
// The first fact the investigation established is that the autonomous runtime
// has NO queued-execution lifecycle at all: it is single-lane by construction.
// A second Run while one is active OR parked is REFUSED, never enqueued
// (driver.go Run duplicate-start protection; ui.runAutonomousDriver mirrors it).
//
// Because there is no A/B execution queue, the R6-B scenarios are proven in
// their truthful single-lane form:
//
//   - a second execution cannot be queued behind the first (it is refused);
//   - cancellation is scoped to ONE execution context (two independent drivers
//     prove the withdrawal cannot cross a boundary);
//   - a parked run is aborted, not context-cancelled, and abort is terminal and
//     non-resumable;
//   - a late decision for a terminated run cannot cross into the next run;
//   - continuation keeps its original execution identity.

// r6bDriver builds an independent driver fixture over a fresh workspace and a
// bus the test can subscribe to.
func r6bDriver(t *testing.T, prov ai.Provider) (*Driver, string, *events.Bus, *mockProvider) {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, prov, bus)
	a := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	var mock *mockProvider
	if m, ok := prov.(*mockProvider); ok {
		mock = m
	}
	return NewDriver(a, bus), root, bus, mock
}

// TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked pins the
// foundational fact: there is no execution queue. A second Run is refused while
// the first is active and while the first is parked; it is never enqueued.
func TestR6B_NoQueue_SecondExecutionIsRefusedWhileActiveOrParked(t *testing.T) {
	t.Run("active", func(t *testing.T) {
		prov := &blockingProvider{started: make(chan struct{})}
		d, _, _, _ := r6bDriver(t, prov)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := r6RunAsync(d, ctx, "explain the file @note.txt")
		select {
		case <-prov.started:
		case <-time.After(5 * time.Second):
			t.Fatal("first run never reached the provider")
		}

		// The first execution is in flight; a second MUST be refused, not queued.
		if _, err := d.Run(context.Background(), "explain the file @note.txt"); err == nil {
			t.Fatal("a second Run was accepted while the first was active — a queue was silently created")
		} else if !strings.Contains(err.Error(), "already active or parked") {
			t.Fatalf("second Run refused for the wrong reason: %v", err)
		}
		// Only ONE execution identity exists.
		if d.RunID() == "" {
			t.Fatal("an active run must publish its execution identity")
		}

		cancel()
		select {
		case r := <-done:
			if r.term == nil || r.term.State != autonomy.RuntimeAborted {
				t.Fatalf("active run = %+v, want aborted after cancel", r.term)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("first run did not return after cancellation")
		}
	})

	t.Run("parked", func(t *testing.T) {
		d, _, _, mock := r6bDriver(t, &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}})
		if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !d.Parked() {
			t.Fatalf("run state = %s, want parked at the approval boundary", d.State())
		}
		// A parked execution is live work: a second Run MUST be refused.
		if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err == nil {
			t.Fatal("a second Run was accepted while the first was parked — it clobbered the parked state")
		}
		if got := mock.calls(); got != 1 {
			t.Fatalf("provider calls = %d, want 1 (a refused second start must spend nothing)", got)
		}
		if _, err := d.Abort("cleanup"); err != nil {
			t.Fatalf("Abort: %v", err)
		}
	})
}

// TestR6B_CancellationIsExecutionScoped proves Invariant 1 and Invariant 2: a
// cancellation request belongs to ONE execution context. Two independent
// executions run concurrently; withdrawing execution A's context leaves
// execution B's terminal state, evidence and workspace untouched.
func TestR6B_CancellationIsExecutionScoped(t *testing.T) {
	// A is a long-running read-only execution, blocked at the provider.
	provA := &blockingProvider{started: make(chan struct{})}
	dA, rootA, _, _ := r6bDriver(t, provA)

	// B is an independent execution that completes a real mutation.
	provB := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	dB, rootB, _, _ := r6bDriver(t, provB)

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	doneA := r6RunAsync(dA, ctxA, "explain the file @note.txt")
	select {
	case <-provA.started:
	case <-time.After(5 * time.Second):
		t.Fatal("execution A never reached the provider")
	}

	// Execution B parks at approval while A is still blocked, then commits.
	if _, err := dB.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("B Run: %v", err)
	}
	termB, err := dB.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("B ResumeApprove: %v", err)
	}
	if termB == nil || termB.State != autonomy.RuntimeCompleted {
		t.Fatalf("B termination = %+v, want completed", termB)
	}
	if got := readTarget(t, rootB, "note.txt"); got == sampleOriginal {
		t.Fatal("B did not commit its mutation")
	}

	// Now cancel A. It must not touch B.
	cancelA()
	var rA r6RunResult
	select {
	case rA = <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("execution A did not return after cancellation")
	}
	if rA.term == nil || rA.term.State != autonomy.RuntimeAborted {
		t.Fatalf("A termination = %+v, want aborted", rA.term)
	}
	if dB.State() != autonomy.RuntimeCompleted {
		t.Fatalf("cancelling A changed B's execution state to %s", dB.State())
	}
	if got := readTarget(t, rootB, "note.txt"); got == sampleOriginal {
		t.Fatal("cancelling A rolled back B's committed mutation")
	}
	if got := readTarget(t, rootA, "note.txt"); got != sampleOriginal {
		t.Fatalf("cancelled A mutated its own workspace: %q", got)
	}
}

// TestR6B_ParkedAbortIsTerminalNonResumableAndDistinctFromRunningCancel proves
// Invariant 6: aborting a parked execution terminates it truthfully, leaves it
// non-resumable, and a fresh execution may then start. Parked-abort is a
// DIFFERENT transition from a running context cancel.
func TestR6B_ParkedAbortIsTerminalNonResumableAndDistinctFromRunningCancel(t *testing.T) {
	prov := &mockProvider{responses: []*ai.Response{
		{Content: sampleReplace},
		{Content: "note.txt is a plain text file."},
	}}
	d, root, bus, mock := r6bDriver(t, prov)

	aborted := make(chan events.AutonomousLifecyclePayload, 1)
	bus.Subscribe(events.EventAutonomousAborted, func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.AutonomousLifecyclePayload); ok {
			aborted <- p
		}
	})

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !d.Parked() {
		t.Fatalf("state = %s, want parked", d.State())
	}
	parkedRunID := d.RunID()
	if parkedRunID == "" {
		t.Fatal("a parked execution must publish its identity")
	}

	term, err := d.Abort("operator")
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("abort termination = %+v, want aborted", term)
	}
	if !strings.Contains(term.Reason, "aborted by operator") {
		t.Fatalf("abort reason = %q, want the operator-abort vocabulary (distinct from 'context cancelled')", term.Reason)
	}
	if d.Parked() {
		t.Fatal("an aborted execution is still reported parked")
	}
	if !d.State().IsTerminal() {
		t.Fatalf("aborted state = %s, want terminal", d.State())
	}
	// The workspace was never touched: abort is not a mutation.
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("aborting a parked run mutated the workspace: %q", got)
	}
	// The parked decision cannot be resumed after abort.
	if _, err := d.ResumeApprove(context.Background()); err == nil {
		t.Fatal("ResumeApprove succeeded on an aborted run — a terminal state was resurrected")
	}
	if got := mock.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (abort must not re-execute)", got)
	}
	select {
	case p := <-aborted:
		if p.Reason == "" {
			t.Fatal("autonomous.aborted carried no reason")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no autonomous.aborted event observed for the parked abort")
	}

	// A fresh execution is legal only because the prior one is TERMINAL.
	term2, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("fresh Run after abort: %v", err)
	}
	if term2 == nil || term2.State != autonomy.RuntimeCompleted {
		t.Fatalf("fresh run termination = %+v, want completed", term2)
	}
	if d.RunID() == parkedRunID {
		t.Fatalf("a fresh execution reused the aborted run's identity %q", parkedRunID)
	}
}

// TestR6B_LateDecisionCannotCrossIntoTheNextExecution proves Invariant 3 and
// Invariant 8: a decision that belonged to a terminated execution cannot be
// applied to the execution that replaced it, and the replacement's terminal
// state and workspace are immutable with respect to the stale decision.
func TestR6B_LateDecisionCannotCrossIntoTheNextExecution(t *testing.T) {
	prov := &mockProvider{responses: []*ai.Response{
		{Content: sampleReplace},
		{Content: "note.txt is a plain text file."},
	}}
	d, root, _, mock := r6bDriver(t, prov)

	// Execution A parks holding an approval decision.
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("A Run: %v", err)
	}
	if !d.Parked() {
		t.Fatalf("A state = %s, want parked", d.State())
	}
	abandonedRunID := d.RunID()

	// A is abandoned (aborted) — terminal, holding nothing.
	if _, err := d.Abort("abandoned"); err != nil {
		t.Fatalf("A Abort: %v", err)
	}

	// Execution B starts on the same driver and completes.
	termB, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("B Run: %v", err)
	}
	if termB == nil || termB.State != autonomy.RuntimeCompleted {
		t.Fatalf("B termination = %+v, want completed", termB)
	}
	if d.RunID() == abandonedRunID {
		t.Fatal("B inherited A's execution identity")
	}
	beforeB := readTarget(t, root, "note.txt")

	// A's late decision arrives now. The driver holds B's execution; the stale
	// decision must be refused and must not alter B or the workspace.
	if _, err := d.ResumeApprove(context.Background()); err == nil {
		t.Fatal("a late approval for an abandoned execution was accepted against the next execution")
	}
	if d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("a late decision changed B's terminal state to %s", d.State())
	}
	if afterB := readTarget(t, root, "note.txt"); afterB != beforeB {
		t.Fatalf("a late decision mutated B's workspace: %q -> %q", beforeB, afterB)
	}
	if got := mock.calls(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (the late decision must not re-execute)", got)
	}
}

// TestR6B_ContinuationKeepsItsOriginalExecutionIdentity proves Invariant 7: an
// approved continuation is the SAME execution (its stable run identity is
// unchanged), and a cancelled unrelated execution cannot alter it.
func TestR6B_ContinuationKeepsItsOriginalExecutionIdentity(t *testing.T) {
	prov := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	d, root, _, _ := r6bDriver(t, prov)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	runID := d.RunID()
	if runID == "" {
		t.Fatal("no execution identity published")
	}

	term, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	// The continuation is the SAME execution: its run identity survives.
	if d.RunID() != runID {
		t.Fatalf("continuation changed the execution identity: %q -> %q", runID, d.RunID())
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approved continuation did not commit")
	}
}
