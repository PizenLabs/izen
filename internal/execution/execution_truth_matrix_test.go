package execution

// ── PHASE 2 — EXECUTION TRUTH MATRIX ────────────────────────────────────────
//
// These tests pin the execution-truth contract end to end through the
// RuntimeExecutor: the final result must correspond to actual filesystem state
// and verification state, never to a model claim or a parser success. Each case
// asserts the actual semantic result (outcome, evidence flags, verification
// report, usage account, event ordering) rather than absence of error.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── helpers ─────────────────────────────────────────────────────────────────

// testAuthorization returns a fresh mutation authorization for the executor.
func testAuthorization() *authorization.MutationAuthorization {
	return &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// failingVerifier is a verifier whose single mandatory step always fails —
// a real verifier failure, used to prove verification gates the apply.
func failingVerifier(root string) *Verifier {
	return &Verifier{
		root:  root,
		steps: []VerificationStep{{Name: "always-fail", Command: "false", Optional: false}},
	}
}

// ── 1. valid mutation ───────────────────────────────────────────────────────

func TestTruthMatrix_ValidMutation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: sampleReplace,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5},
	}}}
	x := phase4Executor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if got := mustRead(t, root, "note.txt"); got != "foo\nqux\nbaz\n" {
		t.Fatalf("filesystem did not change to the artifact: %q", got)
	}
	if apr.Proof.Outcome != OutcomeChanged {
		t.Fatalf("proof outcome = %s, want changed", apr.Proof.Outcome)
	}
	if len(apr.Proof.Mutations) != 1 {
		t.Fatalf("mutations = %d, want 1", len(apr.Proof.Mutations))
	}
	ev := apr.Proof.Mutations[0]
	if !ev.ApplyExecutedChanged() {
		t.Fatalf("evidence does not prove an executed filesystem mutation: %+v", ev)
	}
	if !ev.Verify() {
		t.Fatalf("evidence does not prove the verification gate passed: %+v", ev)
	}
	if !apr.Verification.Passed {
		t.Fatalf("proof verification = %+v, want passed", apr.Verification)
	}
	if !collector.waitCount(events.EventExecutionFinished, 1, time.Second) {
		t.Fatalf("execution.finished never emitted; types=%v", collector.types())
	}
}

// ── 2. no-change mutation ───────────────────────────────────────────────────

func TestTruthMatrix_NoChangeMutation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: sampleOriginal, // identical bytes — the model changed nothing
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("no-change apply must leave the file byte-identical: %q", got)
	}
	if apr.Proof.Outcome != OutcomeNoChange {
		t.Fatalf("proof outcome = %s, want nochange (identical bytes)", apr.Proof.Outcome)
	}
	if apr.Proof.Outcome.MutationSucceeded() {
		t.Fatal("nochange must never report a successful mutation")
	}
	ev := apr.Proof.Mutations[0]
	if !ev.ApplyExecuted {
		t.Fatalf("no-change apply DID execute — evidence must record it: %+v", ev)
	}
	if ev.FilesystemChanged {
		t.Fatalf("no-change apply must not report a filesystem change: %+v", ev)
	}
}

// ── 3. empty artifact ───────────────────────────────────────────────────────

func TestTruthMatrix_EmptyArtifactFails(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{Content: ""}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err == nil || res == nil || res.Err == nil {
		t.Fatalf("empty artifact must fail the execution: err=%v res.Err=%v", err, resErr(res))
	}
	if res.PendingPatchID != "" {
		t.Fatal("empty artifact must never reach the approval gate")
	}
	if res.Proof.Outcome.MutationSucceeded() || res.Proof.Outcome == OutcomeNoChange {
		t.Fatalf("empty artifact must never be a success: %s", res.Proof.Outcome)
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated on an empty artifact: %q", got)
	}
}

func resErr(res *ExecutionResult) error {
	if res == nil {
		return nil
	}
	return res.Err
}

// ── 4. malformed artifact ───────────────────────────────────────────────────

func TestTruthMatrix_MalformedArtifactRejected(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "main.go", "package main\n")

	// The model emits garbage that does not parse as a Go file.
	mock := &mockProvider{responses: []*ai.Response{{Content: "this is not go code at all"}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "add a function", Target: "main.go",
	})
	if err == nil || res == nil || res.Err == nil {
		t.Fatalf("malformed artifact must fail: err=%v resErr=%v", err, resErr(res))
	}
	if !errors.Is(res.Err, ErrArtifactRetryableRejected) {
		t.Fatalf("error = %v, want ErrArtifactRetryableRejected", res.Err)
	}
	if res.Proof.Outcome != OutcomeArtifactRetryableRejected {
		t.Fatalf("proof outcome = %s, want artifact_retryable_rejected", res.Proof.Outcome)
	}
	if res.PendingPatchID != "" {
		t.Fatal("malformed artifact must never reach the approval gate")
	}
	if got := mustRead(t, root, "main.go"); got != "package main\n" {
		t.Fatalf("file mutated on a rejected artifact: %q", got)
	}
}

// ── 5. truncated artifact ───────────────────────────────────────────────────

func TestTruthMatrix_TruncatedArtifactRejected(t *testing.T) {
	root := t.TempDir()

	// A new file whose "artifact" is a single line — the truncation guard
	// rejects it at the apply boundary (never written to disk).
	mock := &mockProvider{responses: []*ai.Response{{Content: "only one line\n"}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "create new.txt", Target: "new.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err == nil {
		t.Fatal("truncated artifact must fail the apply")
	}
	if _, statErr := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("truncated artifact must never be written: statErr=%v", statErr)
	}
}

// ── 6. ambiguous target ─────────────────────────────────────────────────────

func TestTruthMatrix_AmbiguousTargetStopsBeforeModel(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := phase4Executor(t, root, mock, nil)

	// A targeted mutation whose target set is empty: no model call, no
	// mutation, an explicit clarification stop.
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux",
		Strategy: targetedMutationProfile(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.ClarificationRequired {
		t.Fatal("empty target set must demand clarification")
	}
	if res.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("ambiguous target must never claim a mutation: %s", res.Proof.Outcome)
	}
	if mock.callCount != 0 {
		t.Fatalf("provider invoked %d times on an ambiguous target, want 0", mock.callCount)
	}
}

func targetedMutationProfile() *strategy.ExecutionStrategyProfile {
	return &strategy.ExecutionStrategyProfile{
		Strategy:       strategy.TargetedMutation,
		ModelRequired:  true,
		StrategyReason: "test",
	}
}

// ── 7. approval rejected ────────────────────────────────────────────────────

func TestTruthMatrix_ApprovalRejected(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := phase4Executor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rej, err := x.Reject(context.Background(), res.PendingPatchID, "no")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rej.Proof.Outcome != OutcomeRejected {
		t.Fatalf("proof outcome = %s, want rejected", rej.Proof.Outcome)
	}
	if rej.Proof.Outcome == OutcomeCancelled {
		t.Fatal("rejection must be distinct from a cancellation")
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated on a rejection: %q", got)
	}
	if !collector.waitCount(events.EventApprovalRejected, 1, time.Second) {
		t.Fatalf("approval.rejected never emitted; types=%v", collector.types())
	}
}

// ── 8. apply failure ────────────────────────────────────────────────────────

func TestTruthMatrix_ApplyFailure(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	// A short snippet for an existing file is rejected at the apply boundary.
	mock := &mockProvider{responses: []*ai.Response{{Content: "x"}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err == nil {
		t.Fatal("apply must fail")
	}
	if apr == nil || apr.Proof.Outcome != OutcomeApplyFailed {
		t.Fatalf("proof outcome = %v, want apply_failed", aprProofOutcome(apr))
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated on a failed apply: %q", got)
	}
}

func aprProofOutcome(apr *ExecutionResult) string {
	if apr == nil || apr.Proof == nil {
		return "<nil>"
	}
	return string(apr.Proof.Outcome)
}

// ── 9. verifier failure ─────────────────────────────────────────────────────

func TestTruthMatrix_VerifierFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	x := NewRuntimeExecutor(root, config.Default(), mock, bus, "")
	x.SetVerifier(failingVerifier(root))
	x.SetAuthorization(testAuthorization())

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err == nil {
		t.Fatal("a failing verification gate must fail the apply")
	}
	if apr == nil || apr.Proof.Outcome != OutcomeVerifyFailed {
		t.Fatalf("proof outcome = %v, want verify_failed", aprProofOutcome(apr))
	}
	if apr.Proof.Outcome.MutationSucceeded() {
		t.Fatal("verification failure must never report a successful mutation")
	}
	if apr.Verification.Passed {
		t.Fatal("proof verification must reflect the failed gate")
	}
	// The verification gate restored the shadow backup.
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file not restored after verification failure: %q", got)
	}
	if len(apr.Proof.Mutations) == 0 || apr.Proof.Mutations[0].Outcome != OutcomeVerifyFailed {
		t.Fatalf("evidence must record verify_failed: %+v", apr.Proof.Mutations)
	}
	// Verification ran and failed on the evidence — never fabricated.
	if !apr.Proof.Mutations[0].VerificationRun || apr.Proof.Mutations[0].VerificationPassed {
		t.Fatalf("evidence verification facts wrong: %+v", apr.Proof.Mutations[0])
	}
	if !collector.waitCount(events.EventVerificationCompleted, 1, time.Second) {
		t.Fatalf("verification.completed never emitted on the failed gate; types=%v", collector.types())
	}
}

// ── 10. provider failure ────────────────────────────────────────────────────

func TestTruthMatrix_ProviderFailure(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	x := phase4Executor(t, root, &failingProvider{}, nil)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err == nil || res == nil || res.Err == nil {
		t.Fatalf("provider failure must surface: err=%v resErr=%v", err, resErr(res))
	}
	if res.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("provider failure must never claim a mutation: %s", res.Proof.Outcome)
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated on a provider failure: %q", got)
	}
}

// ── 11/12. provider usage known / unknown ───────────────────────────────────

func TestTruthMatrix_UsageKnownVsUnknown(t *testing.T) {
	t.Run("known usage survives to the result", func(t *testing.T) {
		root := t.TempDir()
		writeTarget(t, root, "note.txt", sampleOriginal)
		mock := &mockProvider{responses: []*ai.Response{{
			Content: sampleReplace,
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 12, CompletionTokens: 6, CachedTokens: 2, ReasoningTokens: 3},
		}}}
		x := phase4Executor(t, root, mock, nil)
		res, err := x.Execute(context.Background(), ExecuteRequest{
			Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !res.Completed.Known {
			t.Fatal("usage account must be Known when the provider reported usage")
		}
		if res.Completed.InputTokens != 12 || res.Completed.OutputTokens != 6 {
			t.Fatalf("usage account = %d/%d, want 12/6", res.Completed.InputTokens, res.Completed.OutputTokens)
		}
		if res.Completed.CachedTokens != 2 || res.Completed.ReasoningTokens != 3 {
			t.Fatalf("cached/reasoning usage dropped: cached=%d reasoning=%d", res.Completed.CachedTokens, res.Completed.ReasoningTokens)
		}
		if len(res.Proof.ModelInvocations) != 1 || !res.Proof.ModelInvocations[0].Known {
			t.Fatalf("proof invocation must carry Known: %+v", res.Proof.ModelInvocations)
		}
	})

	t.Run("unknown usage is never a zero claim", func(t *testing.T) {
		root := t.TempDir()
		writeTarget(t, root, "note.txt", sampleOriginal)
		mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
		x := phase4Executor(t, root, mock, nil)
		res, err := x.Execute(context.Background(), ExecuteRequest{
			Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.Completed.Known {
			t.Fatal("usage account must be Unknown when the provider reported no usage")
		}
		if len(res.Proof.ModelInvocations) != 1 || res.Proof.ModelInvocations[0].Known {
			t.Fatalf("proof invocation must NOT claim known usage: %+v", res.Proof.ModelInvocations)
		}
	})
}

// ── 13. streaming usage ─────────────────────────────────────────────────────

func TestTruthMatrix_StreamingUsageSurvives(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.md", "old content\n")

	r := &streamingReader{
		data: "new content\n",
		usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     10,
			CompletionTokens: 5,
			ReasoningTokens:  2,
		},
	}
	prov := &streamingProvider{reader: r}
	x := phase4Executor(t, root, prov, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change note.md", Targets: []string{"note.md"},
		Strategy: executionStrategyProfile(t, "note.md"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Completed.Known {
		t.Fatal("streamed usage must be Known")
	}
	if res.Completed.InputTokens != 10 || res.Completed.OutputTokens != 5 {
		t.Fatalf("stream usage = %d/%d, want 10/5", res.Completed.InputTokens, res.Completed.OutputTokens)
	}
}

// ── 15/16. multi-file partial failure → rollback ────────────────────────────

func TestTruthMatrix_MultiFilePartialFailureRollsBackAll(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.txt": "first\naaa\n",
		"b.txt": "first\nbbb\n",
	})
	bus := events.NewBus(events.DefaultBufferSize)
	_ = newPhase4Collector(bus)
	x := phase4Executor(t, root, &emptySecondProvider{}, bus)
	g := NewIntentGateway(root)
	req, _, err := g.Gate(context.Background(), "$prompt change the first line in @a.txt and @b.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err == nil {
		t.Fatal("the second file's apply must fail")
	}
	if apr == nil || apr.Proof.Outcome != OutcomeApplyFailed {
		t.Fatalf("aggregate outcome = %v, want apply_failed (never changed after rollback)", aprProofOutcome(apr))
	}
	if apr.Proof.Outcome.MutationSucceeded() {
		t.Fatal("a rolled-back transaction must never report a mutation")
	}
	// Both files restored: no partial change survives.
	if got := mustRead(t, root, "a.txt"); got != "first\naaa\n" {
		t.Fatalf("a.txt partial change survived rollback: %q", got)
	}
	if got := mustRead(t, root, "b.txt"); got != "first\nbbb\n" {
		t.Fatalf("b.txt changed on a failed transaction: %q", got)
	}
	// The per-file evidence is corrected to the actual post-rollback state: a
	// file that applied and was rolled back is NOT changed.
	for _, ev := range apr.Proof.Mutations {
		if ev.FilesystemChanged {
			t.Fatalf("evidence claims filesystem change after rollback: %+v", ev)
		}
	}
}

// ── 17. execution cancellation ──────────────────────────────────────────────

func TestTruthMatrix_ExecutionCancelled(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	// cancelProvider returns the context error once the context is cancelled.
	prov := &contextObservingProvider{}
	x := phase4Executor(t, root, prov, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before invocation

	res, err := x.Execute(ctx, ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("cancellation must be a clean terminal outcome, err=%v", res.Err)
	}
	if res.Proof.Outcome != OutcomeCancelled {
		t.Fatalf("proof outcome = %s, want cancelled", res.Proof.Outcome)
	}
	if res.Proof.Outcome.MutationSucceeded() {
		t.Fatal("cancellation must never claim a mutation")
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated on a cancelled execution: %q", got)
	}
}

// contextObservingProvider fails with the context error on invocation.
type contextObservingProvider struct{}

func (p *contextObservingProvider) Name() string { return "ctx-mock" }

func (p *contextObservingProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("unexpected invoke on cancelled context")
}

func (p *contextObservingProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// ── 19. canonical event ordering on the approve path ────────────────────────

func TestTruthMatrix_CanonicalEventOrdering(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: sampleReplace,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5},
	}}}
	x := phase4Executor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !collector.waitCount(events.EventExecutionFinished, 1, time.Second) {
		t.Fatalf("execution.finished never emitted; types=%v", collector.types())
	}
	types := collector.types()
	idx := func(typ string) int {
		for i, k := range types {
			if k == typ {
				return i
			}
		}
		return -1
	}
	ms, mc, vc, ef := idx(events.EventMutationStarted), idx(events.EventMutationCompleted),
		idx(events.EventVerificationCompleted), idx(events.EventExecutionFinished)
	if ms < 0 || mc < 0 || vc < 0 || ef < 0 {
		t.Fatalf("missing lifecycle events: mutation.started=%d mutation.completed=%d verification.completed=%d execution.finished=%d; types=%v",
			ms, mc, vc, ef, types)
	}
	if ms >= mc || mc >= vc || vc >= ef {
		t.Fatalf("event ordering violated: mutation.started=%d mutation.completed=%d verification.completed=%d execution.finished=%d",
			ms, mc, vc, ef)
	}
}
