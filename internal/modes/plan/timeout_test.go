package plan

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

func TestUseStrictAttemptTimeout(t *testing.T) {
	cases := map[string]bool{
		"cohere/north-mini-code:free": true,
		"openai/gpt-4o-mini:free":     true,
		"deepseek/deepseek-r1:free":   true,
		"some-vendor/model-free":      true,
		"anthropic/claude-3.5-sonnet": false,
		"openai/gpt-4o":               false,
		"qwen2.5-coder:7b":            false,
		"llama3.2:3b":                 false,
		"":                            false,
	}
	for model, want := range cases {
		if got := useStrictAttemptTimeout(model); got != want {
			t.Errorf("useStrictAttemptTimeout(%q) = %v, want %v", model, got, want)
		}
	}
}

// hangingStream is a streaming provider result that blocks on Read until the
// attempt context fires — the exact failure mode of a queued/cold-started
// OpenRouter free-tier model that produces no bytes.
type hangingStream struct {
	ctx context.Context
}

func (h *hangingStream) Read(p []byte) (int, error) {
	<-h.ctx.Done()
	return 0, h.ctx.Err()
}

func (h *hangingStream) Close() error { return nil }

func (h *hangingStream) Usage() (int, int) { return 0, 0 }

func (h *hangingStream) FinishReason() string { return "" }

var _ ai.FinishReasonProvider = (*hangingStream)(nil)

// TestProcessFromLedger_FreeModelTimeoutFailsFastToHeuristic is the regression
// guard for the "OpenRouter free-tier hangs for minutes" symptom: a free model
// (`cohere/north-mini-code:free`) whose provider produces zero bytes must be
// cut off by the strict per-attempt deadline and fall back to heuristic task
// extraction derived from the investigation ledger — within a couple hundred
// milliseconds, not the multi-minute provider hang.
func TestProcessFromLedger_FreeModelTimeoutFailsFastToHeuristic(t *testing.T) {
	orig := planAttemptTimeout
	planAttemptTimeout = 60 * time.Millisecond
	defer func() { planAttemptTimeout = orig }()

	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return &hangingStream{ctx: ctx}, nil
	})

	start := time.Now()
	tasks, err := e.ProcessFromLedger(context.Background(),
		"Investigation ledger mentions cmd/api/main.go:12:5 as the failing coordinate.",
		"fix the reported issue", "cohere/north-mini-code:free")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("free-model timeout must fall back to heuristic, got error: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout did not fail fast: elapsed = %v", elapsed)
	}
	if len(tasks) == 0 {
		t.Fatal("heuristic fallback produced no tasks")
	}
	if tasks[0].Target != "cmd/api/main.go" {
		t.Errorf("heuristic task target = %q, want cmd/api/main.go (mined from ledger): %+v", tasks[0].Target, tasks)
	}
}

// TestProcessFromLedger_FreeModelTimeoutEmitsFallbackEvent verifies the
// plan.synthesize.fallback PresentationEvent is published on the timeout
// fast-fail path so the presentation layer can notify the user.
func TestProcessFromLedger_FreeModelTimeoutEmitsFallbackEvent(t *testing.T) {
	orig := planAttemptTimeout
	planAttemptTimeout = 60 * time.Millisecond
	defer func() { planAttemptTimeout = orig }()

	bus := events.NewBus(16)
	defer bus.Close()

	var mu sync.Mutex
	var timeoutFallbacks int
	bus.Subscribe(events.EventPlanFallback, func(ev events.DomainEvent) {
		mu.Lock()
		if p := ev.Payload(); p != nil {
			if k, ok := p.(events.PlanFallbackPayload); ok && k.Kind == "timeout" {
				timeoutFallbacks++
			}
		}
		mu.Unlock()
	})

	e := NewEngine(NewPlanStore()).WithEventBus(bus)
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return &hangingStream{ctx: ctx}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(),
		"ledger: internal/parser/stream.go:40:3 error",
		"fix it", "cohere/north-mini-code:free")
	if err != nil {
		t.Fatalf("timeout fallback must not error: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected heuristic tasks")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := timeoutFallbacks
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout fallback event not emitted (count=%d)", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestProcessFromLedger_FreeModelTimeoutNonStreaming covers the same fail-fast
// contract on the non-streaming provider path: a provider that blocks until the
// attempt context fires must be cut off and fall back to the heuristic plan.
func TestProcessFromLedger_FreeModelTimeoutNonStreaming(t *testing.T) {
	orig := planAttemptTimeout
	planAttemptTimeout = 60 * time.Millisecond
	defer func() { planAttemptTimeout = orig }()

	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	start := time.Now()
	tasks, err := e.ProcessFromLedger(context.Background(),
		"ledger: src/index.html has the duplicate hero section",
		"fix it", "cohere/north-mini-code:free")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("non-streaming timeout must fall back to heuristic, got error: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout did not fail fast: elapsed = %v", elapsed)
	}
	if len(tasks) == 0 {
		t.Fatal("heuristic fallback produced no tasks")
	}
}

// TestProcessFromLedger_PaidModelKeepsParentBudget pins the discriminator: a
// PAID cloud model (no ":free" marker) must NOT be cut off by the strict
// per-attempt deadline. A slow-but-alive provider that eventually answers
// (here after 120ms, well past the test's would-be 60ms budget) still succeeds
// because the strict timeout never applies.
func TestProcessFromLedger_PaidModelKeepsParentBudget(t *testing.T) {
	orig := planAttemptTimeout
	planAttemptTimeout = 60 * time.Millisecond
	defer func() { planAttemptTimeout = orig }()

	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		// Simulate a slow paid model: sleeps past the strict budget, then
		// answers with valid plan JSON.
		select {
		case <-time.After(120 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &ai.Response{Content: validPlanJSON}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "ledger", "plan the change", "anthropic/claude-3.5-sonnet")
	if err != nil {
		t.Fatalf("paid model must not be cut off by the strict budget: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2 (valid plan JSON must be honored): %+v", len(tasks), tasks)
	}
}

// TestProcessFromLedger_FreeModelPartialProseThenTimeout verifies that when the
// per-attempt deadline fires AFTER partial prose arrived on the stream, that
// partial content is what the heuristic mines — not a hard error and not an
// empty plan.
func TestProcessFromLedger_FreeModelPartialProseThenTimeout(t *testing.T) {
	orig := planAttemptTimeout
	planAttemptTimeout = 60 * time.Millisecond
	defer func() { planAttemptTimeout = orig }()

	// The stream yields one prose chunk mentioning a file, then blocks.
	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return &partialThenHangStream{ctx: ctx, chunk: "The fix lives in internal/retrieval/canonical.go at the mismatch parser."}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "ledger", "fix the mismatch", "cohere/north-mini-code:free")
	if err != nil {
		t.Fatalf("partial-prose timeout must fall back to heuristic, got error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 mined from the partial prose: %+v", len(tasks), tasks)
	}
	if tasks[0].Target != "internal/retrieval/canonical.go" {
		t.Errorf("target = %q, want internal/retrieval/canonical.go: %+v", tasks[0].Target, tasks)
	}
}

// partialThenHangStream returns one chunk then blocks on Read until the attempt
// context fires, simulating a provider that streams a little prose and stalls.
type partialThenHangStream struct {
	ctx   context.Context
	chunk string
	pos   int
}

func (p *partialThenHangStream) Read(buf []byte) (int, error) {
	if p.pos < len(p.chunk) {
		n := copy(buf, p.chunk[p.pos:])
		p.pos += n
		return n, nil
	}
	<-p.ctx.Done()
	return 0, p.ctx.Err()
}

func (p *partialThenHangStream) Close() error { return nil }

func (p *partialThenHangStream) Usage() (int, int) { return 0, 0 }

func (p *partialThenHangStream) FinishReason() string { return "" }

var _ ai.FinishReasonProvider = (*partialThenHangStream)(nil)

// TestExtractTasksFromLedger verifies the ledger miner builds one FILE_MUTATE
// task per detected source file from investigation data.
func TestExtractTasksFromLedger(t *testing.T) {
	ledger := "cmd/api/main.go:12:5: no required module provides package\ninternal/parser/stream.go:40:3: undefined: Symbol"
	tasks := extractTasksFromLedger(ledger)
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2: %+v", len(tasks), tasks)
	}
	if tasks[0].Target != "cmd/api/main.go" {
		t.Errorf("task 0 target = %q, want cmd/api/main.go", tasks[0].Target)
	}
	if tasks[1].Target != "internal/parser/stream.go" {
		t.Errorf("task 1 target = %q, want internal/parser/stream.go", tasks[1].Target)
	}
	for i, tk := range tasks {
		if tk.Type != "FILE_MUTATE" {
			t.Errorf("task %d type = %q, want FILE_MUTATE", i, tk.Type)
		}
		if strings.TrimSpace(tk.Description) == "" {
			t.Errorf("task %d description is empty", i)
		}
	}
}

func TestExtractTasksFromLedger_Empty(t *testing.T) {
	if tasks := extractTasksFromLedger(""); tasks != nil {
		t.Fatalf("got %+v, want nil for empty input", tasks)
	}
}
