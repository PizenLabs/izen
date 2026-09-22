package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/llmstep"
)

// newExec is a test executor bound to an arbitrary provider (testExecutor is
// locked to *mockProvider).
func newExec(t *testing.T, root string, p ai.Provider, bus *events.Bus) *RuntimeExecutor {
	t.Helper()
	cfg := config.Default()
	x := NewRuntimeExecutor(root, cfg, p, bus, "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

// subscribeSteps captures the bounded-step telemetry plus the lifecycle events
// so tests can assert the continuation sequence occurred (subscribeAll does not
// subscribe to reasoning.* events).
func subscribeSteps(bus *events.Bus) *eventCollector {
	c := &eventCollector{}
	for _, typ := range []string{
		events.EventStepStarted,
		events.EventStepCompleted,
		events.EventStepExhausted,
		events.EventContinuationScheduled,
		events.EventContinuationStarted,
		events.EventStateCommitted,
		events.EventStateRejected,
	} {
		bus.Subscribe(typ, func(ev events.DomainEvent) { c.add(ev) })
	}
	return c
}

// seqProvider is a mock ai.Provider with sequenced responses and request capture.
type seqProvider struct {
	mu        sync.Mutex
	name      string
	responses []*ai.Response
	requests  []ai.Request
}

func (m *seqProvider) Name() string { return m.name }

func (m *seqProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := len(m.requests)
	if i >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call #%d (%d recorded)", i+1, len(m.responses))
	}
	m.requests = append(m.requests, req)
	return m.responses[i], nil
}

func (m *seqProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

func (m *seqProvider) capturedRequests() []ai.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ai.Request, len(m.requests))
	copy(out, m.requests)
	return out
}

func truncatedResponse(content string, outTokens int) *ai.Response {
	return &ai.Response{
		Content: content,
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     50,
			CompletionTokens: outTokens,
			FinishReason:     "length",
		},
	}
}

func naturalResponse(content string) *ai.Response {
	return &ai.Response{
		Content: content,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 60},
	}
}

func countEvents(c *eventCollector, typ string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.events {
		if ev.Type() == typ {
			n++
		}
	}
	return n
}

// waitCount polls until the collector has recorded at least want events of the
// type (bus delivery is async per-subscription), failing the test on timeout.
func waitCount(t *testing.T, c *eventCollector, typ string, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if countEvents(c, typ) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("collector never reached %d×%s; got %d in %v", want, typ, countEvents(c, typ), c.types())
}

// TestAskTruncatedFollowedByContinuation_Succeeds is the core acceptance:
// a finish_reason="length" read-only/ASK response is NOT a terminal error; a
// bounded continuation advances to a complete answer under the SAME
// read-only authority, and the outcome is a completed result, never a
// mutation, never a PLAN routing.
func TestAskTruncatedFollowedByContinuation_Succeeds(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus(events.DefaultBufferSize)
	c := subscribeSteps(bus)
	p := &seqProvider{name: "mock", responses: []*ai.Response{
		truncatedResponse("part one narrative.", 980),
		naturalResponse("part two narrative."),
	}}
	x := newExec(t, root, p, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "ask-c1",
		Mode:            "ask",
		Prompt:          "explain the file",
		MaxOutputTokens: llmstep.DefaultAskRequestedTokens,
	})
	if err != nil {
		t.Fatalf("Execute returned an error for a recoverable boundary condition: %v", err)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("expected result + proof")
	}
	if res.Proof.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %q, want completed", res.Proof.Outcome)
	}
	waitCount(t, c, events.EventStepCompleted, 1, 2*time.Second)
	// The executor output gate discards the truncated buffer (empty salvage,
	// mirroring plan StepIncomplete rescheduling): the exhausted step commits
	// nothing and the continuation rebuilds from the bare base prompt. The
	// delivered answer is the continuation's complete response.
	if got := res.Content; !strings.Contains(got, "part two narrative.") {
		t.Fatalf("content = %q, want the continuation-delivered answer", got)
	}

	invs := res.Proof.ModelInvocations
	if len(invs) != 2 {
		t.Fatalf("model invocations = %d, want 2", len(invs))
	}
	if invs[0].FinishReason != "length" {
		t.Fatalf("invocation[0].FinishReason = %q, want length", invs[0].FinishReason)
	}
	if invs[0].TokenOutput != 980 {
		t.Fatalf("invocation[0] output tokens = %d, want authoritative 980", invs[0].TokenOutput)
	}

	for _, typ := range []string{
		events.EventStepStarted,
		events.EventStepExhausted,
		events.EventContinuationScheduled,
		events.EventContinuationStarted,
		events.EventStateCommitted,
		events.EventStepCompleted,
	} {
		if !c.hasType(typ) {
			t.Fatalf("missing event %s in %v", typ, c.types())
		}
	}
	if c.hasType(events.EventMutationStarted) {
		t.Fatalf("read-only ASK emitted mutation event: %v", c.types())
	}
	if got := countEvents(c, events.EventStepStarted); got != 2 {
		t.Fatalf("step started count = %d, want 2", got)
	}
	if got := countEvents(c, events.EventStepExhausted); got != 1 {
		t.Fatalf("step exhausted count = %d, want 1", got)
	}
	if got := countEvents(c, events.EventStepCompleted); got != 1 {
		t.Fatalf("step completed count = %d, want 1", got)
	}

	reqs := p.capturedRequests()
	if len(reqs) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(reqs))
	}
	if reqs[0].MaxTokens != llmstep.DefaultAskRequestedTokens {
		t.Fatalf("request[0].MaxTokens = %d, want %d", reqs[0].MaxTokens, llmstep.DefaultAskRequestedTokens)
	}
	if got := reqs[1].Messages[0].Content; !strings.Contains(got, "OUTPUT BUDGET EXHAUSTED") {
		t.Fatalf("continuation turn does not carry the bounded-step contract:\n%s", got)
	}
}

// TestAskContinuationBudgetExhausted_TypedRecoverable locks the fail-closed
// headless path: when every bounded step is cut off and the request budget is
// consumed with nothing delivered, the typed OUTPUT_EXHAUSTED error surfaces
// (never a silent success, never a hidden interactive wait).
func TestAskContinuationBudgetExhausted_TypedRecoverable(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus(events.DefaultBufferSize)
	c := subscribeSteps(bus)
	p := &seqProvider{name: "mock", responses: []*ai.Response{
		truncatedResponse("buffer one.", 980),
		truncatedResponse("buffer two.", 980),
		truncatedResponse("buffer three.", 980),
		truncatedResponse("buffer four.", 980),
	}}
	x := newExec(t, root, p, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "ask-c2",
		Mode:      "ask",
		Prompt:    "we keep hitting the ceiling",
	})
	if err == nil {
		t.Fatal("expected a typed recoverable error, got nil (silent success is forbidden)")
	}
	var exhausted *llmstep.OutputExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want *llmstep.OutputExhaustedError", err)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("expected result + proof")
	}
	if res.Proof.Outcome != OutcomeTruncated {
		t.Fatalf("outcome = %q, want truncated", res.Proof.Outcome)
	}
	if len(res.Proof.ModelInvocations) != 4 {
		t.Fatalf("model invocations = %d, want 4 (every bounded attempt recorded)", len(res.Proof.ModelInvocations))
	}
	waitCount(t, c, events.EventStepExhausted, 4, 2*time.Second)
	if got := countEvents(c, events.EventStepExhausted); got != 4 {
		t.Fatalf("step exhausted count = %d, want 4", got)
	}
	if c.hasType(events.EventStepCompleted) {
		t.Fatalf("completed event despite zero delivered steps: %v", c.types())
	}
	if c.hasType(events.EventMutationStarted) {
		t.Fatalf("read-only budget exhaustion emitted a mutation event: %v", c.types())
	}
}

// TestAskConstrainedModel_RequestClampedBelowCeiling verifies the capability
// clamp on the wire: a free-tier/constrained model (cohere/north-mini-code:free)
// must receive its 980-token ceiling, not the unclamped requested budget.
func TestAskConstrainedModel_RequestClampedBelowCeiling(t *testing.T) {
	root := t.TempDir()
	p := &seqProvider{name: "openrouter", responses: []*ai.Response{naturalResponse("fine.")}}
	cfg := config.Default()
	cfg.Models.SessionModel = "cohere/north-mini-code:free"
	if provCfg, ok := cfg.AI.Providers["openrouter"]; ok {
		provCfg.DefaultModel = "cohere/north-mini-code:free"
		cfg.AI.Providers["openrouter"] = provCfg
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := NewRuntimeExecutor(root, cfg, p, bus, "")

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "ask-c3",
		Mode:      "ask",
		Prompt:    "short answer",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("expected result + proof")
	}
	reqs := p.capturedRequests()
	if len(reqs) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(reqs))
	}
	if got := reqs[0].MaxTokens; got >= capabilityConstrainedCeiling() {
		t.Fatalf("constrained request budget = %d, want clamped below ceiling %d", got, capabilityConstrainedCeiling())
	}
	if got := res.Proof.ModelInvocations[0].Model; got != "cohere/north-mini-code:free" {
		t.Fatalf("invocation model = %q, want cohere/north-mini-code:free", got)
	}
}

// capabilityConstrainedCeiling mirrors the provider constrained output threshold.
func capabilityConstrainedCeiling() int {
	return 1024
}
