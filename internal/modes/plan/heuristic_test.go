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

// cohereNorthMiniProse is narrative output that a deterministic plan engine
// cannot parse as JSON. The legacy heuristic prose fallback (regex-mining
// these file mentions into FILE_MUTATE tasks) is HARD-KILLED: generation
// requests are owned by the intent compiler and this prose must surface an
// explicit error instead of a fabricated plan.
const cohereNorthMiniProse = `The user's page has a duplicated hero section. I should fix index.html
by removing the duplicate DOM node. The styles.css also needs a new class
for the focus state, and script.js should toggle the navigation menu.
After that I would run go build but this is not a Go project.`

// TestProcessFromLedger_HeuristicProseFallbackKilled is the regression guard
// for the hard-kill: a model that emits pure narrative prose must NOT recover
// FILE_MUTATE tasks via heuristic extraction, and must NOT emit the
// plan.synthesize.fallback presentation event. Instead it returns an explicit
// error the TUI surfaces as an escalation.
func TestProcessFromLedger_HeuristicProseFallbackKilled(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: cohereNorthMiniProse}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "command-r-08-2024")
	if err == nil {
		t.Fatal("narrative prose must produce an explicit error, not a heuristic plan")
	}
	if len(tasks) != 0 {
		t.Fatalf("prose must not yield heuristic tasks: %+v", tasks)
	}
	if strings.Contains(err.Error(), "heuristic") || strings.Contains(err.Error(), "extraction") {
		t.Errorf("error should not reference the heuristic fallback: %v", err)
	}
}

// TestProcessFromLedger_HeuristicProseFallbackEmitsNoEvent verifies the
// plan.synthesize.fallback event is NEVER published for prose output.
func TestProcessFromLedger_HeuristicProseFallbackEmitsNoEvent(t *testing.T) {
	bus := events.NewBus(32)
	defer bus.Close()

	var mu sync.Mutex
	var fallbackCount int
	bus.Subscribe(events.EventPlanFallback, func(ev events.DomainEvent) {
		mu.Lock()
		fallbackCount++
		mu.Unlock()
	})

	e := NewEngine(NewPlanStore()).WithEventBus(bus)
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: cohereNorthMiniProse}, nil
	})

	_, _ = e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "command-r-08-2024")

	// Give the non-blocking bus a moment to deliver (asserting zero is only
	// meaningful after the delivery window).
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		mu.Lock()
		n := fallbackCount
		mu.Unlock()
		if n > 0 {
			t.Fatalf("plan.synthesize.fallback event emitted %d time(s) — heuristic fallback must be dead", n)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestProcessFromLedger_RootContextFallbackKilled verifies that a narrative
// response with no detectable file produces an error, NOT the root-context
// CODE_MOD [Target 1/1] fallback task.
func TestProcessFromLedger_RootContextFallbackKilled(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: "I have analyzed the situation and will proceed step by step."}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "address the reported issue", "test-model")
	if err == nil {
		t.Fatal("undetectable prose must produce an explicit error, not a root-context fallback task")
	}
	if len(tasks) != 0 {
		t.Fatalf("got %d tasks, want 0: %+v", len(tasks), tasks)
	}
}

// TestProcessFromLedger_StreamedProseError confirms the hard-kill also applies
// on the streaming path: prose arriving through accumulateStream yields an
// explicit error, never heuristic tasks.
func TestProcessFromLedger_StreamedProseError(t *testing.T) {
	streamed := &mockStreamResult{data: cohereNorthMiniProse, finish: "stop"}
	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return streamed, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "test-model")
	if err == nil {
		t.Fatal("streamed prose must produce an explicit error")
	}
	if len(tasks) != 0 {
		t.Fatalf("got %d tasks, want 0: %+v", len(tasks), tasks)
	}
}

// TestProcessFromLedger_ProseFilesNoRootFallback is the regression guard for
// the empty-workspace case: prose naming files that do not exist on disk must
// produce an error, NOT fall back to a hardcoded root-context task.
func TestProcessFromLedger_ProseFilesNoRootFallback(t *testing.T) {
	root := t.TempDir() // empty — no styles.css / script.js on disk
	e := NewEngine(NewPlanStore())
	e.SetRootPath(root)
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: cohereNorthMiniProse}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "test-model")
	if err == nil {
		t.Fatal("prose targeting non-existent files must produce an explicit error")
	}
	if len(tasks) != 0 {
		t.Fatalf("got %d tasks, want 0: %+v", len(tasks), tasks)
	}
}

// TestRetryReinforcementForInvalidJSON verifies the schema-aware retry
// augmentation: a structural JSON failure re-emits the raw-JSON schema contract
// (NOT the shell-exec reinforcement that historically did nothing for a model
// emitting prose).
func TestRetryReinforcementForInvalidJSON(t *testing.T) {
	got := retryReinforcement(failureInvalidJSON, 1, 2)
	if !strings.Contains(got, "was NOT valid raw JSON") {
		t.Errorf("JSON reinforcement missing rejection reason: %q", got)
	}
	if !strings.Contains(got, "atomic_tasks") {
		t.Errorf("JSON reinforcement must re-emit the schema (atomic_tasks): %q", got)
	}
	if strings.Contains(got, "SHELL_EXEC target") {
		t.Errorf("JSON reinforcement must not use the shell-exec text: %q", got)
	}
}

// TestRetryReinforcementForFilteredCandidates verifies the grounded-target
// augmentation is used when valid JSON was fully rejected by the filters.
func TestRetryReinforcementForFilteredCandidates(t *testing.T) {
	got := retryReinforcement(failureFilteredCandidates, 2, 2)
	if !strings.Contains(got, "do not exist") {
		t.Errorf("filtered-candidates reinforcement missing grounding instruction: %q", got)
	}
}

// TestProcessFromLedger_SchemaReinforcementOnRetry is the end-to-end regression
// guard for the JSON contract re-enforcement: a model that emits invalid JSON on
// its first attempt receives the strict schema reinforcement on the retry, and a
// compliant second attempt succeeds — proving the "all 3 JSON synthesis attempts
// failed" error is only reachable when no attempt is recoverable.
func TestProcessFromLedger_SchemaReinforcementOnRetry(t *testing.T) {
	var mu sync.Mutex
	var callCount int
	var retryPrompt string

	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		mu.Lock()
		callCount++
		if callCount == 2 {
			retryPrompt = req.Messages[len(req.Messages)-1].Content
		}
		mu.Unlock()
		if callCount == 1 {
			return &ai.Response{Content: "I should fix the duplicate hero section in index.html and add focus states."}, nil
		}
		return &ai.Response{Content: `{"context_anchor":{"source":"user","target_packages":[]},"architectural_strategy":"remove duplicate node","atomic_tasks":[{"task_id":1,"file":"index.html","strategy":"FILE_MUTATE","description":"remove duplicate hero","rationale":"confirmed duplicate","solution":"single hero"}]}`}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger must succeed after schema-reinforced retry: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1: %+v", len(tasks), tasks)
	}

	mu.Lock()
	defer mu.Unlock()
	if callCount < 2 {
		t.Fatalf("provider called %d times, want >= 2 (initial + retry)", callCount)
	}
	if !strings.Contains(retryPrompt, "was NOT valid raw JSON") {
		t.Errorf("retry prompt missing the JSON schema reinforcement: %q", retryPrompt)
	}
}
