package autonomy

import (
	"context"
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
	"github.com/PizenLabs/izen/internal/execution"
)

// mockProvider implements ai.Provider for adapter/driver tests. It records
// every ai.Request so tests can assert the ACTUAL contract sent to the model
// (system prompt, user message, max tokens) — not just recovery metadata.
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
	m.callCount++
	m.requests = append(m.requests, req)
	if m.callCount > len(m.responses) {
		return nil, fmt.Errorf("unexpected call #%d", m.callCount)
	}
	resp := m.responses[m.callCount-1]
	return resp, nil
}

func (m *mockProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

func (m *mockProvider) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// recordedRequests returns the ai.Request contract of every invocation,
// oldest first.
func (m *mockProvider) recordedRequests() []ai.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ai.Request(nil), m.requests...)
}

// blockingProvider waits for ctx cancellation inside Execute so a test can
// cancel a real execution mid-flight.
type blockingProvider struct {
	started chan struct{}
	mu      sync.Mutex
	count   int
}

func (m *blockingProvider) Name() string { return "blocking" }

func (m *blockingProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	m.count++
	m.mu.Unlock()
	select {
	case <-m.started:
	default:
		close(m.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

// eventCollector captures loop.transition events race-free.
type eventCollector struct {
	mu     sync.Mutex
	events []events.DomainEvent
}

func (c *eventCollector) add(ev events.DomainEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *eventCollector) loopTransitions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.events {
		if ev.Type() == events.EventLoopTransition {
			n++
		}
	}
	return n
}

func (c *eventCollector) hasTransition(from, to string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Type() != events.EventLoopTransition {
			continue
		}
		p, ok := ev.Payload().(events.LoopTransitionPayload)
		if ok && p.From == from && p.To == to {
			return true
		}
	}
	return false
}

func (c *eventCollector) waitTransitions(n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.loopTransitions() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c.loopTransitions() >= n
}

// testExecutor mirrors the execution-package harness: a real RuntimeExecutor
// over a mock provider with a trivial always-true verifier and a fresh
// authorization grant.
func testExecutor(t *testing.T, root string, mock ai.Provider, bus *events.Bus) *execution.RuntimeExecutor {
	t.Helper()
	cfg := config.Default()
	x := execution.NewRuntimeExecutor(root, cfg, mock, bus, "")
	v := execution.NewVerifier(root)
	v.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	x.SetVerifier(v)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

func testHarness(t *testing.T, responses []*ai.Response) (string, *mockProvider, *ExecutorAdapter, *events.Bus) {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: responses}
	x := testExecutor(t, root, mock, bus)
	return root, mock, NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus
}

func writeTarget(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTarget(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const sampleOriginal = "foo\nbar\nbaz\n"

const sampleReplace = `<<<<<<< SEARCH
bar
=======
qux
>>>>>>>`
