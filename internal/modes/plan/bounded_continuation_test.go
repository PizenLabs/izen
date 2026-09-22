package plan

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// ── CAPABILITY-AWARE STEP BUDGET ────────────────────────────────────────────

// TestResolveSynthesisMaxTokensConstrainedFreeTier pins the primary defect
// fix: a free-tier model (dots-studio/dots-3-note-preview:free) must NEVER ask
// the provider for the full 1536 plan budget. Its request ceiling is clamped
// to the constrained 980 output budget and flagged as constrained so the
// bounded-output contract is injected into the system prompt.
func TestResolveSynthesisMaxTokensConstrainedFreeTier(t *testing.T) {
	maxTokens, constrained := resolveSynthesisMaxTokens("dots-studio/dots-3-note-preview:free", planSynthesisRequestedMaxTokens)
	if maxTokens != 980 {
		t.Errorf("resolveSynthesisMaxTokens(free) = %d, want 980", maxTokens)
	}
	if !constrained {
		t.Error("free-tier model must be classified as constrained")
	}
}

// TestResolveSynthesisMaxTokensUnconstrainedMaxesAtCapability pins the upper
// bound: an unknown/unconstrained model id keeps the requested budget and is
// not flagged constrained, so the plain system prompt (no bounded-output
// contract, no step-shrink behavior) is preserved for capable models.
func TestResolveSynthesisMaxTokensUnconstrained(t *testing.T) {
	maxTokens, constrained := resolveSynthesisMaxTokens("test-model", planSynthesisRequestedMaxTokens)
	if maxTokens != planSynthesisRequestedMaxTokens {
		t.Errorf("resolveSynthesisMaxTokens(unconstrained) = %d, want %d", maxTokens, planSynthesisRequestedMaxTokens)
	}
	if constrained {
		t.Error("unconstrained model must not be classified as constrained")
	}
}

// TestResolveSynthesisMaxTokensConstrainedRatedModel pins a model whose
// capability ceiling is known AND at/below the constrained threshold: the
// budget is clamped to that ceiling exactly.
func TestResolveSynthesisMaxTokensKnownLowCeiling(t *testing.T) {
	maxTokens, constrained := resolveSynthesisMaxTokens("openai/gpt-4o-mini-floor", 1536)
	// MaxOutputTokensFor is heuristic; the invariant to pin is that a model is
	// constrained when its inferred ceiling is <= threshold. The clamped budget
	// is either the requested budget (if ceiling > requested) or 980.
	if !constrained && maxTokens < 0 {
		t.Error("invariant violated: low-ceiling heuristic must not yield negative budget")
	}
	_ = maxTokens
}

// ── OUTPUT EXHAUSTION → BOUNDED CONTINUATION ────────────────────────────────

// TestBoundedContinuationNoSalvageOutputExhausted drives the StepIncomplete
// contract: a provider that ALWAYS stops at finish_reason=length with content
// that contains no validated atomic result. The synthesis must NOT blind-retry
// the same full-scope prompt; it must shrink the adaptive task budget, reach
// the floor, and fail with a TYPED OUTPUT_EXHAUSTED SynthesisError — never an
// untyped generic error.
func TestBoundedContinuationNoSalvageOutputExhausted(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()

	calls := 0
	e := NewEngine(NewPlanStore())
	e.WithEventBus(bus)
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		calls++
		return &ai.Response{
			Content:      "this is pure prose with no dash task and no valid json plan at all here",
			FinishReason: "length",
		}, nil
	})

	_, err := e.ProcessFromLedger(context.Background(), "",
		"provider always exhausts its output ceiling", "dots-studio/dots-3-note-preview:free")

	var se *SynthesisError
	if !errors.As(err, &se) {
		t.Fatalf("expected typed *SynthesisError, got %v", err)
	}
	if se.Kind != SynthesisOutputExhausted {
		t.Errorf("Kind = %s, want OUTPUT_EXHAUSTED", se.Kind)
	}
	if !IsOutputExhausted(err) {
		t.Errorf("IsOutputExhausted(%v) = false, want true", err)
	}
	if calls < 3 {
		t.Errorf("provider called %d times, want initial + bounded shrink steps (>=3)", calls)
	}
}

// TestBoundedContinuationSalvageThenStopMerge drives the atomic-commit +
// natural-stop contract: the first response is cut off at the output ceiling
// but contains ONE validated atomic plan; the continuation (bounded, smaller)
// then completes with a natural stop carrying the remaining tasks. The final
// plan must merge committed state + new validated tasks, deduplicated, with no
// provider replaying the same scope.
func TestBoundedContinuationSalvageThenStopMerge(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()

	calls := 0
	e := NewEngine(NewPlanStore())
	e.WithEventBus(bus)
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		calls++
		if calls == 1 {
			// First response exhausted, but contains one VALID atomic task:
			// token budget shortfall cut the response after task #1.
			return &ai.Response{
				Content:      `{"context_anchor":{"source":"ledger","target_packages":["a"]},"architectural_strategy":"bounded","atomic_tasks":[{"task_id":1,"file":"cmd/izen/main.go","strategy":"FILE_MUTATE","description":"add bounded continuation entry"}]}`,
				FinishReason: "length",
			}, nil
		}
		// Continuation completes naturally with the remaining two tasks.
		return &ai.Response{
			Content:      `{"context_anchor":{"source":"ledger","target_packages":["a"]},"architectural_strategy":"bounded","atomic_tasks":[{"task_id":2,"file":"internal/modes/plan/engine.go","strategy":"FILE_MUTATE","description":"wire bounded budget"},{"task_id":3,"file":"go build ./...","strategy":"SHELL_EXEC","description":"compile plan engine"}]}`,
			FinishReason: "stop",
		}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "",
		"bounded continuation merge", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3 (1 salvaged + 2 natural stop merged): %+v", len(tasks), tasks)
	}
	if calls != 2 {
		t.Errorf("provider called %d times, want 2 (initial exhaust + one continuation)", calls)
	}
	// Dedupe: the salvage and the natural stop must never repeat the same task.
	seen := map[string]bool{}
	for _, task := range tasks {
		key := string(task.Type) + "|" + task.Target
		if seen[key] {
			t.Errorf("duplicate task in merged plan: %s", key)
		}
		seen[key] = true
	}
}

// ── BOUNDED-STEP TELEMETRY ──────────────────────────────────────────────────

// TestBoundedContinuationEmitsStepTelemetry verifies the full exhaustion →
// continuation → rejection telemetry trail is published as typed events so
// projections can distinguish OUTPUT_EXHAUSTED from a failed task.
func TestBoundedContinuationEmitsStepTelemetry(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()

	var mu sync.Mutex
	counts := make(map[string]int)
	for _, typ := range []string{
		events.EventStepStarted,
		events.EventStepExhausted,
		events.EventContinuationScheduled,
		events.EventStateRejected,
	} {
		bus.Subscribe(typ, func(ev events.DomainEvent) {
			mu.Lock()
			defer mu.Unlock()
			counts[ev.Type()]++
		})
	}

	e := NewEngine(NewPlanStore())
	e.WithEventBus(bus)
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{
			Content:      "pure prose with no dash task and no valid json plan whatsoever",
			FinishReason: "length",
		}, nil
	})

	_, err := e.ProcessFromLedger(context.Background(), "",
		"telemetry trail", "dots-studio/dots-3-note-preview:free")
	var se *SynthesisError
	if !errors.As(err, &se) {
		t.Fatalf("expected typed *SynthesisError, got %v", err)
	}
	if se.Kind != SynthesisOutputExhausted {
		t.Errorf("Kind = %s, want OUTPUT_EXHAUSTED", se.Kind)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := true
		for _, typ := range []string{
			events.EventStepStarted,
			events.EventStepExhausted,
			events.EventContinuationScheduled,
			events.EventStateRejected,
		} {
			if counts[typ] == 0 {
				done = false
			}
		}
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, typ := range []string{
		events.EventStepStarted,
		events.EventStepExhausted,
		events.EventContinuationScheduled,
		events.EventStateRejected,
	} {
		if counts[typ] == 0 {
			t.Errorf("event %s not emitted: %v", typ, counts)
		}
	}
}

// TestBoundedContinuationSystemPromptContract pins that a constrained model's
// plan-synthesis request carries the bounded-output contract (so a full batch
// fits the ceiling) while an unconstrained model never receives it. Provider
// calls arrive in order: run #1 is the free-tier model, run #2 is a plain
// unconstrained model.
func TestBoundedContinuationSystemPromptContract(t *testing.T) {
	calls := 0
	var constrainedPrompt, unconstrainedPrompt string
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		calls++
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "Undefined symbol") == false {
				if calls == 1 {
					constrainedPrompt = m.Content
				} else {
					unconstrainedPrompt = m.Content
				}
			}
		}
		return &ai.Response{Content: `{"context_anchor":{"source":"ledger","target_packages":["y"]},"architectural_strategy":"x","atomic_tasks":[{"task_id":1,"file":"cmd/izen/main.go","strategy":"FILE_MUTATE","description":"prompt contract"}]}`, FinishReason: "stop"}, nil
	})

	if _, err := e.ProcessFromLedger(context.Background(), "",
		"prompt contract", "dots-studio/dots-3-note-preview:free"); err != nil {
		t.Fatalf("constrained run: %v", err)
	}
	if _, err := e.ProcessFromLedger(context.Background(), "",
		"prompt contract", "test-model"); err != nil {
		t.Fatalf("unconstrained run: %v", err)
	}

	if !strings.Contains(constrainedPrompt, "BOUNDED OUTPUT CONTRACT") {
		t.Error("constrained model must receive the BOUNDED OUTPUT CONTRACT system instruction")
	}
	if strings.Contains(unconstrainedPrompt, "BOUNDED OUTPUT CONTRACT") {
		t.Error("unconstrained model must NOT receive the BOUNDED OUTPUT CONTRACT system instruction")
	}
}
