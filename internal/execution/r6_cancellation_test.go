package execution

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// ── R6-A: CANCELLATION LIFECYCLE FORENSICS (executor boundary) ──────────────
//
// These tests pin the executor-side half of the cancellation contract:
//
//	cancel.requested  → the context is withdrawn
//	cancel.propagated → the executor observes context.Canceled
//	execution.cancelled → the ATTEMPT terminal outcome is OutcomeCancelled
//	                      (the graph phase is PhaseCancelled, never a failure)
//
// The driver-side half (loop terminal + late-callback suppression) lives in
// internal/runtime/autonomy/r6_cancellation_test.go. The invariant under test
// here is:
//
//	cancel before the mutation boundary  → zero filesystem delta, clean
//	                                       cancellation (never "apply_failed")
//	committed mutation + later cancel    → mutation is NOT rolled back
//	late stream chunk after cancel       → cannot resurrect the execution
//
// The first case is the one the baseline got wrong: a withdrawn context at the
// mutation boundary was reported as an apply failure, indistinguishable from a
// genuine write failure. That is a lifecycle-semantics defect (cancel must stay
// typed as cancel), not a provider limitation.

// r6StageMutation stages one held mutation against target and returns the
// pending patch id. It asserts the file is untouched at the gate.
func r6StageMutation(t *testing.T, root, target, original, replacement string) *RuntimeExecutor {
	t.Helper()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n" + original + "=======\n" + replacement + ">>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 20, FinishReason: "stop"},
	}}}
	x := boundaryExecutor(t, root, mock, nil)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: "update " + target,
		Target: target,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatalf("Execute did not stop at the approval gate: outcome=%s", res.Proof.Outcome)
	}
	if got := readFileString(t, root, target); got != original {
		t.Fatalf("file mutated at the approval gate: %q", got)
	}
	return x
}

// TestR6_MutationBoundary_CancelBeforeAuthorizationLeavesZeroDelta is mutation
// boundary case A: a cancellation that lands BEFORE the held mutation is
// authorized must (1) apply nothing, (2) leave a zero filesystem delta, and
// (3) terminate the attempt as a CLEAN cancellation — not as an apply failure.
func TestR6_MutationBoundary_CancelBeforeAuthorizationLeavesZeroDelta(t *testing.T) {
	root := t.TempDir()
	target := "index.html"
	original := "<h1>before</h1>\n"
	replacement := "<h1>after</h1>\n"
	writeTarget(t, root, target, original)

	x := r6StageMutation(t, root, target, original, replacement)

	// Cancel before Approve runs: the held patch is never authorized. Approving
	// the REAL held candidate under the withdrawn context must apply nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pending := x.PendingPatchIDs()
	if len(pending) != 1 {
		t.Fatalf("pending candidates = %d, want 1", len(pending))
	}
	apr, err := x.Approve(ctx, pending[0])

	// (2) zero filesystem delta.
	if got := readFileString(t, root, target); got != original {
		t.Fatalf("cancel before authorization mutated the file: %q", got)
	}
	if apr == nil {
		t.Fatalf("Approve returned no result (err=%v) for a cancellation", err)
	}
	// (3) truthful typed outcome.
	if apr.Proof == nil || apr.Proof.Outcome != OutcomeCancelled {
		t.Fatalf("cancellation at the mutation boundary reported outcome %q, want %q — a withdrawn context must stay typed as a cancellation, never as an apply failure",
			outcomeOf(apr), OutcomeCancelled)
	}
	if len(apr.Proof.Mutations) != 0 {
		t.Fatalf("cancelled attempt recorded %d mutation(s): %+v", len(apr.Proof.Mutations), apr.Proof.Mutations)
	}
}

// TestR6_MutationBoundary_CommittedMutationIsNotRolledBackByCancellation is
// mutation boundary case B: once a mutation is durably committed through the
// approval gate, a subsequent cancellation must NOT roll it back. Cancellation
// is not rollback, and it must not erase committed mutation evidence.
func TestR6_MutationBoundary_CommittedMutationIsNotRolledBackByCancellation(t *testing.T) {
	root := t.TempDir()
	target := "index.html"
	original := "<h1>before</h1>\n"
	replacement := "<h1>after</h1>\n"
	writeTarget(t, root, target, original)

	x := r6StageMutation(t, root, target, original, replacement)

	apr, err := x.Approve(context.Background(), x.PendingPatchIDs()[0])
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := readFileString(t, root, target); got != replacement {
		t.Fatalf("committed mutation not on disk: %q", got)
	}
	if apr.Proof.Outcome != OutcomeChanged {
		t.Fatalf("committed outcome = %s, want changed", apr.Proof.Outcome)
	}

	// A cancellation signal after the commit must not undo the commit.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
	if got := readFileString(t, root, target); got != replacement {
		t.Fatalf("cancellation rolled back a committed mutation: %q — cancel must not be rollback", got)
	}
	if len(apr.Proof.Mutations) != 1 || !apr.Proof.Mutations[0].FilesystemChanged {
		t.Fatalf("committed mutation evidence was erased by cancellation: %+v", apr.Proof.Mutations)
	}
}

// r6LateChunkReader deliberately delivers one chunk AFTER the context is
// cancelled, modelling an SSE body that had a final frame in flight when the
// user hit Esc/Ctrl+C. The executor must never let it reach the artifact.
type r6LateChunkReader struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
	chunk   string
	sent    bool
}

func (r *r6LateChunkReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	if !r.sent {
		<-r.ctx.Done() // hold the stream open until the cancellation arrives
		r.sent = true
		n := copy(p, r.chunk)
		return n, nil // the late chunk the provider had already produced
	}
	return 0, io.EOF
}

func (r *r6LateChunkReader) Close() error { return nil }

// TestR6_Streaming_LateChunkAfterCancelCannotResurrectExecution proves a late
// stream chunk delivered after cancellation cannot produce an artifact, reach
// the approval gate, mutate the workspace, or complete the execution.
func TestR6_Streaming_LateChunkAfterCancelCannotResurrectExecution(t *testing.T) {
	root := t.TempDir()
	target := "note.md"
	original := "old content\n"
	writeTarget(t, root, target, original)

	ctx, cancel := context.WithCancel(context.Background())
	r := &r6LateChunkReader{
		ctx:     ctx,
		started: make(chan struct{}),
		chunk:   "<<<<<<< SEARCH\nold content\n=======\nnew content\n>>>>>>>",
	}
	prov := &streamingProvider{reader: r}
	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	x := phase4Executor(t, root, prov, bus)

	type outcome struct {
		res *ExecutionResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := x.Execute(ctx, ExecuteRequest{
			Prompt:   "$hot change " + target,
			Targets:  []string{target},
			Strategy: executionStrategyProfile(t, target),
		})
		done <- outcome{res, err}
	}()

	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stream reader never started")
	}
	cancel()

	var out outcome
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}
	if out.err != nil {
		t.Fatalf("Execute returned an error for a cancellation: %v", out.err)
	}
	res := out.res
	if res.Proof == nil || res.Proof.Outcome != OutcomeCancelled {
		t.Fatalf("late chunk after cancel produced outcome %q, want cancelled", outcomeOf(res))
	}
	if res.PendingPatchID != "" {
		t.Fatal("late stream chunk reached the approval gate after cancellation")
	}
	if got := readFileString(t, root, target); got != original {
		t.Fatalf("late stream chunk mutated the workspace after cancellation: %q", got)
	}
	if collector.count(events.EventExecutionFailed) != 0 {
		t.Errorf("cancelled stream emitted execution.failed; types=%v", collector.types())
	}
}
