package runtime_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
)

// presentationCollector captures translated PresentationEvents for assertions.
type presentationCollector struct {
	mu     sync.Mutex
	events []runtime.PresentationEvent
}

func (c *presentationCollector) add(ev runtime.PresentationEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *presentationCollector) snapshot() []runtime.PresentationEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]runtime.PresentationEvent, len(c.events))
	copy(out, c.events)
	return out
}

func (c *presentationCollector) hasType(typ runtime.PresentationEventType) bool {
	for _, ev := range c.snapshot() {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

// waitFor polls pred until it returns true or the deadline expires.
func waitFor(timeout time.Duration, pred func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}

// TestAskPlanBuildFlow verifies the full command execution flow across modes
// (/ask -> /plan -> /build) through the Application-layer Runtime: every
// command is dispatched to its handler, the domain WorkflowRuntime advances,
// domain events are translated into PresentationEvents, and the LedgerBuilder
// projection reflects the run.
func TestAskPlanBuildFlow(t *testing.T) {
	app, err := compose.Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	collector := &presentationCollector{}
	app.Runtime.SubscribePresentation(collector.add)
	app.Runtime.Start()

	ctx := context.Background()

	// /ask
	if err := app.Runtime.Execute(ctx, runtime.SubmitPromptCmd{Prompt: "explain the cache layer", Mode: "ask"}); err != nil {
		t.Fatalf("ask submit: %v", err)
	}

	// /plan
	if err := app.Runtime.Execute(ctx, runtime.SwitchModeCmd{Mode: "plan"}); err != nil {
		t.Fatalf("switch plan: %v", err)
	}
	if err := app.Runtime.Execute(ctx, runtime.SubmitPromptCmd{Prompt: "plan the API migration\nwrite the migration guide", Mode: "plan"}); err != nil {
		t.Fatalf("plan submit: %v", err)
	}

	// /build
	if err := app.Runtime.Execute(ctx, runtime.SwitchModeCmd{Mode: "build"}); err != nil {
		t.Fatalf("switch build: %v", err)
	}
	if err := app.Runtime.Execute(ctx, runtime.SubmitPromptCmd{Prompt: "implement the refactor", Mode: "build"}); err != nil {
		t.Fatalf("build submit: %v", err)
	}

	// approvals: the runtime NEVER fabricates a mutation. Approving or
	// rejecting a patch with no pending execution must fail deterministically
	// (Rule 3: no fake states). Real approval is exercised in the
	// RuntimeExecutor tests, which stage a genuine pending mutation first.
	if err := app.Runtime.Execute(ctx, runtime.ApprovePatchCmd{PatchID: "patch-a"}); err == nil {
		t.Fatal("approve of non-pending patch should fail (no fake mutation)")
	}
	if err := app.Runtime.Execute(ctx, runtime.RejectPatchCmd{PatchID: "patch-b", Reason: "too risky"}); err == nil {
		t.Fatal("reject of non-pending patch should fail (no fake mutation)")
	}
	if err := app.Runtime.Execute(ctx, runtime.CancelCmd{Reason: "stop"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Domain phase must have advanced to build (Review is never reached).
	if got := app.Workflow.Phase().String(); got != "build" {
		t.Fatalf("final phase = %s, want build", got)
	}

	// Presentation events must reflect every stage. PatchApplied/PatchRejected
	// are intentionally absent: no real mutation was staged or resolved.
	wantEvents := []runtime.PresentationEventType{
		runtime.PresentationCommandReceived,
		runtime.PresentationIntentParsed,
		runtime.PresentationPhaseChanged,
		runtime.PresentationStageCompleted,
	}
	for _, typ := range wantEvents {
		if !waitFor(2*time.Second, func() bool { return collector.hasType(typ) }) {
			t.Errorf("presentation stream missing event %q", typ)
		}
	}

	// The plan staging must NOT have produced a fabricated task list: the fake
	// newline-splitting projection was removed.
	snap := app.Ledger.Snapshot()
	if len(snap.Commands) == 0 {
		t.Error("ledger: expected command entries")
	}
	if snap.Phase != "build" {
		t.Errorf("ledger phase = %q, want build", snap.Phase)
	}
	if snap.Plan.TaskCount != 0 {
		t.Errorf("ledger plan task count = %d, want 0 (no fake plan)", snap.Plan.TaskCount)
	}
	// Two approval attempts were truthfully rejected (no pending mutations), so
	// the ledger records two failures — never a fabricated success. Delivery is
	// asynchronous (the ledger subscribes to the bus's dispatch goroutines), so
	// poll for the projection to catch up rather than snapshotting immediately.
	ledgerCaughtUp := waitFor(2*time.Second, func() bool {
		return len(app.Ledger.Snapshot().Failures) == 2
	})
	if !ledgerCaughtUp {
		t.Errorf("ledger failures = %d, want 2 (the two rejected approval attempts)", len(app.Ledger.Snapshot().Failures))
	}
}

// TestInvalidModeCommand verifies that bad commands surface errors and do not
// corrupt the domain phase.
func TestInvalidModeCommand(t *testing.T) {
	app, err := compose.Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	if err := app.Runtime.Execute(context.Background(), runtime.SwitchModeCmd{Mode: "warp"}); err == nil {
		t.Fatal("switch to unknown mode should fail")
	}
	if got := app.Workflow.Phase().String(); got != "ask" {
		t.Fatalf("phase = %s, want ask (unchanged)", got)
	}
}

// TestUnregisteredCommand ensures the dispatcher rejects unknown command types
// instead of silently succeeding.
func TestUnregisteredCommand(t *testing.T) {
	app, err := compose.Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	unknown := &unknownCommand{}
	if err := app.Runtime.Execute(context.Background(), unknown); err == nil {
		t.Fatal("unregistered command should fail")
	}
}

type unknownCommand struct{}

func (c *unknownCommand) Type() runtime.CommandType { return runtime.CommandType("nope") }
