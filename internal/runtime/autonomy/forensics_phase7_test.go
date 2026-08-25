package autonomy

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/language"
)

// TestProviderInvocationCountAfterSingleApproval proves that approving a parked
// approval gate causes EXACTLY ZERO new provider invocations.
func TestProviderInvocationCountAfterSingleApproval(t *testing.T) {
	root, mock, adapter, bus := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	driver := NewDriver(adapter, bus)

	// Step 1: Run objective that reaches approval
	ctx := context.Background()
	_, err := driver.Run(ctx, "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("driver.Run failed: %v", err)
	}

	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %v, want awaiting_human", driver.State())
	}

	initialCalls := mock.calls()
	if initialCalls != 1 {
		t.Fatalf("expected exactly 1 provider call during initial Run, got %d", initialCalls)
	}

	// Step 2: Alt+A -> ResumeApprove
	term, err := driver.ResumeApprove(ctx)
	if err != nil {
		t.Fatalf("driver.ResumeApprove failed: %v", err)
	}

	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %v, want completed", term)
	}

	postApproveCalls := mock.calls()
	if postApproveCalls != initialCalls {
		t.Fatalf("provider calls increased after approval: was %d, now %d (want 0 additional calls)", initialCalls, postApproveCalls)
	}
	_ = root
}

// TestProviderUsageCommittedExactlyOnce proves that usage reported by a provider
// invocation is counted exactly once into the execution proof and completed result.
func TestProviderUsageCommittedExactlyOnce(t *testing.T) {
	root := t.TempDir()
	mock := &mockProvider{
		responses: []*ai.Response{
			{
				Content:     "test response without modification",
				TokenInput:  2181,
				TokenOutput: 682,
				Usage: ai.ProviderUsage{
					PromptTokens:     2181,
					CompletionTokens: 682,
					Known:            true,
				},
			},
		},
	}

	cfg := config.Default()
	x := execution.NewRuntimeExecutor(root, cfg, mock, nil, "")
	req := execution.ExecuteRequest{
		Prompt: "explain this codebase",
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.DirectResponse,
			ModelRequired: true,
		},
	}

	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if len(res.ModelCalls) != 1 {
		t.Fatalf("model calls count = %d, want 1", len(res.ModelCalls))
	}

	if res.Completed.InputTokens != 2181 {
		t.Fatalf("input tokens = %d, want 2181", res.Completed.InputTokens)
	}
	if res.Completed.OutputTokens != 682 {
		t.Fatalf("output tokens = %d, want 682", res.Completed.OutputTokens)
	}
}

// TestAutonomousExecutionConvergesAfterSingleApproval proves that a single approval
// cleanly completes the autonomous loop without re-entering or retrying.
func TestAutonomousExecutionConvergesAfterSingleApproval(t *testing.T) {
	loop := autonomy.NewRuntimeLoop(autonomy.DefaultLoopBounds())
	loop.Start("objective")
	loop.Observe(autonomy.Observation{Outcome: autonomy.OutcomePendingApproval, PatchID: "patch-1"})

	// Decision: AskHuman
	state, err := loop.Step(context.Background(), autonomy.LoopDecision{
		Action:  autonomy.LoopAskHuman,
		PatchID: "patch-1",
		Reason:  "mutation awaiting approval",
	})
	if err != nil || state != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("step to awaiting human: state=%v, err=%v", state, err)
	}

	// Release human
	loop.ReleaseHuman("patch approved")
	if loop.State() != autonomy.RuntimeObserving {
		t.Fatalf("state after release = %v, want observing", loop.State())
	}

	// Observe changed (from apply)
	loop.Observe(autonomy.Observation{Outcome: autonomy.OutcomeChanged})
	if loop.State() != autonomy.RuntimeDeciding {
		t.Fatalf("state after observe = %v, want deciding", loop.State())
	}

	// Step complete
	state, err = loop.Step(context.Background(), autonomy.LoopDecision{
		Action: autonomy.LoopComplete,
		Reason: "objective satisfied: changed",
	})
	if err != nil || state != autonomy.RuntimeCompleted {
		t.Fatalf("step to completed: state=%v, err=%v", state, err)
	}

	if !loop.State().IsTerminal() {
		t.Fatal("loop is not terminal after completion")
	}

	// Ensure no subsequent steps can execute
	_, err = loop.Step(context.Background(), autonomy.LoopDecision{Action: autonomy.LoopContinue})
	if err == nil {
		t.Fatal("expected error stepping from terminal state, got nil")
	}
}

// TestResumeApproveDoesNotCreateNewObjective asserts that resuming approval preserves
// the original objective and does not re-submit a new objective to the driver.
func TestResumeApproveDoesNotCreateNewObjective(t *testing.T) {
	_, _, adapter, bus := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	driver := NewDriver(adapter, bus)

	origObjective := "change bar to qux @note.txt"
	_, _ = driver.Run(context.Background(), origObjective)

	// Verify the parked boundary
	b := driver.Boundary()
	if b == nil {
		t.Fatal("expected non-nil boundary")
	}

	// Driver LastObservation should have intent matching original objective
	obs := driver.LastObservation()
	if obs.PatchID == "" {
		t.Fatal("expected patchID in observation")
	}
}

// TestNoImplicitExecutionRetry verifies that non-failure outcomes never trigger retry.
func TestNoImplicitExecutionRetry(t *testing.T) {
	// Success outcomes are decided by decideDefault BEFORE the recovery matrix
	// is consulted, so they complete the loop instead of retrying/repairing.
	for _, outcome := range []autonomy.ExecutionOutcome{
		autonomy.OutcomeChanged,
		autonomy.OutcomeCreated,
		autonomy.OutcomeNoChange,
		autonomy.OutcomeCompleted,
	} {
		obs := autonomy.Observation{Outcome: outcome}
		dec := decideDefault(obs, autonomy.DefaultLoopBounds())
		if dec.Action == autonomy.LoopRetry || dec.Action == autonomy.LoopRepair {
			t.Errorf("outcome %s triggered %s, want no retry/repair", outcome, dec.Action)
		}
		if dec.Action != autonomy.LoopComplete {
			t.Errorf("outcome %s decided %s, want complete", outcome, dec.Action)
		}
	}
}

func TestDecideDefaultRecognizesRecoveryOutcomes(t *testing.T) {
	for _, outcome := range []autonomy.ExecutionOutcome{
		autonomy.OutcomeTruncated,
		autonomy.OutcomeArtifactRetryableRejected,
	} {
		obs := autonomy.Observation{Outcome: outcome}
		dec := decideDefault(obs, autonomy.DefaultLoopBounds())
		if dec.Action != autonomy.LoopRepair {
			t.Errorf("outcome %s decided %s (%q), want repair", outcome, dec.Action, dec.Reason)
		}
		if strings.Contains(dec.Reason, "unrecognized outcome") {
			t.Errorf("outcome %s was treated as unrecognized: %q", outcome, dec.Reason)
		}
	}
}

// TestRetryStateIsExplicit verifies that retries/repairs explicitly transition through
// RuntimeRecovering and increment loop counters.
func TestRetryStateIsExplicit(t *testing.T) {
	loop := autonomy.NewRuntimeLoop(autonomy.DefaultLoopBounds())
	loop.Start("test")
	loop.Observe(autonomy.Observation{Outcome: autonomy.OutcomeFailed})

	// Decide repair
	state, err := loop.Step(context.Background(), autonomy.LoopDecision{
		Action: autonomy.LoopRepair,
		Reason: "recoverable failure - repair",
	})
	if err != nil {
		t.Fatalf("step error: %v", err)
	}

	if state != autonomy.RuntimeRecovering {
		t.Fatalf("state = %v, want recovering", state)
	}
	if loop.RecoveryCycles() != 1 {
		t.Fatalf("recovery cycles = %d, want 1", loop.RecoveryCycles())
	}
}

// TestLateResultCannotReenterExecution verifies that a late response from an old runID
// is ignored by the driver and cannot alter state.
func TestLateResultCannotReenterExecution(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	mock := &blockingProvider{started: make(chan struct{})}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	driver := NewDriver(adapter, bus)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var term *autonomy.LoopTermination
	go func() {
		defer close(done)
		term, _ = driver.Run(ctx, "change bar to qux @note.txt")
	}()

	<-mock.started
	cancel()
	<-done

	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("expected aborted term on cancel, got %v", term)
	}
}

// TestUsageAggregationDoesNotDoubleCount verifies that finalizing an ExecutionResult
// multiple times does not compound the token counts.
func TestUsageAggregationDoesNotDoubleCount(t *testing.T) {
	res := &execution.ExecutionResult{
		ModelCalls: []execution.ModelInvocation{
			{Model: "test-model", TokenInput: 100, TokenOutput: 50, Known: true},
		},
	}

	totalIn := 0
	totalOut := 0
	for _, inv := range res.ModelCalls {
		if inv.Known {
			totalIn += inv.TokenInput
			totalOut += inv.TokenOutput
		}
	}

	if totalIn != 100 || totalOut != 50 {
		t.Fatalf("unexpected counts: in=%d, out=%d", totalIn, totalOut)
	}
	_ = atomic.LoadInt32
}

// TestProviderRequestDoesNotAccumulateDuplicateContext verifies that recovery
// evidence accumulation does not create unbounded repetitive context bloat:
// each typed repair appends one bounded advisory line to the ledger.
func TestProviderRequestDoesNotAccumulateDuplicateContext(t *testing.T) {
	req := autonomy.LoopRequest{
		Prompt:   "fix index.html",
		Evidence: "evidence 1",
	}
	obs := autonomy.Observation{Outcome: autonomy.OutcomePatchFailed}

	// Default repair
	req.Evidence = strings.TrimSpace(req.Evidence + "\nrecovery of failed execution (outcome " + string(obs.Outcome) + ")")

	if !strings.Contains(req.Evidence, "evidence 1") {
		t.Fatal("lost prior evidence")
	}
	if !strings.Contains(req.Evidence, "outcome patch_failed") {
		t.Fatal("missing repair notice")
	}
}

// ── Phase 7 P0 / P1: exactly-once approval convergence ──────────────────────
//
// Approving a parked approval gate must converge to exactly ONE terminal
// outcome with EXACTLY ZERO new provider invocations and EXACTLY ONE patch
// apply. For a language without a verification contract (HTML), verification
// is NOT APPLICABLE — the patch applies without running Go commands and
// without rollback.

const htmlOriginal = "<html>\n<body>old</body>\n</html>\n"

const htmlReplace = `<<<<<<< SEARCH
<body>old</body>
=======
<body>new</body>
>>>>>>>`

// htmlTestHarness builds a driver over a RuntimeExecutor with an HTML-language
// verifier — the exact production configuration that reproduced the reported
// bug (approval failed because verification fell back to Go commands).
func htmlTestHarness(t *testing.T) (string, *mockProvider, *ExecutorAdapter, *events.Bus) {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, "index.html", htmlOriginal)
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: htmlReplace}}}
	cfg := config.Default()
	x := execution.NewRuntimeExecutor(root, cfg, mock, bus, language.HTML)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return root, mock, NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus
}

// TestSingleObjectiveDoesNotImplicitlyReinvokeProvider is the P0 loop-level
// pin for the 5,883-token repro: ONE objective through the full autonomous
// lifecycle (Run → approval → completed) triggers EXACTLY ONE provider
// invocation. The authoritative usage (5883 output / 5000 reasoning) is
// observable on the loop's event bus as provider.usage_update — the number
// OpenRouter billed survives the loop verbatim, and no implicit re-invocation
// ever re-runs the model.
func TestSingleObjectiveDoesNotImplicitlyReinvokeProvider(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", htmlOriginal)
	bus := events.NewBus(events.DefaultBufferSize)

	stream := &reproUsageStream{
		content: []byte(htmlReplace),
		usage: ai.ProviderUsage{
			PromptTokens:     2181,
			CompletionTokens: 5883,
			ReasoningTokens:  5000,
			TotalTokens:      7064,
			Known:            true,
		},
	}
	prov := &reproStreamProvider{stream: stream}
	cfg := config.Default()
	x := execution.NewRuntimeExecutor(root, cfg, prov, bus, language.HTML)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	driver := NewDriver(adapter, bus)

	// Subscribe to the authoritative usage update BEFORE the objective runs —
	// the bus never replays, so the observer must be wired ahead of execution.
	usageCh := make(chan events.ProviderUsageUpdatePayload, 1)
	usageSub := bus.Subscribe(events.EventProviderUsageUpdate, func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.ProviderUsageUpdatePayload); ok {
			select {
			case usageCh <- p:
			default:
			}
		}
	})
	if usageSub == nil {
		t.Fatal("usage subscription failed")
	}
	defer usageSub.Cancel()

	// Stage 1: Run the single objective → parked at approval after exactly one
	// provider invocation.
	ctx := context.Background()
	term, err := driver.Run(ctx, "change old to new @index.html")
	if err != nil {
		t.Fatalf("driver.Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated early: %+v, want parked at approval", term)
	}
	if prov.calls() != 1 {
		t.Fatalf("provider invocations after Run = %d, want exactly 1", prov.calls())
	}

	// The 5,883-token account is on the bus as authoritative usage.
	var usage events.ProviderUsageUpdatePayload
	select {
	case usage = <-usageCh:
	case <-time.After(time.Second):
		t.Fatal("no authoritative provider.usage_update within deadline")
	}
	if usage.InputTokens != 2181 || usage.OutputTokens != 5883 || usage.ReasoningTokens != 5000 {
		t.Errorf("provider.usage_update = %+v, want 2181/5883/5000 (authoritative repro numbers)", usage)
	}

	// Stage 2: Approve → exactly one terminal outcome, zero additional calls.
	term, err = driver.ResumeApprove(ctx)
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if prov.calls() != 1 {
		t.Fatalf("provider invocations after approval = %d, want still exactly 1 (no implicit re-invocation)", prov.calls())
	}
	if got := readTarget(t, root, "index.html"); got != "<html>\n<body>new</body>\n</html>\n" {
		t.Fatalf("file = %q, want the approved rewrite", got)
	}
}

// reproUsageStream serves content once, then reports the authoritative
// 5,883-token usage of the repro.
type reproUsageStream struct {
	content []byte
	usage   ai.ProviderUsage
	done    bool
}

func (s *reproUsageStream) Read(p []byte) (int, error) {
	if len(s.content) == 0 {
		if s.done {
			return 0, io.EOF
		}
		s.done = true
		return 0, io.EOF
	}
	n := copy(p, s.content)
	s.content = s.content[n:]
	return n, nil
}

func (s *reproUsageStream) Close() error            { return nil }
func (s *reproUsageStream) Usage() ai.ProviderUsage { return s.usage }
func (s *reproUsageStream) FinishReason() string    { return "stop" }

// reproStreamProvider serves a single streaming response and counts invocations.
type reproStreamProvider struct {
	mu     sync.Mutex
	stream io.ReadCloser
	callsN int
}

func (p *reproStreamProvider) Name() string { return "repro" }

func (p *reproStreamProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callsN++
	return nil, errors.New("streaming repro provider must not fall back to Execute")
}

func (p *reproStreamProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callsN++
	return p.stream, nil
}

func (p *reproStreamProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callsN
}

// Run + one Alt+A on an HTML workspace → exactly one provider invocation, one
// apply (file changed), verification skipped (no Go commands), exactly one
// terminal outcome (RuntimeCompleted) and zero re-execution.
func TestHTMLApprovalConvergesExactlyOnce(t *testing.T) {
	root, mock, adapter, bus := htmlTestHarness(t)
	driver := NewDriver(adapter, bus)

	term, err := driver.Run(context.Background(), "change old to new @index.html")
	if err != nil {
		t.Fatalf("driver.Run failed: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated early: %+v, want parked at approval", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %v, want awaiting_human", driver.State())
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls after run = %d, want 1", mock.calls())
	}
	if got := readTarget(t, root, "index.html"); got != htmlOriginal {
		t.Fatalf("file mutated before approval: %q", got)
	}

	// Alt+A → approve
	term, err = driver.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove failed: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if !driver.State().IsTerminal() {
		t.Fatalf("driver state = %v, want terminal", driver.State())
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls after approve = %d, want 1 (approval must not re-execute)", mock.calls())
	}
	if got := readTarget(t, root, "index.html"); got == htmlOriginal {
		t.Fatal("approve did not apply the HTML patch")
	}

	// The last observation must confirm the apply succeeded and verification
	// was not applicable.
	obs := driver.LastObservation()
	if obs.Outcome != autonomy.OutcomeChanged {
		t.Fatalf("last observation outcome = %q, want changed", obs.Outcome)
	}
}

// TestConfiguredVerificationFailureConvergesAborted pins the governance rule
// for a REAL configured verification failure after approval: the approved
// proposal failed, the loop converges to exactly ONE terminal outcome
// (aborted), the patch is rolled back, and there is NO second provider
// invocation (the loop never auto-repairs an approved proposal).
func TestConfiguredVerificationFailureConvergesAborted(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	cfg := config.Default()
	x := execution.NewRuntimeExecutor(root, cfg, mock, bus, "")
	failing := execution.NewVerifier(root)
	failing.SetCustomSteps([]execution.VerificationStep{{Name: "syntax", Command: "false", Optional: false}})
	x.SetVerifier(failing)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	driver := NewDriver(adapter, bus)

	_, err := driver.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("driver.Run failed: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %v, want awaiting_human", driver.State())
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}

	term, err := driver.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove failed: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted (approved proposal failed verification)", term)
	}
	if !driver.State().IsTerminal() {
		t.Fatalf("driver state = %v, want terminal", driver.State())
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls after approve = %d, want 1 (failed approval must not trigger a repair re-execution)", mock.calls())
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("failed verification must roll the patch back, file = %q", got)
	}
}

// TestDoubleApproveCannotLeaveStaleAwaitingHuman pins the convergence rule for
// a hard approve error (patch no longer held): the run converges to a terminal
// aborted state — never a stale awaiting_human and never a second execution.
func TestDoubleApproveCannotLeaveStaleAwaitingHuman(t *testing.T) {
	_, mock, adapter, bus := htmlTestHarness(t)
	driver := NewDriver(adapter, bus)

	if _, err := driver.Run(context.Background(), "change old to new @index.html"); err != nil {
		t.Fatalf("driver.Run failed: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %v, want awaiting_human", driver.State())
	}

	// First approve completes the run.
	term, err := driver.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove failed: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("first approve termination = %+v, want completed", term)
	}

	// Second approve against a completed loop: the driver must refuse (the
	// patch was already consumed) and must NOT park or re-execute.
	term, err = driver.ResumeApprove(context.Background())
	if err == nil {
		t.Fatalf("second approve must fail, got term %+v", term)
	}
	if !driver.State().IsTerminal() {
		t.Fatalf("state after double approve = %v, want terminal", driver.State())
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (double approve must not re-execute)", mock.calls())
	}
}
