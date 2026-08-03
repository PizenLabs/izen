package runtime

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// waitLedger polls the ledger until cond holds or times out, matching the
// asynchronous delivery semantics of the event bus.
func waitLedger(t *testing.T, l *ContextLedger, cond func(LedgerSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond(l.Snapshot()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ledger condition not met within deadline: %s", l.Render())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestLedgerBuilderProjectsEvents(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()

	b := NewLedgerBuilder(bus)
	b.Start()
	defer b.Close()

	bus.Publish(events.NewCommandReceived("/build fix", "build"))
	bus.Publish(events.NewIntentParsed("build", "fix", 0.9))
	bus.Publish(events.NewPlanStaged(2, []string{"a.go", "b.go"}, "build"))
	bus.Publish(events.NewPhaseChanged("plan", "build"))
	bus.Publish(events.NewPatchApplied("a.go", 5, 2, time.Millisecond))
	bus.Publish(events.NewExecutionFailed(events.FailureTransient, fmt.Errorf("flaky"), "build"))
	bus.Publish(events.NewStageCompleted("build", time.Second, ""))
	bus.Publish(events.NewActivity("[ OK ] built"))
	bus.Publish(events.NewApprovalRequested("c.go", "high-risk rewrite", ""))

	waitLedger(t, b.Ledger(), func(s LedgerSnapshot) bool {
		return len(s.Commands) == 1 &&
			s.Intent.Intent == "build" &&
			s.Plan.TaskCount == 2 &&
			s.Phase == "build" &&
			len(s.Patches) == 1 &&
			len(s.Failures) == 1 &&
			len(s.Stages) == 1 &&
			len(s.Activities) == 1 &&
			s.Approvals == 1 &&
			s.Events == 9
	})

	snap := b.Ledger().Snapshot()
	if snap.Commands[0].Command != "/build fix" || snap.Commands[0].Mode != "build" {
		t.Errorf("Command = %+v", snap.Commands[0])
	}
	if snap.Plan.Tasks[1] != "b.go" {
		t.Errorf("Plan.Tasks = %v", snap.Plan.Tasks)
	}
	if snap.Patches[0].LinesAdd != 5 || snap.Patches[0].LinesDel != 2 {
		t.Errorf("Patch = %+v", snap.Patches[0])
	}
	if snap.Failures[0].Classification != events.FailureTransient {
		t.Errorf("Failure = %+v", snap.Failures[0])
	}
	if snap.Stages[0].Stage != "build" {
		t.Errorf("Stages = %+v", snap.Stages[0])
	}
}

func TestLedgerBuilderRender(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()

	b := NewLedgerBuilder(bus)
	b.Start()
	defer b.Close()

	bus.Publish(events.NewCommandReceived("/plan", "plan"))
	bus.Publish(events.NewPlanStaged(3, []string{"a", "b", "c"}, "plan"))
	bus.Publish(events.NewPatchApplied("x.go", 4, 1, time.Millisecond))

	waitLedger(t, b.Ledger(), func(s LedgerSnapshot) bool { return s.Events == 3 })

	render := b.Ledger().Render()
	for _, want := range []string{"context ledger:", "3 events", "/plan", "plan 3 tasks", "x.go (+4 -1)"} {
		if !contains(render, want) {
			t.Errorf("Render() missing %q:\n%s", want, render)
		}
	}
}

func TestLedgerBuilderCloseStopsProjection(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()

	b := NewLedgerBuilder(bus)
	b.Start()
	bus.Publish(events.NewCommandReceived("/a", ""))
	waitLedger(t, b.Ledger(), func(s LedgerSnapshot) bool { return s.Events == 1 })

	b.Close()
	bus.Publish(events.NewCommandReceived("/b", ""))
	time.Sleep(30 * time.Millisecond)

	if got := b.Ledger().Snapshot().Events; got != 1 {
		t.Errorf("Events after Close = %d, want 1", got)
	}

	// Start is idempotent and restartable.
	b.Start()
	bus.Publish(events.NewCommandReceived("/c", ""))
	waitLedger(t, b.Ledger(), func(s LedgerSnapshot) bool { return s.Events == 2 })
	b.Close()
}

func TestLedgerBuilderNilSource(t *testing.T) {
	b := NewLedgerBuilder(nil)
	b.Start()
	defer b.Close()
	if b.Ledger() == nil {
		t.Fatal("Ledger() returned nil for nil source")
	}
	if b.Ledger().Snapshot().Events != 0 {
		t.Error("nil source projected events")
	}
}

func TestLedgerBuilderStartIdempotent(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()

	b := NewLedgerBuilder(bus)
	b.Start()
	b.Start()
	bus.Publish(events.NewCommandReceived("/x", ""))
	waitLedger(t, b.Ledger(), func(s LedgerSnapshot) bool { return s.Events == 1 })
	// One projection per event: duplicate subscriptions would double-count.
	time.Sleep(20 * time.Millisecond)
	if got := b.Ledger().Snapshot().Events; got != 1 {
		t.Errorf("Events = %d, want 1 (duplicate subscription?)", got)
	}
	b.Close()
}

func TestContextLedgerConcurrentApply(t *testing.T) {
	l := NewContextLedger()

	const workers = 8
	const perWorker = 500
	var wg sync.WaitGroup
	var delivered int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				l.Apply(events.NewCommandReceived("/x", ""))
				atomic.AddInt64(&delivered, 1)
			}
		}()
	}

	// Concurrent readers must never race with writers.
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = l.Snapshot()
					_ = l.Render()
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	readers.Wait()

	if got := l.Snapshot().Events; got != workers*perWorker {
		t.Errorf("Events = %d, want %d", got, workers*perWorker)
	}
}

func TestContextLedgerApplyNil(t *testing.T) {
	l := NewContextLedger()
	l.Apply(nil)
	if got := l.Snapshot().Events; got != 0 {
		t.Errorf("nil event projected, Events = %d", got)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
