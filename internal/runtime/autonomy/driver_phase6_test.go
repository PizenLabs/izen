package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
)

// TestDriver_AbortParkedApproval proves Abort terminates a parked approval run
// as a permanent human cancellation without touching the held file, and that a
// fresh Run is legal afterwards.
func TestDriver_AbortParkedApproval(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}

	term, err := d.Abort("not needed")
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", term)
	}
	if term.Class != autonomy.FailurePermanent {
		t.Fatalf("abort class = %s, want permanent", term.Class)
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("abort mutated the file: %q", got)
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (abort must not re-execute)", mock.calls())
	}

	// A fresh bounded run is legal after the abort.
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("fresh Run after abort: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state after fresh run = %s, want awaiting_human", d.State())
	}
}

// TestDriver_AbortParkedClarify proves Abort terminates a parked clarify run
// before any execution (no model call, no mutation).
func TestDriver_AbortParkedClarify(t *testing.T) {
	_, mock, a, _ := testHarness(t, nil)
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change @missing.txt to something"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 before clarify", mock.calls())
	}

	term, err := d.Abort("wrong target")
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", term)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 (abort never executes)", mock.calls())
	}
}

// TestDriver_DuplicateStartBlocked proves the single-lane guard: a second Run
// while a run is active OR parked is rejected and the parked boundary is
// preserved so the human can still resume it.
func TestDriver_DuplicateStartBlocked(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}

	// A second Run must be rejected while parked — it must not clobber the
	// held approval gate.
	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err == nil {
		t.Fatalf("second Run while parked must fail, got term=%+v", term)
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("second Run error = %q, want duplicate-start guard", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state after rejected Run = %s, want awaiting_human (boundary preserved)", d.State())
	}

	// The parked approval gate is still resumable.
	term, err = d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove after rejected Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approve did not mutate the file")
	}
}

// TestDriver_ApprovalBoundaryEnrichment proves a parked approval boundary
// carries the authoritative presentation facts: Targets (the resolved target
// set), Action=approve, Resumable=true.
func TestDriver_ApprovalBoundaryEnrichment(t *testing.T) {
	_, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.PatchID == "" {
		t.Fatalf("boundary = %+v, want an approval gate", b)
	}
	if b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary action = %q, want approve", b.Action)
	}
	if !b.Resumable {
		t.Fatal("approval boundary must be resumable")
	}
	if len(b.Targets) == 0 || b.Targets[0] != "note.txt" {
		t.Fatalf("boundary targets = %v, want [note.txt]", b.Targets)
	}
}

// TestDriver_ClarifyBoundaryEnrichment proves a parked clarify boundary carries
// Options, Action=clarify, Resumable=true, and no target set (nothing resolved).
func TestDriver_ClarifyBoundaryEnrichment(t *testing.T) {
	_, _, a, _ := testHarness(t, nil)
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change @missing.txt to something"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || len(b.Options) == 0 {
		t.Fatalf("boundary = %+v, want a clarify boundary", b)
	}
	if b.Action != autonomy.HumanBoundaryClarify {
		t.Fatalf("boundary action = %q, want clarify", b.Action)
	}
	if !b.Resumable {
		t.Fatal("clarify boundary must be resumable")
	}
	if len(b.Targets) != 0 {
		t.Fatalf("clarify boundary targets = %v, want empty (nothing resolved)", b.Targets)
	}
}

// TestDriver_InformBoundaryNotResumable proves a recovery-exhaustion park is an
// inform boundary: Action=inform, Resumable=false, and no resume decision
// exists (the human may only start a fresh run).
func TestDriver_InformBoundaryNotResumable(t *testing.T) {
	_, _, a, _ := testHarness(t, nil) // provider always fails
	d := NewDriver(a, nil, WithLoopBounds(autonomy.LoopBounds{
		MaxAttempts:       10,
		MaxRecoveryCycles: 1,
	}))

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil {
		t.Fatal("expected a parked boundary at recovery exhaustion")
	}
	if b.Action != autonomy.HumanBoundaryInform {
		t.Fatalf("boundary action = %q, want inform", b.Action)
	}
	if b.Resumable {
		t.Fatal("inform boundary must NOT be resumable")
	}
	if b.PatchID != "" {
		t.Fatalf("inform boundary patch id = %q, want empty", b.PatchID)
	}
}
