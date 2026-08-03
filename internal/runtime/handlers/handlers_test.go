package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime"
)

// collectBus captures published domain events in a race-free slice.
type collectBus struct {
	*events.Bus
	mu     sync.Mutex
	events []events.DomainEvent
}

func subscribeCollect(b *events.Bus, types ...string) *collectBus {
	c := &collectBus{Bus: b}
	for _, typ := range types {
		b.Subscribe(typ, func(ev events.DomainEvent) {
			c.mu.Lock()
			c.events = append(c.events, ev)
			c.mu.Unlock()
		})
	}
	return c
}

func newDeps() (HandlerDeps, *collectBus) {
	bus := events.NewBus(events.DefaultBufferSize)
	c := subscribeCollect(bus,
		events.EventIntentParsed,
		events.EventPhaseChanged,
		events.EventPlanStaged,
		events.EventPatchApplied,
		events.EventPatchRejected,
		events.EventStageCompleted,
	)
	return HandlerDeps{
		Workflow: workflow.NewWorkflowRuntime(),
		Bus:      bus,
		Approver: NewInMemoryApprover(),
	}, c
}

func TestSwitchModeHandler_TransitionsAndEmits(t *testing.T) {
	deps, c := newDeps()
	h := New(deps).Switch()

	if err := h.Handle(context.Background(), runtime.SwitchModeCmd{Mode: "plan"}); err != nil {
		t.Fatalf("SwitchMode: %v", err)
	}
	if got := deps.Workflow.Phase(); got != workflow.PhasePlan {
		t.Fatalf("phase = %s, want plan", got)
	}
	if !hasType(c, events.EventPhaseChanged) {
		t.Fatalf("expected EventPhaseChanged, got %+v", c.snapshot())
	}
	if !hasType(c, events.EventStageCompleted) {
		t.Fatalf("expected EventStageCompleted, got %+v", c.snapshot())
	}
}

func TestSwitchModeHandler_InvalidMode(t *testing.T) {
	deps, _ := newDeps()
	h := New(deps).Switch()
	err := h.Handle(context.Background(), runtime.SwitchModeCmd{Mode: "nope"})
	if !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("err = %v, want ErrInvalidMode", err)
	}
}

func TestSwitchModeHandler_RejectsIllegalTransition(t *testing.T) {
	deps, _ := newDeps()
	// Reset to Ask then jump to Build (forward is legal) then attempt
	// backward to Ask (illegal per DefaultTransitionRule).
	deps.Workflow.Reset()
	h := New(deps).Switch()
	if err := h.Handle(context.Background(), runtime.SwitchModeCmd{Mode: "build"}); err != nil {
		t.Fatalf("forward transition: %v", err)
	}
	if err := h.Handle(context.Background(), runtime.SwitchModeCmd{Mode: "ask"}); err == nil {
		t.Fatal("backward transition should fail")
	}
}

func TestSubmitPromptHandler_ExplicitModeRoutesPhase(t *testing.T) {
	deps, c := newDeps()
	h := New(deps).Submit()

	if err := h.Handle(context.Background(), runtime.SubmitPromptCmd{Prompt: "migrate the database", Mode: "plan"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := deps.Workflow.Phase(); got != workflow.PhasePlan {
		t.Fatalf("phase = %s, want plan", got)
	}
	if !hasType(c, events.EventIntentParsed) {
		t.Fatalf("expected EventIntentParsed, got %+v", c.snapshot())
	}
	if !hasType(c, events.EventPlanStaged) {
		t.Fatalf("expected EventPlanStaged, got %+v", c.snapshot())
	}
	if !hasType(c, events.EventStageCompleted) {
		t.Fatalf("expected EventStageCompleted, got %+v", c.snapshot())
	}
}

func TestSubmitPromptHandler_KeywordClassifiesIntent(t *testing.T) {
	deps, c := newDeps()
	h := New(deps).Submit()
	if err := h.Handle(context.Background(), runtime.SubmitPromptCmd{Prompt: "why does the build fail"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !hasType(c, events.EventIntentParsed) {
		t.Fatalf("expected EventIntentParsed, got %+v", c.snapshot())
	}
	if got := deps.Workflow.Phase(); got != workflow.PhaseInvestigate {
		t.Fatalf("phase = %s, want investigate (keyword intent)", got)
	}
}

func TestSubmitPromptHandler_EmptyPrompt(t *testing.T) {
	deps, _ := newDeps()
	h := New(deps).Submit()
	if err := h.Handle(context.Background(), runtime.SubmitPromptCmd{Prompt: "  "}); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("err = %v, want ErrEmptyPrompt", err)
	}
}

func TestApprovePatchHandler_EmitsApplied(t *testing.T) {
	deps, c := newDeps()
	deps.Approver.(*InMemoryApprover).Register("p1", ApprovalResult{File: "a.go", LinesAdd: 3, LinesDel: 1})
	h := New(deps).Approve()

	if err := h.Handle(context.Background(), runtime.ApprovePatchCmd{PatchID: "p1"}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !hasType(c, events.EventPatchApplied) {
		t.Fatalf("expected EventPatchApplied, got %+v", c.snapshot())
	}
}

func TestRejectPatchHandler_EmitsRejected(t *testing.T) {
	deps, c := newDeps()
	h := New(deps).Reject()
	if err := h.Handle(context.Background(), runtime.RejectPatchCmd{PatchID: "p2", Reason: "too risky"}); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if !hasType(c, events.EventPatchRejected) {
		t.Fatalf("expected EventPatchRejected, got %+v", c.snapshot())
	}
}

func TestCancelHandler_EmitsStageCompleted(t *testing.T) {
	deps, c := newDeps()
	h := New(deps).Cancel()
	if err := h.Handle(context.Background(), runtime.CancelCmd{Reason: "stop"}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !hasType(c, events.EventStageCompleted) {
		t.Fatalf("expected EventStageCompleted, got %+v", c.snapshot())
	}
}

func TestHandlersRespectCancellation(t *testing.T) {
	deps, _ := newDeps()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handlers := []runtime.CommandHandler{
		New(deps).Submit(),
		New(deps).Switch(),
		New(deps).Approve(),
		New(deps).Reject(),
		New(deps).Cancel(),
	}
	for _, h := range handlers {
		if err := h.Handle(ctx, runtime.CancelCmd{}); err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("Handle(%T) = %v, want context.Canceled", h, err)
		}
	}
}

func TestRegister(t *testing.T) {
	d := runtime.NewCommandDispatcher()
	deps, _ := newDeps()
	if err := New(deps).Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := d.Dispatch(context.Background(), runtime.SwitchModeCmd{Mode: "ask"}); err != nil {
		t.Fatalf("Dispatch(switch_mode): %v", err)
	}
}

func TestRegisterDefaults(t *testing.T) {
	d := runtime.NewCommandDispatcher()
	if err := RegisterDefaults(d); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	// Default deps have no workflow: SwitchMode must return ErrNoWorkflow.
	err := d.Dispatch(context.Background(), runtime.SwitchModeCmd{Mode: "plan"})
	if !errors.Is(err, ErrNoWorkflow) {
		t.Fatalf("Dispatch(switch_mode) = %v, want ErrNoWorkflow", err)
	}
}

func TestRegisterNilDispatcher(t *testing.T) {
	if err := New(HandlerDeps{}).Register(nil); err == nil {
		t.Fatal("Register(nil): want error, got nil")
	}
}

func TestClassifyIntent(t *testing.T) {
	tests := []struct {
		prompt string
		mode   string
		want   string
	}{
		{"plan the migration", "", "plan"},
		{"implement the feature", "", "build"},
		{"debug the crash", "", "investigate"},
		{"review the changes", "", "review"},
		{"hello world", "", "ask"},
		{"anything at all", "build", "build"},
	}
	for _, tt := range tests {
		got, conf := ClassifyIntent(tt.prompt, tt.mode)
		if got != tt.want {
			t.Errorf("ClassifyIntent(%q,%q) = %q, want %q", tt.prompt, tt.mode, got, tt.want)
		}
		if conf <= 0 || conf > 1 {
			t.Errorf("ClassifyIntent confidence out of range: %f", conf)
		}
	}
}

func TestExtractTasks(t *testing.T) {
	if got := ExtractTasks("a\nb\n\nc"); len(got) != 3 {
		t.Fatalf("ExtractTasks = %v, want 3 tasks", got)
	}
	if got := ExtractTasks("single"); len(got) != 1 || got[0] != "single" {
		t.Fatalf("ExtractTasks = %v, want [single]", got)
	}
}

func hasType(c *collectBus, typ string) bool {
	if c.has(typ) {
		return true
	}
	// Delivery is async; give the bus a moment to drain before failing.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if c.has(typ) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (c *collectBus) has(typ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Type() == typ {
			return true
		}
	}
	return false
}

// snapshot returns a race-free copy of the captured events.
func (c *collectBus) snapshot() []events.DomainEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]events.DomainEvent, len(c.events))
	copy(out, c.events)
	return out
}
