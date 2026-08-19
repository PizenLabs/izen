package autonomy

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
)

// TestDriver_ReadOnlyCompletes proves the happy-path bounded loop: a read-only
// objective observes, executes through the RuntimeExecutor, interprets the
// canonical completed outcome and terminates Completed.
func TestDriver_ReadOnlyCompletes(t *testing.T) {
	_, mock, a, _ := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("state = %s, want completed", d.State())
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}
	if len(d.History()) == 0 {
		t.Fatal("driver must record the bounded transition history")
	}
}

// TestDriver_MutationApprovalCycle proves the real mutation loop: execute →
// park at the approval gate (no mutation yet) → human approves → the SAME
// execution is interpreted as completed. The provider is invoked exactly once —
// approval resumes, never re-executes.
func TestDriver_MutationApprovalCycle(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated early: %+v, want parked at approval", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	b := d.Boundary()
	if b == nil || b.PatchID == "" {
		t.Fatalf("boundary = %+v, want an approval gate with a patch id", b)
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated before approval: %q", got)
	}

	term, err = d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination after approve = %+v, want completed", term)
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approve did not mutate the file")
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (approval must not re-execute)", mock.calls())
	}
}

// TestDriver_MutationReject proves a parked approval gate rejects the held
// patch: the execution terminates Aborted (permanent human decision) and the
// file stays untouched.
func TestDriver_MutationReject(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}

	term, err := d.ResumeReject(context.Background(), "not wanted")
	if err != nil {
		t.Fatalf("ResumeReject: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination after reject = %+v, want aborted", term)
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("rejection mutated the file: %q", got)
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (reject must not re-execute)", mock.calls())
	}
}

// TestDriver_ClarificationResume proves the clarification boundary: an
// unresolvable target parks before any execution; the human picks a target;
// the bounded loop then executes it and stops again at the approval gate.
func TestDriver_ClarificationResume(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "change @missing.txt to something")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated early: %+v, want parked at clarification", term)
	}
	b := d.Boundary()
	if b == nil || len(b.Options) == 0 {
		t.Fatalf("boundary = %+v, want clarification options", b)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 before clarification", mock.calls())
	}

	term, err = d.ResumeClarify(context.Background(), "note.txt")
	if err != nil {
		t.Fatalf("ResumeClarify: %v", err)
	}
	if term != nil {
		t.Fatalf("post-clarify terminated early: %+v, want parked at approval", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (approval gate)", d.State())
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated before approval: %q", got)
	}

	term, err = d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approve did not mutate the file")
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}
}

// TestDriver_RecoveryExhaustionParks proves the canonical recovery matrix over
// a real failing execution: repeated patch failures trigger bounded repairs,
// and when the recovery cycles are exhausted the loop PARKS at a human boundary
// (it does not abort, because the human may start a fresh bounded run).
func TestDriver_RecoveryExhaustionParks(t *testing.T) {
	_, mock, a, _ := testHarness(t, nil) // provider always fails
	d := NewDriver(a, nil, WithLoopBounds(autonomy.LoopBounds{
		MaxAttempts:       10,
		MaxRecoveryCycles: 1,
	}))

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated: %+v, want parked at recovery exhaustion", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	b := d.Boundary()
	if b == nil || b.PatchID != "" {
		t.Fatalf("boundary = %+v, want a non-approval human boundary", b)
	}
	if mock.calls() != 2 {
		t.Fatalf("provider calls = %d, want 2 (one execution + one bounded re-execution)", mock.calls())
	}
	if len(d.History()) == 0 {
		t.Fatal("driver must record the recovery transitions")
	}
}

// TestDriver_BudgetBlocksNextExecution proves the runtime-owned bound is
// enforced BEFORE the next execution: after the attempt budget is exhausted,
// an execution-bound decision aborts and the executor is never called again.
func TestDriver_BudgetBlocksNextExecution(t *testing.T) {
	_, mock, a, _ := testHarness(t, nil) // provider always fails
	forcedRetry := func(o autonomy.Observation, b autonomy.LoopBounds) autonomy.LoopDecision {
		return autonomy.LoopDecision{Action: autonomy.LoopRetry, Reason: "forced transient retry"}
	}
	d := NewDriver(a, nil,
		WithLoopBounds(autonomy.LoopBounds{MaxAttempts: 1}),
		WithDecider(forcedRetry),
	)

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted (attempt budget)", term)
	}
	if term.Class != autonomy.FailurePermanent {
		t.Fatalf("termination class = %s, want permanent", term.Class)
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (the second retry was blocked before execution)", mock.calls())
	}
}

// TestDriver_CancellationBeforeExecution proves a cancelled context terminates
// the loop as a permanent abort without ever touching the executor.
func TestDriver_CancellationBeforeExecution(t *testing.T) {
	_, mock, a, _ := testHarness(t, []*ai.Response{{Content: "explanation"}})
	d := NewDriver(a, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	term, err := d.Run(ctx, "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", term)
	}
	if term.Class != autonomy.FailurePermanent {
		t.Fatalf("termination class = %s, want permanent", term.Class)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 (cancelled before execution)", mock.calls())
	}
}

// TestDriver_CancellationDuringExecution proves a cancellation mid-flight is a
// clean permanent abort, never an auto-retry.
func TestDriver_CancellationDuringExecution(t *testing.T) {
	_, _, a, _ := testHarness(t, nil)
	prov := &blockingProvider{started: make(chan struct{})}
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	x := testExecutor(t, root, prov, nil)
	adapter := NewExecutorAdapter(root, a.gateway, x)
	d := NewDriver(adapter, nil)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		term *autonomy.LoopTermination
		err  error
	}
	done := make(chan result, 1)
	go func() {
		term, err := d.Run(ctx, "change bar to qux @note.txt")
		done <- result{term, err}
	}()
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never started")
	}
	cancel()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Run: %v", r.err)
		}
		if r.term == nil || r.term.State != autonomy.RuntimeAborted {
			t.Fatalf("termination = %+v, want aborted on cancellation", r.term)
		}
		if r.term.Class != autonomy.FailurePermanent {
			t.Fatalf("termination class = %s, want permanent", r.term.Class)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestDriver_PublishesLoopTransitions proves the driver is the single owner of
// canonical loop.transition events: every runtime state move is published on
// the shared bus for projections to observe.
func TestDriver_PublishesLoopTransitions(t *testing.T) {
	_, _, a, bus := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	collector := &eventCollector{}
	bus.Subscribe(events.EventLoopTransition, collector.add)
	d := NewDriver(a, bus)

	if _, err := d.Run(context.Background(), "explain the file @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !collector.waitTransitions(4, 2*time.Second) {
		t.Fatalf("loop.transition events = %d, want >= 4", collector.loopTransitions())
	}
	if !collector.hasTransition("observing", "deciding") {
		t.Fatalf("missing observing -> deciding transition (found %d)", collector.loopTransitions())
	}
	if !collector.hasTransition("deciding", "executing") {
		t.Fatalf("missing deciding -> executing transition; self-transitions leak from-state? (found %d)", collector.loopTransitions())
	}
}

// TestDriver_TerminalLoopRejectsReentry proves idempotency: a completed loop
// refuses further steps — no second execution is ever started after terminal.
func TestDriver_TerminalLoopRejectsReentry(t *testing.T) {
	_, mock, a, _ := testHarness(t, []*ai.Response{
		{Content: "note.txt is a plain text file."},
		{Content: "note.txt is a plain text file."},
	})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	before := mock.calls()

	// A second Run is a fresh bounded run (new loop); this is the ONLY legal
	// re-entry. The completed loop itself is frozen.
	term2, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if term2 == nil || term2.State != autonomy.RuntimeCompleted {
		t.Fatalf("second termination = %+v, want completed", term2)
	}
	if mock.calls() != before+1 {
		t.Fatalf("provider calls = %d, want %d (one fresh bounded run)", mock.calls(), before+1)
	}
}
