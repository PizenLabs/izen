package execution

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── RuntimeExecutor streaming evidence tests (P0 #2/#3) ─────────────────────
//
// The RuntimeExecutor must expose the model invocation as an OBSERVABLE stream
// (provider.waiting → provider.first_token → provider.stream_delta →
// provider.usage_update → provider.response) while the final ExecutionResult
// contract stays stable. Reasoning must surface as telemetry ONLY — never as
// verbatim chain-of-thought text on the event stream.

// streamingReader serves a fixed byte stream in small chunks and reports
// authoritative provider usage through the ai.UsageProvider contract.
type streamingReader struct {
	mu      sync.Mutex
	data    string
	offset  int
	usage   ai.ProviderUsage
	onRead  func()
	closeCt int
}

func (r *streamingReader) Read(p []byte) (int, error) {
	if r.onRead != nil {
		r.onRead()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r *streamingReader) Close() error {
	r.closeCt++
	return nil
}

func (r *streamingReader) Usage() ai.ProviderUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage
}

// streamingProvider serves a single streaming response and records the requests
// it received (stream flag + reasoning handler presence).
type streamingProvider struct {
	mu      sync.Mutex
	reader  io.ReadCloser
	callCt  int
	execCt  int
	lastReq ai.Request
}

func (p *streamingProvider) Name() string { return "stream-mock" }

func (p *streamingProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.execCt++
	return nil, errors.New("streaming provider must not fall back to Execute")
}

func (p *streamingProvider) ExecuteStream(_ context.Context, req ai.Request) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callCt++
	p.lastReq = req
	return p.reader, nil
}

func (p *streamingProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCt
}

func (p *streamingProvider) executeCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.execCt
}

func (p *streamingProvider) lastRequest() ai.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastReq
}

func TestExecutorStreamsProviderLifecycleAndStripsReasoning(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.md", "old content\n")

	// Reasoning arrives sentinel-wrapped in the main stream (OpenRouter style);
	// content follows. The runtime must expose telemetry, never the reasoning
	// text itself.
	r := &streamingReader{
		data: stream.ReasoningSentinel + "think step one" + stream.ReasoningSentinel + "the visible answer",
		usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     10,
			CompletionTokens: 5,
			ReasoningTokens:  3,
		},
	}
	prov := &streamingProvider{reader: r}

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	x := phase4Executor(t, root, prov, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Prompt:   "$hot change note.md",
		Targets:  []string{"note.md"},
		Strategy: executionStrategyProfile(t, "note.md"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("expected pending patch at the approval gate")
	}
	if prov.executeCalls() != 0 {
		t.Fatalf("streaming provider fell back to Execute %d times, want 0", prov.executeCalls())
	}
	if prov.calls() != 1 {
		t.Fatalf("provider stream calls = %d, want 1", prov.calls())
	}
	if lr := prov.lastRequest(); !lr.Stream {
		t.Error("streaming request must carry Stream: true")
	} else if lr.ReasoningHandler == nil {
		t.Error("streaming request must carry a ReasoningHandler (telemetry only)")
	}

	// The authoritative provider usage must land on the proof (not a local
	// estimate or a fabricated 0).
	if len(res.Proof.ModelInvocations) != 1 {
		t.Fatalf("proof model invocations = %d, want 1", len(res.Proof.ModelInvocations))
	}
	inv := res.Proof.ModelInvocations[0]
	if inv.TokenInput != 10 || inv.TokenOutput != 5 {
		t.Fatalf("proof usage = %d in / %d out, want 10 / 5", inv.TokenInput, inv.TokenOutput)
	}

	// The streamed artifact must NOT contain the reasoning text.
	if !strings.Contains(res.Content, "the visible answer") {
		t.Fatalf("artifact missing streamed content: %q", res.Content)
	}
	if strings.Contains(res.Content, "think step one") {
		t.Fatalf("reasoning text leaked into the artifact: %q", res.Content)
	}

	// The canonical stream lifecycle events must fire in the truthful order:
	// model.invoked → provider.waiting → provider.first_token →
	// provider.usage_update → provider.response. The stream deltas are
	// evidence transport and may be delivered after waiting/first_token.
	if !collector.waitCount(events.EventProviderFirstToken, 1, 2*time.Second) {
		t.Fatalf("provider.first_token never fired; types=%v", collector.types())
	}
	if !collector.waitCount(events.EventProviderResponse, 1, 2*time.Second) {
		t.Fatalf("provider.response never fired; types=%v", collector.types())
	}
	kinds := collector.types()
	idx := map[string]int{}
	for i, k := range kinds {
		if _, ok := idx[k]; !ok {
			idx[k] = i
		}
	}
	order := []string{
		events.EventModelInvoked,
		events.EventProviderWaiting,
		events.EventProviderFirstToken,
		events.EventProviderResponse,
	}
	for i := 1; i < len(order); i++ {
		a, b := idx[order[i-1]], idx[order[i]]
		if a < 0 || b < 0 {
			t.Fatalf("missing stream event %q or %q; types=%v", order[i-1], order[i], kinds)
		}
		if a > b {
			t.Errorf("stream event order violated: %s (%d) after %s (%d); types=%v", order[i-1], a, order[i], b, kinds)
		}
	}
	if collector.count(events.EventProviderUsageUpdate) < 1 {
		t.Errorf("provider.usage_update never fired; types=%v", kinds)
	}
	if collector.count(events.EventProviderStreamDelta) < 1 {
		t.Errorf("provider.stream_delta never fired; types=%v", kinds)
	}
	// Reasoning is telemetry-only: reasoning.telemetry fires, and NO event
	// carries the reasoning text.
	if collector.count(events.EventReasoningTelemetry) < 1 {
		t.Errorf("reasoning.telemetry never fired; types=%v", kinds)
	}
	for _, ev := range collector.eventsSnapshot() {
		switch p := ev.Payload().(type) {
		case events.ReasoningTelemetryPayload:
			if p.Tokens != 3 {
				t.Errorf("reasoning telemetry tokens = %d, want provider-reported 3", p.Tokens)
			}
			if p.Duration <= 0 {
				t.Error("reasoning telemetry duration must be positive")
			}
		case events.ProviderStreamDeltaPayload:
			if strings.Contains(p.Delta, "think step one") {
				t.Errorf("reasoning text leaked as a stream delta: %q", p.Delta)
			}
		}
	}
}

// blockingReader never yields bytes: Read only returns when the context is
// cancelled, mimicking a provider stream that is stalled on the network.
type blockingReader struct{ ctx context.Context }

func (r *blockingReader) Read(p []byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r *blockingReader) Close() error { return nil }

// TestExecutorStreamCancellationRoutesToCancelled pins P0 #1/#2: a provider
// call cancelled via the operation context produces a CLEAN cancelled outcome —
// never a fabricated failure — and no artifact, no mutation, no rollback.
func TestExecutorStreamCancellationRoutesToCancelled(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.md", "old content\n")

	ctx, cancel := context.WithCancel(context.Background())
	prov := &streamingProvider{reader: &blockingReader{ctx: ctx}}

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
			Prompt:   "$hot change note.md",
			Targets:  []string{"note.md"},
			Strategy: executionStrategyProfile(t, "note.md"),
		})
		done <- outcome{res, err}
	}()

	// The execution reaches the model stage (provider.waiting) and then blocks
	// on the stalled reader. Cancel from the caller side — exactly like Ctrl+C
	// cancelling the active operation context.
	if !collector.waitCount(events.EventProviderWaiting, 1, 2*time.Second) {
		t.Fatalf("provider.waiting never fired; types=%v", collector.types())
	}
	cancel()

	out := <-done
	if out.err != nil {
		t.Fatalf("Execute returned an error for a cancellation: %v", out.err)
	}
	res := out.res
	// The result must carry a clean cancelled outcome (nil error, cancelled
	// proof outcome) and no mutation evidence.
	if res.Err != nil {
		t.Fatalf("cancelled execution returned err = %v, want nil (clean cancellation)", res.Err)
	}
	if res.Proof == nil || res.Proof.Outcome != OutcomeCancelled {
		t.Fatalf("proof outcome = %+v, want %s", res.Proof, OutcomeCancelled)
	}
	if len(res.Proof.Mutations) != 0 {
		t.Fatalf("cancelled execution recorded mutations: %+v", res.Proof.Mutations)
	}
	if res.PendingPatchID != "" {
		t.Fatal("cancelled execution must not reach the approval gate")
	}
	if collector.count(events.EventExecutionFailed) != 0 {
		t.Errorf("cancelled execution emitted execution.failed; types=%v", collector.types())
	}
}

// executionStrategyProfile builds a deterministic targeted-mutation strategy
// profile for the test executor (mirrors the gateway's unconditional select).
func executionStrategyProfile(t *testing.T, target string) *strategy.ExecutionStrategyProfile {
	t.Helper()
	return &strategy.ExecutionStrategyProfile{
		Strategy:       strategy.TargetedMutation,
		ModelRequired:  true,
		StrategyReason: "test targeted mutation",
		ContextPolicy:  strategy.ContextPolicyTargetFileOnly,
		Targets:        []strategy.Target{{Resolved: target, Exists: true}},
	}
}
