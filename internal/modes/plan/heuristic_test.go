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

// Cohere North Mini style narrative output: pure reasoning prose that a
// deterministic plan engine should not be able to parse as JSON.
const cohereNorthMiniProse = `The user's page has a duplicated hero section. I should fix index.html
by removing the duplicate DOM node. The styles.css also needs a new class
for the focus state, and script.js should toggle the navigation menu.
After that I would run go build but this is not a Go project.`

func TestExtractTasksFromProse_DetectsFiles(t *testing.T) {
	tasks := extractTasksFromProse(cohereNorthMiniProse)
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3 (index.html, styles.css, script.js): %+v", len(tasks), tasks)
	}
	wantTargets := []string{"index.html", "styles.css", "script.js"}
	for i, want := range wantTargets {
		if tasks[i].Type != "FILE_MUTATE" {
			t.Errorf("task %d type = %q, want FILE_MUTATE", i, tasks[i].Type)
		}
		if tasks[i].Target != want {
			t.Errorf("task %d target = %q, want %q", i, tasks[i].Target, want)
		}
		if tasks[i].Description == "" {
			t.Errorf("task %d description is empty", i)
		}
		if tasks[i].IsHardcoded {
			t.Errorf("task %d must not be hardcoded (prose-derived files need disk validation)", i)
		}
	}
}

func TestExtractTasksFromProse_DerivesNearbyDescription(t *testing.T) {
	raw := "The duplicate DOM node lives in index.html so I will remove it."
	tasks := extractTasksFromProse(raw)
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	desc := tasks[0].Description
	if !strings.Contains(desc, "duplicate DOM node") {
		t.Errorf("description %q should carry the nearby text", desc)
	}
	if strings.Contains(desc, "index.html") {
		t.Errorf("description %q should not repeat the file token", desc)
	}
}

func TestExtractTasksFromProse_RelativePaths(t *testing.T) {
	raw := "The bug is in cmd/api/main.go and the parser in internal/parser/stream.go."
	tasks := extractTasksFromProse(raw)
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2: %+v", len(tasks), tasks)
	}
	if tasks[0].Target != "cmd/api/main.go" {
		t.Errorf("task 0 target = %q, want cmd/api/main.go", tasks[0].Target)
	}
	if tasks[1].Target != "internal/parser/stream.go" {
		t.Errorf("task 1 target = %q, want internal/parser/stream.go", tasks[1].Target)
	}
}

func TestExtractTasksFromProse_NoFalsePositiveOnLongerWord(t *testing.T) {
	// "myindex.html" must not match "index.html" as a substring.
	raw := "Check myindex.html configuration before touching anything else."
	tasks := extractTasksFromProse(raw)
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want exactly 1: %+v", len(tasks), tasks)
	}
	if tasks[0].Target != "myindex.html" {
		t.Errorf("target = %q, want myindex.html", tasks[0].Target)
	}
}

func TestExtractTasksFromProse_Deduplicates(t *testing.T) {
	raw := "Fix index.html. Also fix index.html again and script.js once more."
	tasks := extractTasksFromProse(raw)
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2 (deduplicated): %+v", len(tasks), tasks)
	}
}

func TestExtractTasksFromProse_RootContextFallback(t *testing.T) {
	raw := "I have thought carefully about the problem and the best approach is a staged rollout."
	tasks := extractTasksFromProse(raw)
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 root-context fallback", len(tasks))
	}
	if tasks[0].Target != rootContextFallbackTarget {
		t.Errorf("target = %q, want root-context fallback target", tasks[0].Target)
	}
	if tasks[0].Type != "FILE_MUTATE" {
		t.Errorf("type = %q, want FILE_MUTATE", tasks[0].Type)
	}
	if !tasks[0].IsHardcoded {
		t.Error("root-context fallback must be hardcoded so it survives filters")
	}
}

func TestExtractTasksFromProse_Empty(t *testing.T) {
	if tasks := extractTasksFromProse(""); tasks != nil {
		t.Fatalf("got %+v, want nil for empty input", tasks)
	}
	if tasks := extractTasksFromProse("   \n  "); tasks != nil {
		t.Fatalf("got %+v, want nil for whitespace-only input", tasks)
	}
}

// TestProcessFromLedger_HeuristicProseFallback is the end-to-end regression
// guard for the "all 3 JSON synthesis attempts failed" failure: when a model
// emits pure narrative prose, the plan engine must recover FILE_MUTATE tasks
// via heuristic extraction instead of returning an error.
func TestProcessFromLedger_HeuristicProseFallback(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: cohereNorthMiniProse}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "command-r-08-2024")
	if err != nil {
		t.Fatalf("ProcessFromLedger with narrative prose must not fail: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3 heuristic FILE_MUTATE tasks: %+v", len(tasks), tasks)
	}
	for _, task := range tasks {
		if task.Type != "FILE_MUTATE" {
			t.Errorf("task type = %q, want FILE_MUTATE", task.Type)
		}
		if task.Target == "" {
			t.Error("heuristic task has empty target")
		}
	}
}

// TestProcessFromLedger_HeuristicProseFallbackEmitsEvent verifies the
// plan.synthesize.fallback PresentationEvent is published when the heuristic
// prose fallback generates the plan.
func TestProcessFromLedger_HeuristicProseFallbackEmitsEvent(t *testing.T) {
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

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "command-r-08-2024")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected heuristic tasks")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := fallbackCount
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("plan.synthesize.fallback event not emitted (count=%d)", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestProcessFromLedger_RootContextFallbackNoError verifies that a narrative
// response with no detectable file still produces a root-context fallback task
// instead of an unrecoverable synthesis error.
func TestProcessFromLedger_RootContextFallbackNoError(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: "I have analyzed the situation and will proceed step by step."}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "address the reported issue", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger with undetectable prose must not fail: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 root-context fallback: %+v", len(tasks), tasks)
	}
	if tasks[0].Target != rootContextFallbackTarget {
		t.Errorf("target = %q, want root-context fallback target", tasks[0].Target)
	}
}

// TestProcessFromLedger_StreamedProseFallback confirms the heuristic fallback
// also works on the streaming path, where prose arrives through accumulateStream
// (including prose promoted from the reasoning fallback when content is empty).
func TestProcessFromLedger_StreamedProseFallback(t *testing.T) {
	streamed := &mockStreamResult{data: cohereNorthMiniProse, finish: "stop"}
	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return streamed, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the duplicated hero section", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger with streamed prose: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3: %+v", len(tasks), tasks)
	}
}
