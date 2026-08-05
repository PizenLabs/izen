package control

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/decision"
	"github.com/PizenLabs/izen/pkg/engine/ir"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// behaviorExecutor maps node ids to deterministic behaviors.
func behaviorExecutor(behaviors map[string]func(node *ir.ExecutionNode, vars ir.Variables) ir.ObservationPayload) Executor {
	return ExecutorFunc(func(_ context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
		if b, ok := behaviors[node.ID]; ok {
			return b(node, vars), nil
		}
		return ir.ObservationPayload{NodeID: node.ID, OK: true}, nil
	})
}

func mustAddNode(t *testing.T, g *ir.ExecutionGraph, id string, kind ir.NodeKind, critical bool, description string, deps ...string) {
	t.Helper()
	if err := g.AddNode(id, kind, critical, description, deps...); err != nil {
		t.Fatal(err)
	}
}

// recordingEngine wraps a decision engine and logs every directive it emits so
// tests can prove the loop never re-planned or re-dispatched unexpectedly.
type recordingEngine struct {
	decision.DecisionEngine
	mu  sync.Mutex
	log []decision.Directive
}

func (r *recordingEngine) Decide(ctx context.Context, snap ir.SnapshotReader) (decision.Decision, error) {
	d, err := r.DecisionEngine.Decide(ctx, snap)
	r.mu.Lock()
	r.log = append(r.log, d.Directive)
	r.mu.Unlock()
	return d, err
}

func (r *recordingEngine) directives() []decision.Directive {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]decision.Directive(nil), r.log...)
}

// TestLoopSelfHealingSkipsNonCriticalMissingFile is the canonical adaptive
// self-healing path: a non-critical file check fails (missing optional file)
// and the loop absorbs it WITHOUT any LLM re-planning, then completes the
// dependent verify node.
func TestLoopSelfHealingSkipsNonCriticalMissingFile(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "check-optional", ir.KindFileCheck, false, "probe the optional hooks file")
	mustAddNode(t, g, "check-core", ir.KindFileCheck, true, "probe the required core file")
	mustAddNode(t, g, "verify", ir.KindVerify, true, "run verification", "check-optional", "check-core")

	exec := behaviorExecutor(map[string]func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload{
		"check-optional": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{
				OK:     false,
				Err:    "optional/hooks.go: no such file",
				Output: "missing optional file",
			}
		},
		"check-core": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{OK: true, Output: "core.go present"}
		},
		"verify": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{OK: true, Output: "build passed"}
		},
	})

	session := NewSession(&ir.Plan{ID: "selfheal", Description: "skip missing optional file", Graph: g})
	inner := decision.NewStandardDecisionEngine()
	rec := &recordingEngine{DecisionEngine: inner}
	pool := NewWorkerPool(2, exec)
	orch := NewControlLoopOrchestrator(session, rec, pool, WithMaxIterations(20))

	res, err := orch.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v\ndirectives: %v\nstates: %v", err, rec.directives(), session.Observe().NodeStates)
	}
	if res.Directive != decision.DirectiveContinue {
		t.Fatalf("terminal directive = %s (%s), want continue", res.Directive, res.Reason)
	}
	if res.NodeStates["verify"] != ir.StateSuccess {
		t.Fatalf("verify state = %s, want success", res.NodeStates["verify"])
	}
	if res.NodeStates["check-core"] != ir.StateSuccess {
		t.Fatalf("check-core state = %s, want success", res.NodeStates["check-core"])
	}
	// The absorbed node must be Success AND retain its skip provenance.
	if res.NodeStates["check-optional"] != ir.StateSuccess {
		t.Fatalf("check-optional state = %s, want success (absorbed)", res.NodeStates["check-optional"])
	}

	// Prove the loop self-healed WITHOUT re-planning: no RePlan directive was
	// ever emitted and the only failure was the absorbed one.
	for _, d := range rec.directives() {
		if d == decision.DirectiveRePlan {
			t.Fatal("self-healing path must not re-plan")
		}
	}

	snap := session.Observe()
	obs, ok := snap.Observation("check-optional")
	if !ok || obs.SkipReason == "" {
		t.Fatal("absorbed node must retain its skip provenance in the Dynamic IR")
	}
	if obs.OK {
		t.Fatal("absorbed node observation must retain the raw failure fact (OK=false) with skip provenance")
	}
}

// TestLoopRetryThenSuccess drives a flaky verify node: the Decision Engine
// emits Retry after the first failure, the loop applies the backoff and the
// second attempt succeeds.
func TestLoopRetryThenSuccess(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "build", ir.KindVerify, true, "run the build")

	var buildCalls atomic.Int32
	exec := ExecutorFunc(func(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
		if node.ID == "build" && buildCalls.Add(1) == 1 {
			return ir.ObservationPayload{NodeID: node.ID, OK: false, Err: "flake: transient", Output: "exit 1"}, nil
		}
		return ir.ObservationPayload{NodeID: node.ID, OK: true, Output: "build passed"}, nil
	})

	session := NewSession(&ir.Plan{ID: "retry", Graph: g})
	decisions := decision.NewStandardDecisionEngine(decision.WithRetryPolicy(decision.RetryPolicy{
		MaxAttempts: 2,
		Backoff:     func(int) time.Duration { return time.Millisecond },
	}))
	pool := NewWorkerPool(1, exec)
	orch := NewControlLoopOrchestrator(session, decisions, pool, WithMaxIterations(20))

	res, err := orch.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Directive != decision.DirectiveContinue {
		t.Fatalf("terminal directive = %s, want continue", res.Directive)
	}
	if buildCalls.Load() != 2 {
		t.Fatalf("build executed %d time(s), want 2", buildCalls.Load())
	}
	if res.Attempts != 2 {
		t.Errorf("total attempts = %d, want 2", res.Attempts)
	}
	snap := session.Observe()
	if snap.Attempts("build") != 2 {
		t.Errorf("build attempt count = %d, want 2", snap.Attempts("build"))
	}
}

// TestLoopRePlanTerminates verifies a graph-invalidation signal on an
// exhausted critical node terminates the loop with a RePlan directive.
func TestLoopRePlanTerminates(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "scan", ir.KindLLM, true, "scan the workspace")

	exec := behaviorExecutor(map[string]func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload{
		"scan": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{
				OK:         false,
				Err:        "toolchain moved",
				EnvSignals: []ir.EnvSignal{{Kind: ir.SignalGraphInvalidation, Name: "toolchain.moved"}},
			}
		},
	})

	session := NewSession(&ir.Plan{ID: "replan", Graph: g})
	decisions := decision.NewStandardDecisionEngine(decision.WithRetryPolicy(decision.RetryPolicy{MaxAttempts: 1}))
	orch := NewControlLoopOrchestrator(session, decisions, NewWorkerPool(1, exec))

	res, err := orch.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Directive != decision.DirectiveRePlan {
		t.Fatalf("terminal directive = %s (%s), want replan", res.Directive, res.Reason)
	}
	if res.Err != nil {
		t.Errorf("replan must not carry an error, got %v", res.Err)
	}
}

// TestLoopAbortCriticalFailure verifies an exhausted critical failure without a
// graph-invalidation signal terminates with Abort.
func TestLoopAbortCriticalFailure(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "build", ir.KindVerify, true, "run the build")

	exec := behaviorExecutor(map[string]func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload{
		"build": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{OK: false, Err: "compilation failed", Output: "cannot compile"}
		},
	})

	session := NewSession(&ir.Plan{ID: "abort", Graph: g})
	decisions := decision.NewStandardDecisionEngine(decision.WithRetryPolicy(decision.RetryPolicy{MaxAttempts: 1}))
	orch := NewControlLoopOrchestrator(session, decisions, NewWorkerPool(1, exec))

	res, err := orch.Run(context.Background())
	if err == nil {
		t.Fatal("expected abort error")
	}
	if !errors.Is(err, ErrAbortedByDecision) {
		t.Fatalf("err = %v, want ErrAbortedByDecision", err)
	}
	if res.Directive != decision.DirectiveAbort {
		t.Fatalf("terminal directive = %s, want abort", res.Directive)
	}
}

// TestLoopHumanApprovalGate exercises the safety bounds: a destructive action
// pauses for human authorization, then executes after approval.
func TestLoopHumanApprovalGate(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "probe", ir.KindEnvProbe, true, "probe workspace")
	mustAddNode(t, g, "mutation", ir.KindShell, true, "destructive mutation", "probe")
	if err := g.MarkApprovalRequired("mutation"); err != nil {
		t.Fatal(err)
	}

	exec := behaviorExecutor(map[string]func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload{
		"probe": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload { return ir.ObservationPayload{OK: true} },
		"mutation": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{OK: true, Output: "mutation applied"}
		},
	})

	var approved atomic.Bool
	approvals := ApprovalFunc(func(ctx context.Context, d decision.Decision) (bool, error) {
		if d.Directive != decision.DirectiveHumanApproval || d.NodeID != "mutation" {
			t.Errorf("approval request = %+v, want human_approval for mutation", d)
		}
		return approved.Load(), nil
	})

	session := NewSession(&ir.Plan{ID: "approval", Graph: g})
	decisions := decision.NewStandardDecisionEngine()
	orch := NewControlLoopOrchestrator(session, decisions, NewWorkerPool(2, exec),
		WithApprovalRequester(approvals), WithMaxIterations(20))

	approved.Store(true)
	res, err := orch.Run(context.Background())
	if err != nil {
		t.Fatalf("Run (approved): %v", err)
	}
	if res.Directive != decision.DirectiveContinue {
		t.Fatalf("terminal directive = %s, want continue after approval", res.Directive)
	}
	if res.NodeStates["mutation"] != ir.StateSuccess {
		t.Fatalf("mutation state = %s, want success", res.NodeStates["mutation"])
	}

	// A declined request aborts the loop.
	approved.Store(false)
	session2 := NewSession(&ir.Plan{ID: "denied", Graph: g})
	orch2 := NewControlLoopOrchestrator(session2, decisions, NewWorkerPool(2, exec),
		WithApprovalRequester(approvals), WithMaxIterations(20))
	res2, err := orch2.Run(context.Background())
	if err == nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("denied err = %v, want ErrDenied", err)
	}
	if res2.Directive != decision.DirectiveAbort {
		t.Fatalf("denied terminal directive = %s, want abort", res2.Directive)
	}
	if res2.NodeStates["mutation"] != ir.StatePending {
		t.Fatalf("mutation must never run after denial, state = %s", res2.NodeStates["mutation"])
	}
}

// TestLoopEmitsFactOnlyTelemetry verifies the control loop publishes raw facts
// (iterations, node observations, termination) and never directives.
func TestLoopEmitsFactOnlyTelemetry(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "probe", ir.KindEnvProbe, true, "probe")
	mustAddNode(t, g, "verify", ir.KindVerify, true, "verify", "probe")

	exec := behaviorExecutor(map[string]func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload{
		"probe": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{OK: true, Output: "probed"}
		},
		"verify": func(*ir.ExecutionNode, ir.Variables) ir.ObservationPayload {
			return ir.ObservationPayload{OK: true, Output: "verified"}
		},
	})

	bus := telemetry.NewEventBus(64)
	defer bus.Close()
	var mu sync.Mutex
	var iterations, observed, terminated int
	bus.SubscribeAll(func(ev telemetry.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type() {
		case telemetry.EventControlIteration:
			iterations++
		case telemetry.EventControlNodeObserved:
			observed++
		case telemetry.EventControlTerminated:
			terminated++
		}
	})

	session := NewSession(&ir.Plan{ID: "facts", Graph: g})
	orch := NewControlLoopOrchestrator(session, decision.NewStandardDecisionEngine(),
		NewWorkerPool(2, exec), WithEventBus(bus), WithMaxIterations(20))

	res, err := orch.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Directive != decision.DirectiveContinue {
		t.Fatalf("terminal directive = %s, want continue", res.Directive)
	}

	waitForCount(t, &mu, func() int { return iterations }, 3)
	waitForCount(t, &mu, func() int { return observed }, 2)
	waitForCount(t, &mu, func() int { return terminated }, 1)
	if iterations < 3 {
		t.Errorf("iterations = %d, want >= 3", iterations)
	}
	if observed != 2 {
		t.Errorf("node observations = %d, want 2", observed)
	}
	if terminated != 1 {
		t.Errorf("terminated = %d, want 1", terminated)
	}
}

func waitForCount(t *testing.T, mu *sync.Mutex, count func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := count()
		mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for count %d, got %d", want, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
