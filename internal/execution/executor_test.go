package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

// mockProvider implements ai.Provider for executor tests.
type mockProvider struct {
	mu        sync.Mutex
	responses []*ai.Response
	callCount int
	requests  []ai.Request
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callCount >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call #%d", m.callCount+1)
	}
	m.requests = append(m.requests, req)
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *mockProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

// eventCollector captures domain events race-free.
type eventCollector struct {
	mu     sync.Mutex
	events []events.DomainEvent
}

func subscribeAll(bus *events.Bus) *eventCollector {
	c := &eventCollector{}
	bus.Subscribe(events.EventExecutionStarted, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventStrategySelected, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventTargetResolved, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventContextPrepared, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventModelInvoked, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventArtifactProduced, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventMutationStarted, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventMutationCompleted, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventVerificationCompleted, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventExecutionFinished, func(ev events.DomainEvent) { c.add(ev) })
	bus.Subscribe(events.EventExecutionFailed, func(ev events.DomainEvent) { c.add(ev) })
	return c
}

func (c *eventCollector) add(ev events.DomainEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *eventCollector) hasType(typ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Type() == typ {
			return true
		}
	}
	return false
}

func (c *eventCollector) types() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.events))
	for _, ev := range c.events {
		out = append(out, ev.Type())
	}
	return out
}

func (c *eventCollector) waitHas(typ string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.hasType(typ) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c.hasType(typ)
}

// trivialVerifier is a verifier whose single step is a real command that always
// succeeds — a real verifier result, not a fabricated one.
func trivialVerifier(root string) *Verifier {
	return &Verifier{
		root:  root,
		steps: []VerificationStep{{Name: "noop", Command: "true", Optional: false}},
	}
}

// namedMockProvider is an ai.Provider with a controllable identity + call
// counter so contract tests can assert WHICH adapter a model was routed to.
type namedMockProvider struct {
	name      string
	err       error
	mu        sync.Mutex
	callCount int
	requests  []ai.Request
}

func (m *namedMockProvider) Name() string { return m.name }

func (m *namedMockProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	return &ai.Response{Content: sampleReplace}, nil
}

func (m *namedMockProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

func (m *namedMockProvider) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *namedMockProvider) modelsCalled() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.requests))
	for _, r := range m.requests {
		out = append(out, r.Model)
	}
	return out
}

func TestRuntimeExecutor_ProviderModelContract(t *testing.T) {
	writeNote := func(t *testing.T, root string) {
		t.Helper()
		writeTarget(t, root, "note.txt", sampleOriginal)
	}
	exec := func(t *testing.T, root string, cfg *config.Config, p ai.Provider) *RuntimeExecutor {
		t.Helper()
		return NewRuntimeExecutor(root, cfg, p, nil, "")
	}

	t.Run("openrouter provider + openrouter model => openrouter invocation", func(t *testing.T) {
		root := t.TempDir()
		writeNote(t, root)
		cfg := config.Default()
		cfg.AI.Providers["openrouter"] = config.AIProviderConfig{
			BaseURL:      "https://openrouter.ai/api/v1",
			APIKey:       "sk-test",
			DefaultModel: "cohere/north-mini-code:free",
		}
		cfg.Models.SessionModel = "cohere/north-mini-code:free"
		p := &namedMockProvider{name: "openrouter"}
		x := exec(t, root, cfg, p)

		res, err := x.Execute(context.Background(), ExecuteRequest{
			RequestID: "c1",
			Mode:      "build",
			Prompt:    "change bar to qux",
			Target:    "note.txt",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.Err != nil {
			t.Fatalf("Execute res.Err: %v", res.Err)
		}
		if p.calls() != 1 {
			t.Fatalf("provider calls = %d, want exactly 1", p.calls())
		}
		if got := p.modelsCalled(); len(got) != 1 || got[0] != "cohere/north-mini-code:free" {
			t.Fatalf("invoked models = %v, want [cohere/north-mini-code:free]", got)
		}
	})

	t.Run("ollama provider + local model => ollama invocation", func(t *testing.T) {
		root := t.TempDir()
		writeNote(t, root)
		cfg := config.Default() // ollama default qwen2.5-coder:7b, no session override
		p := &namedMockProvider{name: "ollama"}
		x := exec(t, root, cfg, p)

		res, err := x.Execute(context.Background(), ExecuteRequest{
			RequestID: "c2",
			Mode:      "build",
			Prompt:    "change bar to qux",
			Target:    "note.txt",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.Err != nil {
			t.Fatalf("Execute res.Err: %v", res.Err)
		}
		if p.calls() != 1 {
			t.Fatalf("provider calls = %d, want exactly 1", p.calls())
		}
		if got := p.modelsCalled(); len(got) != 1 || got[0] != "qwen2.5-coder:7b" {
			t.Fatalf("invoked models = %v, want [qwen2.5-coder:7b]", got)
		}
	})

	t.Run("provider mismatch fails deterministically before any network call", func(t *testing.T) {
		root := t.TempDir()
		writeNote(t, root)
		cfg := config.Default()
		// The user's active model is an OpenRouter model…
		cfg.Models.SessionModel = "cohere/north-mini-code:free"
		// …but the executor is bound to the Ollama adapter (a stale provider
		// that was never re-bound after the UI switched). This is the exact
		// mismatch that must never reach the network.
		p := &namedMockProvider{name: "ollama"}
		x := exec(t, root, cfg, p)

		res, err := x.Execute(context.Background(), ExecuteRequest{
			RequestID: "c3",
			Mode:      "build",
			Prompt:    "change bar to qux",
			Target:    "note.txt",
		})
		if err == nil {
			t.Fatal("expected a deterministic error for a provider/model mismatch")
		}
		if res == nil || res.Err == nil {
			t.Fatal("expected the result to carry the error")
		}
		if !errors.Is(res.Err, ErrProviderModelMismatch) {
			t.Fatalf("error = %v, want ErrProviderModelMismatch", res.Err)
		}
		if p.calls() != 0 {
			t.Fatalf("provider invoked %d times on a mismatch, want 0 (must fail before the network)", p.calls())
		}
		if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
			t.Fatalf("file mutated on a mismatch: %q", got)
		}
	})
}

func testExecutor(t *testing.T, root string, mock *mockProvider, bus *events.Bus) *RuntimeExecutor {
	t.Helper()
	cfg := config.Default()
	x := NewRuntimeExecutor(root, cfg, mock, bus, "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

const sampleOriginal = "foo\nbar\nbaz\n"

const sampleReplace = `<<<<<<< SEARCH
bar
=======
qux
>>>>>>>`

func writeTarget(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeExecutor_TargetedMutationFlow(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := subscribeAll(bus)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := testExecutor(t, root, mock, bus)

	ctx := context.Background()
	res, err := x.Execute(ctx, ExecuteRequest{
		RequestID: "r1",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// ── Execution stops at the approval gate; no file mutation yet ─────
	if res.PendingPatchID == "" {
		t.Fatal("expected a pending patch id (approval gate)")
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated before approval: %q", got)
	}
	if res.Strategy != "targeted_mutation" {
		t.Fatalf("strategy = %q, want targeted_mutation", res.Strategy)
	}
	if len(res.ModelCalls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(res.ModelCalls))
	}

	// ── Canonical lifecycle events must have fired ─────────────────────
	for _, typ := range []string{
		events.EventExecutionStarted,
		events.EventStrategySelected,
		events.EventTargetResolved,
		events.EventContextPrepared,
		events.EventModelInvoked,
		events.EventArtifactProduced,
	} {
		if !collector.waitHas(typ, time.Second) {
			t.Errorf("missing event %q; got %v", typ, collector.types())
		}
	}
	if collector.hasType(events.EventMutationStarted) {
		t.Error("MutationStarted fired before approval")
	}

	// ── Approve: the runtime owns the mutation + verification ──────────
	apr, err := x.Approve(ctx, res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := mustRead(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approve did not mutate the file")
	}
	if apr.Proof.Outcome != OutcomeChanged && apr.Proof.Outcome != OutcomeCreated {
		t.Fatalf("proof outcome = %q, want changed/created", apr.Proof.Outcome)
	}
	if !apr.Verification.Passed {
		t.Fatalf("verification did not pass: %+v", apr.Verification)
	}
	if len(apr.Mutations) == 0 {
		t.Fatal("expected mutation evidence after approve")
	}

	for _, typ := range []string{
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
	} {
		if !collector.waitHas(typ, time.Second) {
			t.Errorf("missing event %q after approve; got %v", typ, collector.types())
		}
	}

	// The MutationSet must be terminal.
	if ms := x.patches.MutationSet(); ms == nil || !ms.Terminal() {
		t.Fatal("mutation set not terminal after approve")
	}
}

// TestRuntimeExecutor_CompletedUsageAccount pins requirement 5 of the UX/exec
// consistency work: the runtime stamps the AUTHORITATIVE terminal usage account
// (provider, model, aggregate provider-reported input/output tokens, latency,
// artifact) onto ExecutionResult.Completed on EVERY terminal path — the
// renderer consumes it directly and never re-sums model calls.
func TestRuntimeExecutor_CompletedUsageAccount(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     sampleReplace,
		TokenInput:  2860,
		TokenOutput: 2048,
		Usage: ai.ProviderUsage{
			PromptTokens:     2860,
			CompletionTokens: 2048,
			TotalTokens:      4908,
			Known:            true,
		},
	}, {
		Content:     sampleReplace,
		TokenInput:  2860,
		TokenOutput: 2048,
		Usage: ai.ProviderUsage{
			PromptTokens:     2860,
			CompletionTokens: 2048,
			TotalTokens:      4908,
			Known:            true,
		},
	}}}
	x := testExecutor(t, root, mock, bus)

	ctx := context.Background()
	res, err := x.Execute(ctx, ExecuteRequest{
		RequestID: "r-usage",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// ── The approval-gate result carries the authoritative usage account ──
	if res.Completed.Provider != "mock" {
		t.Errorf("Completed.Provider = %q, want mock", res.Completed.Provider)
	}
	if res.Completed.InputTokens != 2860 || res.Completed.OutputTokens != 2048 {
		t.Errorf("Completed tokens = %d/%d, want provider-reported 2860/2048", res.Completed.InputTokens, res.Completed.OutputTokens)
	}
	if res.Completed.Model == "" {
		t.Error("Completed.Model must name the invoked model")
	}
	if res.Completed.Latency <= 0 {
		t.Errorf("Completed.Latency = %v, want positive", res.Completed.Latency)
	}
	if res.Completed.Artifact != "patch" {
		t.Errorf("Completed.Artifact = %q, want patch", res.Completed.Artifact)
	}

	// ── Approve: the terminal mutation result keeps the same account ─────
	apr, err := x.Approve(ctx, res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if apr.Completed.InputTokens != 2860 || apr.Completed.OutputTokens != 2048 {
		t.Errorf("approve Completed tokens = %d/%d, want provider-reported 2860/2048",
			apr.Completed.InputTokens, apr.Completed.OutputTokens)
	}
	if apr.Completed.Provider != "mock" || apr.Completed.Artifact != "patch" {
		t.Errorf("approve Completed = %+v, want provider=mock artifact=patch", apr.Completed)
	}

	// ── Reject path also stamps the account ──────────────────────────────
	writeTarget(t, root, "note.txt", sampleOriginal)
	res2, err := x.Execute(ctx, ExecuteRequest{RequestID: "r-usage2", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"})
	if err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	rej, err := x.Reject(ctx, res2.PendingPatchID, "skip")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rej.Completed.InputTokens != 2860 || rej.Completed.OutputTokens != 2048 {
		t.Errorf("reject Completed tokens = %d/%d, want provider-reported 2860/2048",
			rej.Completed.InputTokens, rej.Completed.OutputTokens)
	}
}

func TestRuntimeExecutor_RejectDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := subscribeAll(bus)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := testExecutor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "r2",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := x.Reject(context.Background(), res.PendingPatchID, "not wanted"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("reject mutated the file: %q", got)
	}
	if !collector.waitHas(events.EventExecutionFinished, time.Second) {
		t.Fatal("missing ExecutionFinished after reject")
	}
	fin := lastFinished(t, collector)
	if p, ok := fin.Payload().(events.ExecutionFinishedPayload); !ok || p.Success {
		t.Fatalf("expected non-success ExecutionFinished, got %+v", fin)
	}
}

func TestRuntimeExecutor_StrategyResolvesTargetFromPrompt(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "app.go", "package main\n")

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: "package main\n"}}}
	x := testExecutor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "r3",
		Mode:      "build",
		Prompt:    "refactor the function in @app.go",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Targets) == 0 || res.Targets[0] != "app.go" {
		t.Fatalf("targets = %v, want [app.go]", res.Targets)
	}
}

func TestRuntimeExecutor_NoProviderFailsDeterministically(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := subscribeAll(bus)
	cfg := config.Default()
	x := NewRuntimeExecutor(root, cfg, nil, bus, "")
	x.SetVerifier(trivialVerifier(root))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "r4",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err == nil {
		t.Fatal("expected error when no provider is configured for a model-required strategy")
	}
	if res == nil || res.Err == nil {
		t.Fatal("expected result carrying the error")
	}
	if !collector.waitHas(events.EventExecutionFailed, time.Second) {
		t.Fatal("missing ExecutionFailed event")
	}
	if collector.hasType(events.EventModelInvoked) {
		t.Fatal("ModelInvoked fired with no provider")
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated with no provider: %q", got)
	}
}

func TestRuntimeExecutor_ApprovalWithoutPendingFails(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, &mockProvider{}, bus)

	if _, err := x.Approve(context.Background(), "ghost"); err == nil {
		t.Fatal("approve of unknown patch should fail (Rule 3: no fake mutation)")
	}
	if _, err := x.Reject(context.Background(), "ghost", "x"); err == nil {
		t.Fatal("reject of unknown patch should fail (Rule 3: no fake mutation)")
	}
}

func lastFinished(t *testing.T, c *eventCollector) events.DomainEvent {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i].Type() == events.EventExecutionFinished {
			return c.events[i]
		}
	}
	t.Fatal("no ExecutionFinished event captured")
	return nil
}

func mustRead(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
