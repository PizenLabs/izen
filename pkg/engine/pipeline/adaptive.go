package pipeline

import (
	"context"
	"fmt"
	"sync"

	"github.com/PizenLabs/izen/pkg/engine/control"
	"github.com/PizenLabs/izen/pkg/engine/decision"
	"github.com/PizenLabs/izen/pkg/engine/ir"
	"github.com/PizenLabs/izen/pkg/engine/layer0"
	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/layer4"
)

// AdaptivePlan expresses the six pipeline steps for req as immutable Static IR:
// an ExecutionGraph whose nodes are knowledge, capabilities, context, execute
// and validate. Every node is critical: the pipeline has no self-healable
// non-critical steps. A missing target simply yields an empty governed context
// (the existing non-fallback behavior), never a fabricated one.
func (e *Engine) AdaptivePlan(req Request) (*ir.Plan, error) {
	g := ir.NewGraph()
	add := func(id string, kind ir.NodeKind, description string, deps ...string) error {
		return g.AddNode(id, kind, true, description, deps...)
	}
	if err := add("knowledge", ir.KindEnvProbe, "resolve workspace knowledge"); err != nil {
		return nil, err
	}
	if err := add("capabilities", ir.KindEnvProbe, "detect workspace capability graph"); err != nil {
		return nil, err
	}
	if err := add("context", ir.KindContext, "assemble governed execution context", "knowledge", "capabilities"); err != nil {
		return nil, err
	}
	if err := add("execute", ir.KindLLM, "propose patches via the routed worker", "context"); err != nil {
		return nil, err
	}
	if err := add("validate", ir.KindVerify, "run the validation DAG", "execute"); err != nil {
		return nil, err
	}
	return &ir.Plan{
		ID:          fmt.Sprintf("plan-%s-%s", req.Mode, req.Intent),
		Description: fmt.Sprintf("adaptive pipeline for %s request", req.Mode),
		Graph:       g,
	}, nil
}

// RunAdaptive executes req as an immutable ExecutionGraph under the adaptive
// control system (Observe → Decide → Execute). It is the control-system
// equivalent of Run: the pipeline steps are Static IR, the live state is the
// Dynamic IR ExecutionSnapshot, and every retry / re-plan / skip decision is
// delegated to the Decision Engine. The pipeline steps are deterministic, so a
// single-attempt retry policy is used — re-generation is owned by the caller,
// never by the state machine.
func (e *Engine) RunAdaptive(ctx context.Context, req Request) (*Result, error) {
	plan, err := e.AdaptivePlan(req)
	if err != nil {
		return nil, err
	}
	exec := &adaptiveExecutor{e: e, req: req}
	session := control.NewSession(plan)
	pool := control.NewWorkerPool(2, exec)
	decisions := decision.NewStandardDecisionEngine(decision.WithRetryPolicy(decision.RetryPolicy{MaxAttempts: 1}))

	orch := control.NewControlLoopOrchestrator(session, decisions, pool, control.WithEventBus(e.bus))
	run, err := orch.Run(ctx)
	if err != nil {
		return nil, err
	}
	if run.Directive != decision.DirectiveContinue {
		return nil, fmt.Errorf("pipeline: adaptive run terminated with %s: %s", run.Directive, run.Reason)
	}

	exec.mu.Lock()
	defer exec.mu.Unlock()
	res := &Result{
		Knowledge:    exec.knowledge,
		Capabilities: exec.caps,
		Context:      exec.exec,
		Route:        e.RouteForMode(req.Mode),
		Patches:      exec.patches,
		Run:          exec.run,
		Validation:   exec.validation,
	}
	if exec.run != nil && exec.run.Result() != nil {
		res.Tokens = exec.run.Result().Tokens
	}
	return res, nil
}

// adaptiveExecutor is the Execution Plane of the adaptive pipeline: it maps
// each static node id onto the engine's layer steps and produces observations.
// It never decides anything — retry / re-plan / skip policy is the Decision
// Engine's. Intermediate Go values flow between nodes through the guarded
// fields below.
type adaptiveExecutor struct {
	e   *Engine
	req Request

	mu         sync.Mutex
	knowledge  *layer0.ResolvedKnowledge
	caps       *layer1.Graph
	exec       *layer2.ExecutionContext
	run        *layer3.Run
	patches    []layer3.FilePatch
	validation *layer4.Result
}

// Execute implements control.Executor.
func (x *adaptiveExecutor) Execute(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
	obs := ir.ObservationPayload{NodeID: node.ID}
	switch node.ID {
	case "knowledge":
		k, err := x.e.Knowledge(ctx)
		if err != nil {
			return obs, err
		}
		x.mu.Lock()
		x.knowledge = k
		x.mu.Unlock()
		obs.OK = true
		obs.Output = fmt.Sprintf("resolved %d constraint(s) via %s manager", len(k.StructuralConstraints), k.PrimaryManager)

	case "capabilities":
		g, err := x.e.Capabilities()
		if err != nil {
			return obs, err
		}
		x.mu.Lock()
		x.caps = g
		x.mu.Unlock()
		count := 0
		for _, c := range layer1.AllCapabilities() {
			if g.Supports(c) {
				count++
			}
		}
		obs.OK = true
		obs.Output = fmt.Sprintf("detected %s stack with %d capability/capabilities", g.Stack(), count)

	case "context":
		route := x.e.RouteForMode(x.req.Mode)
		exec, err := x.e.Context(ctx, x.req, route.Policy)
		if err != nil {
			return obs, err
		}
		x.mu.Lock()
		x.exec = exec
		x.mu.Unlock()
		obs.OK = true
		obs.Output = fmt.Sprintf("assembled %d file(s) / %d symbol(s) under budget", exec.Stats.Files, exec.Stats.Symbols)

	case "execute":
		x.mu.Lock()
		exec := x.exec
		x.mu.Unlock()
		if exec == nil {
			return obs, fmt.Errorf("pipeline: execute step ran before context assembly")
		}
		intent := x.req.Intent
		if intent == "" {
			intent = layer3.IntentRefactor
		}
		route := x.e.RouteForMode(x.req.Mode)
		pipeline := x.e.newPipeline(route, exec)
		run, err := pipeline.Execute(ctx, x.e.toLayer3Request(x.req, intent))
		if err != nil {
			return obs, err
		}
		patches := make([]layer3.FilePatch, 0)
		if run != nil && run.Result() != nil {
			patches = run.Result().Patches
		}
		x.mu.Lock()
		x.run = run
		x.patches = patches
		x.mu.Unlock()
		obs.OK = true
		obs.Output = fmt.Sprintf("produced %d patch(es)", len(patches))

	case "validate":
		x.mu.Lock()
		patches := x.patches
		x.mu.Unlock()
		val, err := x.e.Validate(ctx, patches)
		if err != nil && val == nil {
			return obs, err
		}
		x.mu.Lock()
		x.validation = val
		x.mu.Unlock()
		if val != nil && !val.OK {
			obs.OK = false
			if val.Err != nil {
				obs.Err = val.Err.Error()
			} else {
				obs.Err = "validation DAG failed"
			}
			obs.Output = "validation DAG failed"
			return obs, nil
		}
		obs.OK = true
		obs.Output = fmt.Sprintf("validation passed (%d node(s))", len(val.Nodes))

	default:
		return obs, fmt.Errorf("pipeline: unknown adaptive node %q", node.ID)
	}
	return obs, nil
}
