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
)

// ── PHASE 4 — EXECUTION EVENT SEMANTICS ──────────────────────────────────────
//
// These tests pin the runtime event contract:
//
//  1. An artifact can never exist before the provider response that produced
//     it — artifact.produced is always after provider.response.
//  2. A failed model invocation produces NO artifact, NO provider.response and
//     NO misleading success outcome — a failed execution must never emit a
//     success artifact.
//  3. execution.finished is ALWAYS terminal: exactly one, emitted last, and
//     every execution reaches it.
//  4. ExecutionProof reflects the exact lifecycle order (started → strategy →
//     context → invocation → artifact → terminal).

// phase4Collector subscribes to the ENTIRE lifecycle stream through ONE
// SubscribeAll subscription so events arrive in publish order (a single FIFO
// channel + dispatch goroutine). Per-type subscriptions would race across
// dispatch goroutines and defeat ordering assertions.
type phase4Collector struct {
	mu     sync.Mutex
	kinds  []string
	events []events.DomainEvent
}

func newPhase4Collector(bus *events.Bus) *phase4Collector {
	c := &phase4Collector{}
	bus.SubscribeAll(func(ev events.DomainEvent) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.kinds = append(c.kinds, ev.Type())
		c.events = append(c.events, ev)
	})
	return c
}

func (c *phase4Collector) types() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.kinds...)
}

func (c *phase4Collector) count(typ string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.kinds {
		if t == typ {
			n++
		}
	}
	return n
}

// waitCount polls until at least n events of typ have arrived (async delivery).
func (c *phase4Collector) waitCount(typ string, n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.count(typ) >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c.count(typ) >= n
}

// indexOf returns the index of the first event of typ, or -1.
func (c *phase4Collector) indexOf(typ string) int {
	types := c.types()
	for i, t := range types {
		if t == typ {
			return i
		}
	}
	return -1
}

// timestampOf returns the timestamp of the first event of typ.
func (c *phase4Collector) timestampOf(typ string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Type() == typ {
			return ev.Timestamp(), true
		}
	}
	return time.Time{}, false
}

// failingProvider fails on the first call (model failure path).
type failingProvider struct {
	mu     sync.Mutex
	callCt int
}

func (m *failingProvider) Name() string { return "mock" }

func (m *failingProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	m.callCt++
	m.mu.Unlock()
	return nil, errors.New("model invocation exploded")
}

func (m *failingProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

func (m *failingProvider) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCt
}

func phase4Executor(t *testing.T, root string, p ai.Provider, bus *events.Bus) *RuntimeExecutor {
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

// TestModelFailureProducesNoArtifact pins rule 2: when the provider fails, the
// runtime must emit NO artifact.produced, NO provider.response and NO
// model.invoked record in the proof, and the proof outcome must be a clean
// failure — never a misleading success artifact.
func TestModelFailureProducesNoArtifact(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	prov := &failingProvider{}
	x := phase4Executor(t, root, prov, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "p1",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err == nil {
		t.Fatal("expected the provider failure to surface as an execution error")
	}
	if res == nil || res.Err == nil {
		t.Fatal("expected the result to carry the error")
	}
	collector.waitCount(events.EventExecutionFinished, 1, time.Second)

	for _, forbidden := range []string{
		events.EventArtifactProduced,
		events.EventProviderResponse,
		events.EventApprovalRequired,
	} {
		if collector.count(forbidden) != 0 {
			t.Errorf("forbidden event %q emitted on model failure; got %v", forbidden, collector.types())
		}
	}

	// The proof must not record a fake successful invocation.
	if len(res.Proof.ModelInvocations) != 0 {
		t.Errorf("proof recorded %d model invocations on failure, want 0: %+v",
			len(res.Proof.ModelInvocations), res.Proof.ModelInvocations)
	}
	if res.Proof.Outcome != OutcomePatchGenerationFailed && res.Proof.Outcome != OutcomeFailed {
		t.Errorf("proof outcome = %s, want a failure outcome", res.Proof.Outcome)
	}
	if res.ArtifactKind != "" || res.Content != "" {
		t.Errorf("failed execution carried an artifact: kind=%q content=%q", res.ArtifactKind, res.Content)
	}
	// No file was mutated.
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated on model failure: %q", got)
	}

	// model.invoked WAS emitted (the invocation began) — but it must never be
	// followed by an artifact.
	if collector.count(events.EventModelInvoked) != 1 {
		t.Errorf("model.invoked count = %d, want exactly 1 (the invocation began)", collector.count(events.EventModelInvoked))
	}
	if got := prov.calls(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
}

// TestArtifactOrderAfterProviderResponse pins rule 1: artifact.produced must
// always follow provider.response, which must always follow model.invoked. The
// proof graph and the event stream must agree.
func TestArtifactOrderAfterProviderResponse(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: sampleReplace,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 12, CompletionTokens: 6},
	}}}
	x := phase4Executor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "p2",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("expected a pending patch id (approval gate)")
	}

	// Wait for the terminal-adjacent events to flush, then assert order.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if collector.count(events.EventApprovalRequired) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mi := collector.indexOf(events.EventModelInvoked)
	pr := collector.indexOf(events.EventProviderResponse)
	ap := collector.indexOf(events.EventArtifactProduced)
	if mi < 0 || pr < 0 || ap < 0 {
		t.Fatalf("missing lifecycle events: model.invoked=%d provider.response=%d artifact.produced=%d; types=%v",
			mi, pr, ap, collector.types())
	}
	if mi >= pr || pr >= ap {
		t.Errorf("event order violated: model.invoked=%d provider.response=%d artifact.produced=%d — artifact must be after the provider response",
			mi, pr, ap)
	}

	// Timestamps must agree: artifact produced strictly after the response.
	artT, _ := collector.timestampOf(events.EventArtifactProduced)
	respT, _ := collector.timestampOf(events.EventProviderResponse)
	if artT.Before(respT) {
		t.Errorf("artifact timestamp %v precedes provider-response timestamp %v", artT, respT)
	}

	// Proof must carry the exact invocation evidence (authoritative usage).
	if len(res.Proof.ModelInvocations) != 1 {
		t.Fatalf("proof invocations = %d, want 1", len(res.Proof.ModelInvocations))
	}
	inv := res.Proof.ModelInvocations[0]
	if inv.TokenInput != 12 || inv.TokenOutput != 6 {
		t.Errorf("proof usage = (%d,%d), want (12,6)", inv.TokenInput, inv.TokenOutput)
	}
}

// TestExecutionFinishedAlwaysTerminal pins rule 3: every execution path emits
// exactly one execution.finished and it is always the LAST canonical lifecycle
// event. No lifecycle event may follow it.
func TestExecutionFinishedAlwaysTerminal(t *testing.T) {
	run := func(t *testing.T, setup func(root string, bus *events.Bus) *RuntimeExecutor, req ExecuteRequest, approve bool) {
		t.Helper()
		root := t.TempDir()
		writeTarget(t, root, "note.txt", sampleOriginal)
		bus := events.NewBus(events.DefaultBufferSize)
		collector := newPhase4Collector(bus)
		x := setup(root, bus)
		res, err := x.Execute(context.Background(), req)
		if err == nil && approve && res != nil && res.PendingPatchID != "" {
			_, err = x.Approve(context.Background(), res.PendingPatchID)
		}
		_ = err
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if collector.count(events.EventExecutionFinished) > 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		types := collector.types()
		if n := collector.count(events.EventExecutionFinished); n != 1 {
			t.Fatalf("execution.finished count = %d, want exactly 1; types=%v", n, types)
		}
		lastIdx := -1
		for i, typ := range types {
			if typ == events.EventExecutionFinished {
				lastIdx = i
			}
		}
		if lastIdx != len(types)-1 {
			t.Errorf("execution.finished is not terminal: index=%d len=%d types=%v", lastIdx, len(types), types)
		}
	}

	t.Run("targeted mutation approval", func(t *testing.T) {
		run(t, func(root string, bus *events.Bus) *RuntimeExecutor {
			mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
			return phase4Executor(t, root, mock, bus)
		}, ExecuteRequest{RequestID: "p3", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"}, true)
	})

	t.Run("read-only explanation", func(t *testing.T) {
		run(t, func(root string, bus *events.Bus) *RuntimeExecutor {
			mock := &mockProvider{responses: []*ai.Response{{Content: "the answer"}}}
			return phase4Executor(t, root, mock, bus)
		}, ExecuteRequest{RequestID: "p4", Mode: "ask", Prompt: "explain the login flow in @note.txt"}, false)
	})

	t.Run("model failure", func(t *testing.T) {
		run(t, func(root string, bus *events.Bus) *RuntimeExecutor {
			return phase4Executor(t, root, &failingProvider{}, bus)
		}, ExecuteRequest{RequestID: "p5", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"}, false)
	})

	t.Run("rejected mutation", func(t *testing.T) {
		root := t.TempDir()
		writeTarget(t, root, "note.txt", sampleOriginal)
		bus := events.NewBus(events.DefaultBufferSize)
		collector := newPhase4Collector(bus)
		mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
		x := phase4Executor(t, root, mock, bus)
		res, err := x.Execute(context.Background(), ExecuteRequest{RequestID: "p6", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if _, err := x.Reject(context.Background(), res.PendingPatchID, "no"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if collector.count(events.EventExecutionFinished) > 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		types := collector.types()
		if n := collector.count(events.EventExecutionFinished); n != 1 {
			t.Fatalf("execution.finished count = %d, want 1; types=%v", n, types)
		}
		for i, typ := range types {
			if typ == events.EventExecutionFinished && i != len(types)-1 {
				t.Errorf("execution.finished not terminal: index=%d len=%d types=%v", i, len(types), types)
			}
		}
	})
}

// TestDirectResponseZeroContext pins the Phase 4 casual-chat contract through
// the real executor: a greeting selects direct_response, compiles ZERO context
// channels, reads no target file, and completes as a read-only artifact.
func TestDirectResponseZeroContext(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "unrelated.go", "package main\n")

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	mock := &mockProvider{responses: []*ai.Response{{Content: "hi there!"}}}
	x := phase4Executor(t, root, mock, bus)

	// Route through the IntentGateway so strategy selection is authoritative.
	g := NewIntentGateway(root)
	req, det, err := g.Gate(context.Background(), "hi")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if string(det.Profile.Strategy) != "direct_response" {
		t.Fatalf("strategy = %s, want direct_response", det.Profile.Strategy)
	}

	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("Execute res.Err: %v", res.Err)
	}
	// Wait for the terminal event so every lifecycle event has been delivered.
	if !collector.waitCount(events.EventExecutionFinished, 1, time.Second) {
		t.Fatalf("execution.finished never delivered; types=%v", collector.types())
	}
	if res.Strategy != "direct_response" {
		t.Fatalf("result strategy = %s, want direct_response", res.Strategy)
	}
	if res.ArtifactKind != "response" {
		t.Fatalf("artifact kind = %s, want response", res.ArtifactKind)
	}
	if res.Content == "" {
		t.Fatal("direct response produced no content")
	}

	// Zero context channels on the canonical event.
	var gotChannels []string
	collector.mu.Lock()
	for _, ev := range collector.events {
		if p, ok := ev.Payload().(events.ContextPreparedPayload); ok {
			gotChannels = p.Channels
		}
	}
	collector.mu.Unlock()
	if len(gotChannels) != 0 {
		t.Fatalf("context channels = %v, want 0 (zero-context casual chat)", gotChannels)
	}

	// No target was resolved (nothing to read, no workspace scan).
	if len(res.Targets) != 0 {
		t.Fatalf("targets = %v, want none for casual chat", res.Targets)
	}
	if len(mock.requests) != 1 {
		t.Fatalf("provider requests = %d, want exactly 1", len(mock.requests))
	}
	// The provider prompt must not embed the unrelated target file.
	for _, r := range mock.requests {
		for _, msg := range r.Messages {
			if strings.Contains(msg.Content, "unrelated.go") || strings.Contains(msg.Content, "package main") {
				t.Fatalf("provider prompt leaked workspace file context: %q", msg.Content)
			}
		}
	}

	// Terminal read-only completion: finished once, success.
	if n := collector.count(events.EventExecutionFinished); n != 1 {
		t.Fatalf("execution.finished count = %d, want 1", n)
	}
	if res.Proof.Outcome != OutcomeCompleted {
		t.Fatalf("proof outcome = %s, want completed", res.Proof.Outcome)
	}
}

// TestProofGraphReflectsLifecycleOrder pins rule 4: the ExecutionProof graph
// must record stages in the exact lifecycle order — no artifact stage before
// the invocation, no mutation before the approval, and a terminal stage.
func TestProofGraphReflectsLifecycleOrder(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := phase4Executor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "p7",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stages := make([]string, 0, len(res.Proof.Graph))
	for _, step := range res.Proof.Graph {
		stages = append(stages, step.Stage)
	}
	order := map[string]int{}
	for i, s := range stages {
		order[s] = i
	}
	if order["strategy_selected"] > order["context_prepared"] {
		t.Errorf("proof graph: strategy_selected after context_prepared: %v", stages)
	}
	if order["context_prepared"] > order["artifact_produced"] {
		t.Errorf("proof graph: context_prepared after artifact_produced: %v", stages)
	}
	if _, ok := order["mutate"]; ok {
		t.Errorf("proof graph recorded mutate before approval: %v", stages)
	}
}
