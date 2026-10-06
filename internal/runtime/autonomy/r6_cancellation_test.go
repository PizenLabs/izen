package autonomy

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── R6-A: CANCELLATION LIFECYCLE FORENSICS (driver/loop boundary) ───────────
//
// These tests pin the driver-side half of the cancellation contract:
//
//	RUNNING
//	  → cancel.requested (the run context is withdrawn)
//	  → cancel.propagated (the loop's inter-step gate and RuntimeLoop.Step observe it)
//	  → execution cancelled (terminal RuntimeAborted, reason "context cancelled")
//
// The single authoritative cancellation ordering is: the driver observes
// ctx.Done() at ONE of two linearization points — the inter-step select at the
// top of the loop, or RuntimeLoop.Step. Whichever runs first wins. A late
// provider or capability result that arrives after cancellation is consumed
// into an already-terminal (or about-to-terminate) loop and can never become
// PROVEN.
//
// The loop has no distinct RuntimeCancelled position; cancellation terminates
// at RuntimeAborted/FailurePermanent with an explicit reason. That is the
// recorded observability gap (see docs/report/R6_CANCELLATION_REPORT.md), NOT
// a resurrection path: the abort is terminal and the completion authority never
// runs after it.

// lateResultProvider deliberately ignores ctx cancellation and returns a
// SUCCESSFUL result once released. It models a transport/daemon that had a
// result in flight when the human cancelled.
type lateResultProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	content string
}

func (p *lateResultProvider) Name() string { return "late-result" }

func (p *lateResultProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return &ai.Response{
		Content: p.content,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5, FinishReason: "stop"},
	}, nil
}

func (p *lateResultProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return &lateResultReader{content: p.content}, nil
}

type lateResultReader struct {
	content string
	sent    bool
}

func (r *lateResultReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(p, r.content), nil
}

func (r *lateResultReader) Close() error { return nil }

// r6RunResult is the outcome of a driver run executed on a goroutine.
type r6RunResult struct {
	term *autonomy.LoopTermination
	err  error
}

func r6RunAsync(d *Driver, ctx context.Context, objective string) chan r6RunResult {
	done := make(chan r6RunResult, 1)
	go func() {
		term, err := d.Run(ctx, objective)
		done <- r6RunResult{term, err}
	}()
	return done
}

// TestR6_CancelWhileProviderActiveAbortsAndNeverMutates proves the base
// lifecycle: a cancellation while the provider is in flight terminates the loop
// as a permanent abort, emits no success artifact, and leaves a zero-delta
// workspace.
func TestR6_CancelWhileProviderActiveAbortsAndNeverMutates(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	finished := make(chan events.ExecutionFinishedPayload, 4)
	bus.Subscribe(events.EventExecutionFinished, func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.ExecutionFinishedPayload); ok {
			finished <- p
		}
	})

	prov := &blockingProvider{started: make(chan struct{})}
	x := testExecutor(t, root, prov, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := r6RunAsync(d, ctx, "change bar to qux @note.txt")
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never started")
	}
	cancel()

	var r r6RunResult
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if r.term == nil || r.term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", r.term)
	}
	if r.term.Class != autonomy.FailurePermanent {
		t.Fatalf("termination class = %s, want permanent", r.term.Class)
	}
	if d.State() != autonomy.RuntimeAborted {
		t.Fatalf("state = %s, want aborted", d.State())
	}
	// No mutation, no held candidate.
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("cancelled run mutated the workspace: %q", got)
	}
	if held := x.PendingPatchIDs(); len(held) != 0 {
		t.Fatalf("cancelled run left a pending mutation candidate: %v", held)
	}
	// The attempt-level terminal record must be the typed cancellation, never
	// a success. `execution.finished(success=false, outcome="cancelled")` is
	// IZEN's existing representation of `execution.cancelled`.
	select {
	case p := <-finished:
		if p.Success {
			t.Fatalf("cancelled attempt emitted execution.finished success=true (%+v)", p)
		}
		if p.Outcome != string(execution.OutcomeCancelled) {
			t.Fatalf("execution.finished outcome = %q, want %q", p.Outcome, execution.OutcomeCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no execution.finished event observed for the cancellation")
	}
}

// TestR6_CancelBeforeExecutionNeverTouchesExecutor proves a cancellation that
// lands before the first dispatch consumes zero provider calls.
func TestR6_CancelBeforeExecutionNeverTouchesExecutor(t *testing.T) {
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
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 (cancelled before execution)", mock.calls())
	}
}

// TestR6_LateProviderSuccessAfterCancelCannotComplete is the required negative
// test: a provider that ignores cancellation and returns a SUCCESS after the
// run was cancelled must not transition the execution to PROVEN / completed.
func TestR6_LateProviderSuccessAfterCancelCannotComplete(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	prov := &lateResultProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		content: "note.txt is a plain text file.",
	}
	x := testExecutor(t, root, prov, nil)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := r6RunAsync(d, ctx, "explain the file @note.txt")
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never started")
	}
	cancel()
	// Release the hostile provider's late SUCCESS. The run is already cancelled.
	close(prov.release)

	var r r6RunResult
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the late provider result")
	}
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if r.term == nil || r.term.State != autonomy.RuntimeAborted {
		t.Fatalf("late provider success terminated the run at %+v, want aborted (never completed)", r.term)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("late provider success resurrected the cancelled run into completed")
	}
}

// TestR6_LateMutationResultCannotBeAppliedAfterCancel proves a late MUTATING
// result (a provider that ignores cancellation and returns a patch) cannot be
// applied after the run is cancelled: the loop aborts before the approval gate
// is ever resumed, and the approval resume path refuses because the loop is no
// longer parked.
func TestR6_LateMutationResultCannotBeAppliedAfterCancel(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	prov := &lateResultProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		content: sampleReplace,
	}
	x := testExecutor(t, root, prov, nil)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := r6RunAsync(d, ctx, "change bar to qux @note.txt")
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never started")
	}
	cancel()
	close(prov.release)

	var r r6RunResult
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the late provider result")
	}
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if r.term == nil || r.term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", r.term)
	}
	// Even if the executor held the late candidate, the terminal loop refuses
	// to resume it: there is no executable artifact to authorize.
	if _, err := d.ResumeApprove(context.Background()); err == nil {
		t.Fatal("ResumeApprove succeeded on a cancelled (terminal) run")
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("late mutation result mutated the workspace after cancellation: %q", got)
	}
}

// TestR6_resultProvider returns one successful response and is safe to call
// twice (intra-loop continuation tests).
type r6ResultProvider struct {
	mu      sync.Mutex
	calls   int
	content string
}

func (p *r6ResultProvider) Name() string { return "r6-result" }
func (p *r6ResultProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &ai.Response{
		Content: p.content,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5, FinishReason: "stop"},
	}, nil
}
func (p *r6ResultProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &lateResultReader{content: p.content}, nil
}
func (p *r6ResultProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt proves Invariant 6:
// a selected continuation (a next execution-bound decision) cannot start after
// cancellation. The decider selects LoopContinue at the interpretation boundary
// and withdraws the context in the same step; RuntimeLoop.Step observes the
// withdrawal and terminates before the next attempt is dispatched.
func TestR6_CancelAtContinuationBoundaryDoesNotStartNextAttempt(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	ctx, cancel := context.WithCancel(context.Background())
	prov := &r6ResultProvider{content: "note.txt is a plain text file."}
	x := testExecutor(t, root, prov, nil)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	var decisions int32
	decider := func(o autonomy.Observation, b autonomy.LoopBounds) autonomy.LoopDecision {
		if atomic.AddInt32(&decisions, 1) >= 2 {
			// The continuation is selected; the human cancels in the same
			// step. It must never be dispatched.
			cancel()
			return autonomy.LoopDecision{Action: autonomy.LoopContinue, Reason: "selected continuation"}
		}
		return autonomy.LoopDecision{Action: autonomy.LoopContinue, Reason: "execute"}
	}
	d := NewDriver(adapter, nil, WithDecider(decider))

	term, err := d.Run(ctx, "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted (continuation suppressed by cancel)", term)
	}
	if got := prov.callCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 — the selected continuation started after cancellation", got)
	}
}

// TestR6_CompletionCancelRaceIsDeterministic proves Invariant 7: when a
// completion decision and a cancellation race at the SAME decision boundary,
// cancellation observed by RuntimeLoop.Step wins and the terminal state is
// Aborted, never Completed. There is exactly one authoritative ordering.
func TestR6_CompletionCancelRaceIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	ctx, cancel := context.WithCancel(context.Background())
	prov := &r6ResultProvider{content: "note.txt is a plain text file."}
	x := testExecutor(t, root, prov, nil)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	decider := func(o autonomy.Observation, b autonomy.LoopBounds) autonomy.LoopDecision {
		// Propose completion and withdraw the context in the same step.
		cancel()
		return autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "race: completion proposed at cancel"}
	}
	d := NewDriver(adapter, nil, WithDecider(decider))

	term, err := d.Run(ctx, "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted (cancellation wins the race)", term)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("completion/cancel race produced BOTH completed and cancelled")
	}
}

// TestR6_TerminalCompletionIsImmutable proves the converse ordering: once a run
// has legitimately completed, a later cancellation cannot revoke or roll it
// back. Terminal state is frozen.
func TestR6_TerminalCompletionIsImmutable(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}

	// A cancellation after terminal completion must be a no-op.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
	if d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("post-completion cancellation changed the terminal state to %s", d.State())
	}
	if _, err := d.Abort("post-completion cancel"); err != nil {
		t.Fatalf("Abort after completion returned an error: %v", err)
	}
	if d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("Abort changed a completed run to %s", d.State())
	}
	_ = root
}

// TestR6_CommittedMutationSurvivesCancellation proves Invariant 4 at the driver
// boundary: once an approved mutation is committed, a later cancellation is not
// a rollback. The workspace keeps the committed bytes and the run does not
// revert it.
func TestR6_CommittedMutationSurvivesCancellation(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated before approval: %q", got)
	}
	term, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination after approve = %+v, want completed", term)
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approved mutation did not commit")
	}

	// Cancel after the commit: the mutation must remain on disk.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
	_, _ = d.Abort("cancel after committed mutation")
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("cancellation rolled back a committed mutation")
	}
}
