package execution

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

// reproUsage is the authoritative provider usage of the 5,883-token repro:
// OpenRouter reported completion_tokens=5883 (output INCLUDING reasoning, the
// OpenAI-compatible convention) with reasoning_tokens=5000 — only ~883 tokens
// of visible content. Every executor accounting test anchors to these exact
// numbers so the forensic table stays reconcilable.
var reproUsage = ai.ProviderUsage{
	PromptTokens:     2181,
	CompletionTokens: 5883,
	ReasoningTokens:  5000,
	TotalTokens:      7064,
	Known:            true,
}

const validHTML = `<!doctype html>
<html><head><title>t</title></head><body><p>hello</p></body></html>`

// malformedHTML mirrors the repro's rejected artifact: an unterminated
// <script> element (the exact "html: unterminated <script> element" gate error).
const malformedHTML = `<html><body><script>alert(1)</body></html>`

// blockingUsageStream delivers content once, reports a fixed authoritative
// usage, then blocks until ctx cancellation so the executor can be cancelled
// mid-stream AFTER billing occurred.
type blockingUsageStream struct {
	usage   ai.ProviderUsage
	content []byte
	ctx     context.Context
	started chan struct{}
	once    sync.Once
	done    bool
	mu      sync.Mutex
}

func (s *blockingUsageStream) Usage() ai.ProviderUsage { return s.usage }
func (s *blockingUsageStream) FinishReason() string    { return "stop" }
func (s *blockingUsageStream) Close() error            { return nil }

func (s *blockingUsageStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if !s.done && len(s.content) > 0 {
		n := copy(p, s.content)
		s.content = s.content[n:]
		if len(s.content) == 0 {
			s.done = true
		}
		s.mu.Unlock()
		s.once.Do(func() { close(s.started) })
		return n, nil
	}
	s.mu.Unlock()
	select {
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	case <-time.After(time.Hour):
		return 0, io.EOF
	}
}

// TestProviderUsageMatchesAuthoritativeOpenRouterUsage proves the accounting
// spine A==B==C==D: the provider's authoritative usage flows verbatim through
// streamUsageTracker (here: non-streaming fallback), into ModelInvocation, and
// is aggregated by finalizeResult into ExecutionCompleted — no estimate, no
// rescaling, no drop. This is the exact 5,883-token repro table.
func TestProviderUsageMatchesAuthoritativeOpenRouterUsage(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", validHTML)
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: validHTML,
		Usage:   reproUsage,
	}}}
	x := testExecutor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "repro-1",
		Mode:      "build",
		Prompt:    "check this file @index.html and rewrite the code for me",
		Target:    "index.html",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("res.Err: %v", res.Err)
	}
	// D: final aggregate must equal the authoritative provider usage verbatim.
	if res.Completed.InputTokens != reproUsage.PromptTokens {
		t.Errorf("Completed.InputTokens = %d, want %d", res.Completed.InputTokens, reproUsage.PromptTokens)
	}
	if res.Completed.OutputTokens != reproUsage.CompletionTokens {
		t.Errorf("Completed.OutputTokens = %d, want %d", res.Completed.OutputTokens, reproUsage.CompletionTokens)
	}
	if res.Completed.ReasoningTokens != reproUsage.ReasoningTokens {
		t.Errorf("Completed.ReasoningTokens = %d, want %d", res.Completed.ReasoningTokens, reproUsage.ReasoningTokens)
	}
	if !res.Completed.Known {
		t.Error("Completed.Known = false, want true (authoritative usage must be marked known)")
	}
	if len(res.Proof.ModelInvocations) != 1 {
		t.Fatalf("Proof.ModelInvocations = %d entries, want exactly 1 logical invocation", len(res.Proof.ModelInvocations))
	}
	inv := res.Proof.ModelInvocations[0]
	if inv.TokenInput != reproUsage.PromptTokens || inv.TokenOutput != reproUsage.CompletionTokens ||
		inv.ReasoningTokens != reproUsage.ReasoningTokens || !inv.Known {
		t.Errorf("ModelInvocation = %+v, want exact authoritative reproduction of %+v", inv, reproUsage)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want exactly 1", mock.callCount)
	}
}

// TestArtifactRejectedPreservesProviderUsageAndTerminalSemantics pins the
// 5,883-token repro outcome end-to-end: the provider BILLED 5883 tokens, the
// artifact gate rejected the malformed HTML (unterminated <script>), and the
// authoritative usage MUST survive into ExecutionCompleted instead of being
// erased to 0/Known=false. The rejection itself stays a PERMANENT, explicitly
// terminal outcome — no repair re-invocation, no second provider call.
func TestArtifactRejectedPreservesProviderUsageAndTerminalSemantics(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", validHTML)
	bus := events.NewBus(events.DefaultBufferSize)
	collector := subscribeAll(bus)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: malformedHTML,
		Usage:   reproUsage,
	}}}
	x := testExecutor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "repro-2",
		Mode:      "build",
		Prompt:    "check this file @index.html and rewrite the code for me",
		Target:    "index.html",
	})
	if err == nil {
		t.Fatal("Execute must fail on the malformed HTML artifact")
	}
	if !errors.Is(err, ErrArtifactRetryableRejected) {
		t.Fatalf("err = %v, want ErrArtifactRetryableRejected", err)
	}
	if res == nil {
		t.Fatal("Execute must return a non-nil result carrying the rejection")
	}
	// The truthful 5,883-token account survives the rejection.
	if res.Completed.OutputTokens != 5883 {
		t.Errorf("Completed.OutputTokens = %d, want 5883 (provider billing must survive artifact rejection)", res.Completed.OutputTokens)
	}
	if res.Completed.InputTokens != 2181 {
		t.Errorf("Completed.InputTokens = %d, want 2181", res.Completed.InputTokens)
	}
	if res.Completed.ReasoningTokens != 5000 {
		t.Errorf("Completed.ReasoningTokens = %d, want 5000", res.Completed.ReasoningTokens)
	}
	if !res.Completed.Known {
		t.Error("Completed.Known = false, want true — provider billing is real regardless of artifact validity")
	}
	if res.Proof.Outcome != OutcomeArtifactRetryableRejected {
		t.Errorf("Proof.Outcome = %q, want %q", res.Proof.Outcome, OutcomeArtifactRetryableRejected)
	}
	if len(res.Proof.ModelInvocations) != 1 {
		t.Errorf("Proof.ModelInvocations = %d entries, want 1 (one logical invocation, billed)", len(res.Proof.ModelInvocations))
	}
	// Phase 7E: DecisionRetry is preserved as a recoverable execution fact;
	// the executor does not perform a second model invocation here.
	if !collector.waitHas(events.EventExecutionFailed, time.Second) {
		t.Error("no EventExecutionFailed emitted")
	}
	// The file is untouched (rejection happened before any mutation surface).
	if got := mustRead(t, root, "index.html"); got != validHTML {
		t.Fatalf("file mutated on a rejected artifact: %q", got)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 (rejection must not re-invoke)", mock.callCount)
	}
}

// TestOutputBudgetDoesNotAlterReportedUsage proves a bounded output budget is a
// CONTROL mechanism, never a reporting mechanism: with MaxOutputTokens set, the
// provider request carries max_tokens=512 while Completed.OutputTokens still
// reports the authoritative 5883 verbatim.
func TestOutputBudgetDoesNotAlterReportedUsage(t *testing.T) {
	run := func(maxTokens int) (*ExecutionResult, int) {
		root := t.TempDir()
		writeTarget(t, root, "index.html", validHTML)
		mock := &mockProvider{responses: []*ai.Response{{
			Content: validHTML,
			Usage:   reproUsage,
		}}}
		x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))
		res, err := x.Execute(context.Background(), ExecuteRequest{
			RequestID:       "budget",
			Mode:            "build",
			Prompt:          "rewrite index.html",
			Target:          "index.html",
			MaxOutputTokens: maxTokens,
		})
		if err != nil {
			t.Fatalf("Execute (max=%d): %v", maxTokens, err)
		}
		if len(mock.requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(mock.requests))
		}
		return res, mock.requests[0].MaxTokens
	}

	unbounded, sentUnbounded := run(0)
	bounded, sentBounded := run(512)

	if sentUnbounded != 0 {
		t.Errorf("unbounded request MaxTokens = %d, want 0 (omitted)", sentUnbounded)
	}
	if sentBounded != 512 {
		t.Errorf("bounded request MaxTokens = %d, want 512", sentBounded)
	}
	// Reporting is identical: the budget bounds the request, never the truth.
	if unbounded.Completed.OutputTokens != 5883 || bounded.Completed.OutputTokens != 5883 {
		t.Errorf("budget altered reporting: unbounded=%d bounded=%d, want both 5883",
			unbounded.Completed.OutputTokens, bounded.Completed.OutputTokens)
	}
	if !unbounded.Completed.Known || !bounded.Completed.Known {
		t.Error("budget altered Known (want true in both cases)")
	}
}

// TestLogicalInvocationVsHTTPAttemptAccounting pins the P1 distinction at the
// executor boundary: 3 HTTP attempts (2 rate-limit retries) inside ONE logical
// invocation produce exactly ONE ModelInvocation carrying HTTPAttempts=3 and
// RateLimitedRetries=2 — the token account is summed once, never per attempt.
func TestLogicalInvocationVsHTTPAttemptAccounting(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", validHTML)
	usage := reproUsage
	usage.HTTPAttempts = 3
	usage.RateLimitedRetries = 2
	mock := &mockProvider{responses: []*ai.Response{{Content: validHTML, Usage: usage}}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "retry",
		Mode:      "build",
		Prompt:    "rewrite index.html",
		Target:    "index.html",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Proof.ModelInvocations) != 1 {
		t.Fatalf("ModelInvocations = %d, want 1 logical invocation across 3 HTTP attempts", len(res.Proof.ModelInvocations))
	}
	inv := res.Proof.ModelInvocations[0]
	if inv.HTTPAttempts != 3 || inv.RateLimitedRetries != 2 {
		t.Errorf("invocation retry forensics = HTTPAttempts:%d RateLimitedRetries:%d, want 3/2", inv.HTTPAttempts, inv.RateLimitedRetries)
	}
	// Output is counted ONCE for the logical invocation, not 3x.
	if res.Completed.OutputTokens != 5883 {
		t.Errorf("Completed.OutputTokens = %d, want 5883 (never multiplied across HTTP attempts)", res.Completed.OutputTokens)
	}
}

// TestCancellationPreservesBilledUsage proves a mid-stream cancellation that
// occurred AFTER the provider reported usage does not erase the billing: the
// partial authoritative account survives into ExecutionCompleted with the
// cancelled outcome.
func TestCancellationPreservesBilledUsage(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", validHTML)
	bus := events.NewBus(events.DefaultBufferSize)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &blockingUsageStream{
		usage:   reproUsage,
		content: []byte("<html><body><p>partial"),
		ctx:     ctx,
		started: make(chan struct{}),
	}
	prov := &streamingProvider{reader: stream}
	cfg := config.Default()
	x := NewRuntimeExecutor(root, cfg, prov, bus, "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})

	// Cancel as soon as the stream delivered its first bytes (billing began).
	go func() { <-stream.started; cancel() }()

	res, err := x.Execute(ctx, ExecuteRequest{
		RequestID: "cancel",
		Mode:      "build",
		Prompt:    "rewrite index.html",
		Target:    "index.html",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Proof.Outcome != OutcomeCancelled {
		t.Fatalf("Proof.Outcome = %q, want %q (cancelled mid-stream)", res.Proof.Outcome, OutcomeCancelled)
	}
	// The authoritative usage observed before cancellation is preserved.
	if res.Completed.OutputTokens != 5883 {
		t.Errorf("Completed.OutputTokens = %d, want 5883 (billing preserved after cancellation)", res.Completed.OutputTokens)
	}
	if res.Completed.InputTokens != 2181 {
		t.Errorf("Completed.InputTokens = %d, want 2181", res.Completed.InputTokens)
	}
	if !res.Completed.Known {
		t.Error("Completed.Known = false, want true")
	}
	if prov.calls() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 (cancellation must not re-invoke)", prov.calls())
	}
}
