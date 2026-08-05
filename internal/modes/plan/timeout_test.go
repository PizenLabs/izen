package plan

import (
	"context"
	"io"
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

// TestProcessFromLedger_FreeModelTimeoutFailsFast is the regression guard for
// the "OpenRouter free-tier hangs for minutes" symptom: a free model whose
// provider produces zero bytes must be cut off by the strict per-attempt
// deadline and fail fast with an explicit error (NOT a heuristic plan mined
// from the ledger — the heuristic fallback is hard-killed).
func TestProcessFromLedger_FreeModelTimeoutFailsFast(t *testing.T) {
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

	if err == nil {
		t.Fatal("free-model timeout must fail fast with an explicit error, not a heuristic plan")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout did not fail fast: elapsed = %v", elapsed)
	}
	if len(tasks) != 0 {
		t.Fatalf("timeout must not yield heuristic tasks: %+v", tasks)
	}
}

// TestProcessFromLedger_FreeModelTimeoutEmitsNoFallbackEvent verifies the
// plan.synthesize.fallback event is NEVER published on the timeout fast-fail
// path — the heuristic fallback is dead.
func TestProcessFromLedger_FreeModelTimeoutEmitsNoFallbackEvent(t *testing.T) {
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

	_, _ = e.ProcessFromLedger(context.Background(),
		"ledger: internal/parser/stream.go:40:3 error",
		"fix it", "cohere/north-mini-code:free")

	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		mu.Lock()
		n := timeoutFallbacks
		mu.Unlock()
		if n >= 1 {
			t.Fatalf("timeout fallback event emitted %d time(s) — heuristic fallback must be dead", n)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestProcessFromLedger_FreeModelTimeoutNonStreaming covers the same fail-fast
// contract on the non-streaming provider path: a provider that blocks until the
// attempt context fires must be cut off and fail with an explicit error.
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

	if err == nil {
		t.Fatal("non-streaming timeout must fail with an explicit error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout did not fail fast: elapsed = %v", elapsed)
	}
	if len(tasks) != 0 {
		t.Fatalf("timeout must not yield heuristic tasks: %+v", tasks)
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
// per-attempt deadline fires AFTER partial prose arrived on the stream, the
// engine still fails fast with an explicit error — it does NOT mine the partial
// prose for a heuristic plan.
func TestProcessFromLedger_FreeModelPartialProseThenTimeout(t *testing.T) {
	orig := planAttemptTimeout
	planAttemptTimeout = 60 * time.Millisecond
	defer func() { planAttemptTimeout = orig }()

	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return &partialThenHangStream{ctx: ctx, chunk: "The fix lives in internal/retrieval/canonical.go at the mismatch parser."}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "ledger", "fix the mismatch", "cohere/north-mini-code:free")
	if err == nil {
		t.Fatal("partial-prose timeout must fail with an explicit error, not a heuristic plan")
	}
	if len(tasks) != 0 {
		t.Fatalf("partial-prose timeout must not yield heuristic tasks: %+v", tasks)
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
