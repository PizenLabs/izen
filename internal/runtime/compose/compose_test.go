package compose

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain/signal"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/events/audit"
	"github.com/PizenLabs/izen/internal/orchestrator"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
)

// TestWireBuildsFullyFunctionalEventWiredApplication is the Sprint 3
// integration test: it asserts the single-composition-root invariant — one
// compose.Wire() invocation produces a complete Application whose engine tree
// (layered pipeline, plan, execution, patch, authorization, intent router,
// orchestrator) is fully constructed and wired onto ONE shared event bus, and
// whose canonical Runtime facade drives the domain WorkflowRuntime.
func TestWireBuildsFullyFunctionalEventWiredApplication(t *testing.T) {
	app, err := Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	// ── Engine tree fully wired by the single composition root ───────────
	if app.Runtime == nil {
		t.Error("Runtime facade not wired")
	}
	if app.Workflow == nil {
		t.Error("domain WorkflowRuntime not wired")
	}
	if app.Pipeline == nil {
		t.Error("layered pipeline engine not wired")
	}
	if app.PlanEngine == nil || app.PlanStore == nil {
		t.Error("plan engine/store not wired")
	}
	if app.Execution == nil {
		t.Error("execution engine not wired")
	}
	if app.Patch == nil {
		t.Error("patch engine not wired")
	}
	if app.Auth == nil {
		t.Error("authorization engine not wired")
	}
	if app.Policy == nil {
		t.Error("policy engine not wired")
	}
	if app.Auth.PolicyEngine() != app.Policy {
		t.Error("authorization engine is not wired to the composed policy engine")
	}
	if app.Orchestrator == nil {
		t.Error("orchestrator not wired")
	}
	if app.WorkflowSM == nil {
		t.Error("workflow state machine not wired")
	}
	if app.RuntimeCtx == nil {
		t.Error("runtime context not wired")
	}
	if app.Caps == nil || app.Budget == nil || app.MicroBudget == nil || app.Artifacts == nil {
		t.Error("control-plane capability set not fully wired")
	}
	if app.SnapCache == nil || app.CapRegistry == nil {
		t.Error("workspace snapshot/capability registry not wired")
	}
	if app.Git == nil || app.Lea == nil {
		t.Error("git/lea engines not wired")
	}
	if app.Microkernel == nil || app.IntentCompiler == nil {
		t.Error("microkernel/intent-compiler planners not wired")
	}
	if app.Session() == nil || app.Manager() == nil {
		t.Error("process session/AI manager not bootstrapped")
	}
	if app.Caps == nil || !app.Caps.CanPatch() || !app.Caps.CanWrite() {
		t.Error("capability set not granted")
	}

	// ── Shared event bus across the engine tree ──────────────────────────
	// The orchestrator's pipeline attachment proves the two engines share the
	// same instance (wired by Wire, not constructed in the presentation layer).
	if app.Orchestrator.Pipeline() != app.Pipeline {
		t.Error("orchestrator is not wired to the composed pipeline engine")
	}
	if app.Orchestrator.RuntimeContext() != app.RuntimeCtx {
		t.Error("orchestrator is not wired to the composed runtime context")
	}

	// ── Canonical commands flow through the Runtime facade ───────────────
	// The Runtime facade is the single entry point; executing a SwitchModeCmd
	// must drive the domain WorkflowRuntime AND publish the PhaseChanged event
	// on the shared bus that every projection subscribes to.
	phaseCh := make(chan events.DomainEvent, 4)
	sub := app.Bus.Subscribe(events.EventPhaseChanged, func(ev events.DomainEvent) {
		phaseCh <- ev
	})
	defer sub.Cancel()

	if err := app.Runtime.Execute(context.Background(), appruntime.SwitchModeCmd{Mode: "plan"}); err != nil {
		t.Fatalf("Execute(SwitchMode plan): %v", err)
	}
	if got := app.Workflow.Phase().String(); got != "plan" {
		t.Errorf("workflow phase = %q, want plan", got)
	}

	select {
	case ev := <-phaseCh:
		p, ok := ev.Payload().(events.PhaseChangedPayload)
		if !ok {
			t.Fatalf("PhaseChanged payload = %T", ev.Payload())
		}
		if p.To != "plan" {
			t.Errorf("PhaseChanged.To = %q, want plan", p.To)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected EventPhaseChanged on the shared bus after SwitchMode")
	}

	// ── Orchestrator phase transition lands on the shared bus ─────────────
	// The orchestrator drives the core WorkflowStateMachine and publishes
	// EventPhaseChanged on the SAME shared bus the Runtime facade uses — one
	// canonical stream for every projection.
	if err := app.Orchestrator.Transition(orchestrator.PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("orchestrator transition to plan: %v", err)
	}
	if app.WorkflowSM.State().String() != "planning" {
		t.Errorf("workflow SM state = %q, want planning", app.WorkflowSM.State())
	}
	select {
	case ev := <-phaseCh:
		p, ok := ev.Payload().(events.PhaseChangedPayload)
		if !ok {
			t.Fatalf("PhaseChanged payload = %T", ev.Payload())
		}
		if p.To != "plan" {
			t.Errorf("PhaseChanged.To = %q, want plan", p.To)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected EventPhaseChanged on the shared bus after orchestrator transition")
	}
}

func TestWireAuditDirCreatesLogger(t *testing.T) {
	root := t.TempDir()
	app, err := Wire(WithAuditDir(filepath.Join(root, ".izen", "audit")))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}

	if app.Audit == nil {
		app.Close()
		t.Fatal("expected an AuditLogger to be wired")
	}
	if app.Audit.Path() != filepath.Join(root, ".izen", "audit", "events.ndjson") {
		app.Close()
		t.Fatalf("Audit path = %q", app.Audit.Path())
	}

	// Publish an envelope on the shared bus and wait for the audit logger to
	// accept it, then close to flush.
	auditPath := app.Audit.Path()
	app.Bus.PublishEnvelope(events.NewSignalEnvelope(
		signal.New(signal.SignalBuildHalted, "test", nil), "test"))
	waitAuditAccepted(t, app.Audit, 1)
	app.Close()

	f, err := os.Open(auditPath)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	lines := 0
	var last []byte
	for sc.Scan() {
		if sc.Text() != "" {
			lines++
			last = append([]byte(nil), sc.Bytes()...)
		}
	}
	if lines != 1 {
		t.Fatalf("expected 1 audit line, got %d", lines)
	}
	var back events.Envelope
	if err := json.Unmarshal(last, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Kind != events.DomainKindSignal {
		t.Fatalf("unexpected envelope kind %q", back.Kind)
	}
}

func TestWireNoAuditDir(t *testing.T) {
	app, err := Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()
	if app.Audit != nil {
		t.Fatal("expected no AuditLogger when no audit dir is wired")
	}
}

// waitAuditAccepted polls the wired audit logger until it has accepted want
// envelopes, timing out after 3 seconds.
func waitAuditAccepted(t *testing.T, l *audit.AuditLogger, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for l.Accepted() < want {
		if time.Now().After(deadline) {
			t.Fatalf("audit logger accepted %d envelopes, want %d", l.Accepted(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
