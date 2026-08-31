package execution

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// INV-SESSION-10: runtime executions are correlated with session_id. The
// executor resolves the originating session at admission (explicit request
// value wins, otherwise the wired resolver) and stamps it onto the execution
// proof, the terminal result, the usage account, and the canonical lifecycle
// events (execution.started / execution.evidence).

// allEventCollector captures every event on the bus, including the terminal
// execution.evidence record.
type allEventCollector struct {
	mu     sync.Mutex
	events []events.DomainEvent
}

func subscribeAllEvents(bus *events.Bus) *allEventCollector {
	c := &allEventCollector{}
	bus.SubscribeAll(func(ev events.DomainEvent) {
		c.mu.Lock()
		c.events = append(c.events, ev)
		c.mu.Unlock()
	})
	return c
}

func (c *allEventCollector) waitHas(typ string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, ev := range c.events {
			if ev.Type() == typ {
				c.mu.Unlock()
				return true
			}
		}
		c.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (c *allEventCollector) payloads(typ string) []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []interface{}
	for _, ev := range c.events {
		if ev.Type() == typ {
			out = append(out, ev.Payload())
		}
	}
	return out
}

func TestRuntimeExecutor_SessionCorrelationFromResolver(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := subscribeAllEvents(bus)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := testExecutor(t, root, mock, bus)
	x.SetSessionResolver(func() string { return "sess-8f31a2" })

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "r1",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The request carried no explicit SessionID: the resolver must win.
	if res.SessionID != "sess-8f31a2" {
		t.Fatalf("res.SessionID = %q, want sess-8f31a2", res.SessionID)
	}
	if res.Proof.SessionID != "sess-8f31a2" {
		t.Fatalf("proof.SessionID = %q, want sess-8f31a2", res.Proof.SessionID)
	}
	if res.Completed.SessionID != "sess-8f31a2" {
		t.Fatalf("completed.SessionID = %q, want sess-8f31a2", res.Completed.SessionID)
	}

	if !collector.waitHas(events.EventExecutionStarted, time.Second) {
		t.Fatal("execution.started never fired")
	}
	for _, p := range collector.payloads(events.EventExecutionStarted) {
		sp, ok := p.(events.ExecutionStartedPayload)
		if !ok {
			t.Fatalf("execution.started payload type %T", p)
		}
		if sp.SessionID != "sess-8f31a2" {
			t.Errorf("execution.started session_id = %q, want sess-8f31a2", sp.SessionID)
		}
	}

	// Approve: the terminal evidence must keep the originating session.
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if apr.SessionID != "sess-8f31a2" {
		t.Fatalf("approve result SessionID = %q, want sess-8f31a2", apr.SessionID)
	}
	if !collector.waitHas(events.EventExecutionEvidence, time.Second) {
		t.Fatal("execution.evidence never fired")
	}
	for _, p := range collector.payloads(events.EventExecutionEvidence) {
		ep, ok := p.(events.ExecutionEvidencePayload)
		if !ok {
			t.Fatalf("execution.evidence payload type %T", p)
		}
		if ep.SessionID != "sess-8f31a2" {
			t.Errorf("execution.evidence session_id = %q, want sess-8f31a2", ep.SessionID)
		}
	}
}

func TestRuntimeExecutor_ExplicitSessionIDWinsOverResolver(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	x := testExecutor(t, root, &mockProvider{responses: []*ai.Response{{Content: "hello"}}}, nil)
	// A resolver that would corrupt correlation must never win over the
	// explicit request value.
	x.SetSessionResolver(func() string { return "sess-WRONG" })

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "r2",
		Mode:            "ask",
		Prompt:          "hello",
		SessionID:       "sess-explicit",
		MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.SessionID != "sess-explicit" {
		t.Fatalf("res.SessionID = %q, want sess-explicit", res.SessionID)
	}
	if res.Proof.SessionID != "sess-explicit" {
		t.Fatalf("proof.SessionID = %q, want sess-explicit", res.Proof.SessionID)
	}
}

// TestRuntimeExecutor_NoSessionResolverLeavesEmpty pins that a harness without
// a session authority still executes cleanly with an empty correlation.
func TestRuntimeExecutor_NoSessionResolverLeavesEmpty(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	x := testExecutor(t, root, &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}, nil)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "r3",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.SessionID != "" {
		t.Fatalf("res.SessionID = %q, want empty", res.SessionID)
	}
}
