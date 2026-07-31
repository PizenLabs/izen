package review

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// TestRunCleanRepoPublishesCommandReceived covers the headless event emission
// of the review engine through the deterministic clean-tree exit path: Run()
// publishes CommandReceived and returns immediately with the "no changes to
// review" error, so StageCompleted/ExecutionFailed must not fire.
func TestRunCleanRepoPublishesCommandReceived(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	bus := events.NewBus(32)
	defer bus.Close()

	e := NewEngine(dir, &mockRetriever{}, nil)
	e.WithEventBus(bus)

	var mu sync.Mutex
	var got []events.DomainEvent
	for _, typ := range []string{
		events.EventCommandReceived,
		events.EventStageCompleted,
		events.EventExecutionFailed,
	} {
		bus.Subscribe(typ, func(ev events.DomainEvent) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, ev)
		})
	}

	result, err := e.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error != "no changes to review — working tree is clean" {
		t.Errorf("unexpected error: %q", result.Error)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: got %d events, want >= 1", n)
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	types := make(map[string]int, len(got))
	for _, ev := range got {
		types[ev.Type()]++
	}
	if types[events.EventCommandReceived] != 1 {
		t.Errorf("CommandReceived emitted %d times, want 1", types[events.EventCommandReceived])
	}
	if types[events.EventStageCompleted] != 0 {
		t.Errorf("StageCompleted emitted %d times, want 0 on clean-tree exit", types[events.EventStageCompleted])
	}
	if types[events.EventExecutionFailed] != 0 {
		t.Errorf("ExecutionFailed emitted %d times, want 0", types[events.EventExecutionFailed])
	}
}

// TestRunNoBus is the backward-compatibility guard: with no bus wired the
// engine still runs and never panics.
func TestRunNoBus(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	e := NewEngine(dir, &mockRetriever{}, nil)
	if _, err := e.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
