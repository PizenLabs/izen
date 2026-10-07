package autonomy

// ── POST-R7 FINITE REAL-WORLD ACCEPTANCE MATRIX (deterministic) ─────────────
//
// The phase brief asks for a small number of REPRESENTATIVE real tasks, driven
// through the production runtime composition, each proven by evidence that lives
// OUTSIDE the TUI:
//
//	MODIFY      existing target → kernel → bytes → PROVEN
//	READ-ONLY   inspect → answer → ZERO mutation
//	AMBIGUOUS   discovery without authority → clarification, ZERO provider calls
//	UNAUTHORIZED approval without a grant → ZERO filesystem delta, no PROVEN
//
// CREATE lives in create_target_integration_test.go. Continuation (multi-step)
// is pinned by the R4/R5 suites (TestR4_*, TestR5_*), which this matrix runs
// alongside rather than duplicating.

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/kernelbridge"
)

// TestAcceptance_ModifyReachesKernelAndProves is the MODIFY arm: an existing
// target is read, changed through the frozen kernel and verified independently.
func TestAcceptance_ModifyReachesKernelAndProves(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil || d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("run did not park at approval: term=%+v state=%s", term, d.State())
	}
	if calls := mock.calls(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	// Nothing applied before the human decision.
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("workspace changed before approval: %q", got)
	}

	term, err = d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want COMPLETED", term)
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("the approved modification did not land")
	}
	obs := kernelbridge.Observe(context.Background(), root, []string{"note.txt"})
	if !obs.Proven() || !obs.Exists("note.txt") {
		t.Fatalf("kernel did not prove the modified target present: %+v", obs)
	}
	if got := d.objectiveEvaluation().Outcome; got != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", got, d.objectiveEvaluation().Reason)
	}
}

// TestAcceptance_ReadOnlyMutatesNothing is the READ-ONLY arm: an investigation
// answers from the workspace and writes nothing.
func TestAcceptance_ReadOnlyMutatesNothing(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want COMPLETED", term)
	}
	if d.Boundary() != nil {
		t.Fatalf("a read-only objective parked at a mutation boundary: %+v", d.Boundary())
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("a read-only objective changed the workspace: %q", got)
	}
	if calls := mock.calls(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1 read-only invocation", calls)
	}
}

// TestAcceptance_AmbiguousTargetParksBeforeProvider is the AMBIGUITY arm: a
// targetless objective over several candidates must ask, not guess, and no
// provider may be billed.
func TestAcceptance_AmbiguousTargetParksBeforeProvider(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)
	_, probe, d := clarificationHarness(t, root)

	term, err := d.Run(context.Background(), ambiguousObjective)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("an ambiguous objective terminated instead of parking: %+v", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want AWAITING_HUMAN", d.State())
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryClarify {
		t.Fatalf("boundary = %+v, want a clarification", b)
	}
	if calls := probe.Calls(); calls != 0 {
		t.Fatalf("an ambiguous target billed %d provider call(s); discovery is evidence, not authority", calls)
	}
	if changed := ambiguityDiff(before, ambiguitySnapshot(t, root)); len(changed) != 0 {
		t.Fatalf("an ambiguous objective mutated %v", changed)
	}
}

// TestAcceptance_UnauthorizedApprovalLeavesZeroDelta is the AUTHORIZATION arm:
// an approval without a mutation grant is refused and changes nothing. It builds
// the real RuntimeExecutor WITHOUT the blanket authorization the other tests
// install, so the executor's own authorization boundary decides.
func TestAcceptance_UnauthorizedApprovalLeavesZeroDelta(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := execution.NewRuntimeExecutor(root, config.Default(), mock, bus, "")
	v := execution.NewVerifier(root)
	v.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	x.SetVerifier(v)
	// Deliberately NO SetAuthorization: no human released the mutate capability.
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want AWAITING_HUMAN", d.State())
	}

	// The human attempts to approve, but no authorization exists.
	term, err := d.ResumeApprove(context.Background())
	_ = err // the refusal may surface as an error or as a parked/aborted term
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatal("an unauthorized approval reported COMPLETED")
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("an unauthorized approval changed the workspace: %q", got)
	}
	if got := d.objectiveEvaluation().Outcome; got == execution.ObjectiveProven {
		t.Fatalf("an unauthorized approval reported PROVEN")
	}
	// The kernel's own observation confirms the target is unchanged.
	obs := kernelbridge.Observe(context.Background(), root, []string{"note.txt"})
	if !obs.Proven() || !obs.Exists("note.txt") {
		t.Fatalf("kernel evidence is inconsistent after an unauthorized approval: %+v", obs)
	}
}
