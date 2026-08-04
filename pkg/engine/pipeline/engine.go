package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/pkg/engine/layer0"
	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/layer4"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// Sentinel errors returned by the pipeline engine.
var (
	ErrNoRouter     = errors.New("pipeline: no model router configured")
	ErrNoClient     = errors.New("pipeline: no worker client configured")
	ErrNoCapability = errors.New("pipeline: workspace capability detection failed")
	ErrNoKnowledge  = errors.New("pipeline: knowledge resolution failed")
)

// ValidationMode selects how the engine validates proposed patches.
type ValidationMode int

const (
	// ValidationRAMOnly (default) runs the in-RAM structural checks first: the
	// Layer 4 structural validator (SoR-based) and the native syntax parser. It
	// never shells out to the workspace toolchain.
	ValidationRAMOnly ValidationMode = iota
	// ValidationCapability runs the full Layer 4 validation plan: structural +
	// syntax always, then lint/build/test when the workspace capability graph
	// exposes them. Command stages execute against the on-disk workspace, so
	// patches must already be applied before Run.
	ValidationCapability
)

// Request is a single request executed by the pipeline engine. Mode selects
// the model-routing intent; the remaining fields describe the execution work.
type Request struct {
	// Mode is the originating mode name ("plan", "build", "ask", ...). It
	// selects the routing intent and therefore the model + context budget.
	Mode string
	// Intent is the Layer 3 execution intent (refactor, new_feature, bug_fix,
	// rename, ...). Empty defaults to refactor for generative requests.
	Intent       layer3.Intent
	TargetFile   string
	TargetSymbol string
	NewName      string
	NewImport    string
	Description  string
	Scope        []string
}

// Result is the immutable outcome of a full pipeline run. It exposes the
// knowledge, capabilities, governed context, selected route, proposed patches
// and the validation DAG result.
type Result struct {
	Knowledge    *layer0.ResolvedKnowledge
	Capabilities *layer1.Graph
	Context      *layer2.ExecutionContext
	Route        RouteProfile
	Patches      []layer3.FilePatch
	Run          *layer3.Run
	Validation   *layer4.Result
	Tokens       layer3.TokenUsage
}

// Engine is the central Pipeline Engine. It wires Layers 0-5 into a single
// execution path and performs no legacy heuristic context gathering or stack
// detection. It is safe for concurrent use: every Run constructs its own
// Layer 3 pipeline run and the underlying layer instances are immutable or
// concurrency-safe.
type Engine struct {
	root   string
	leaEng *lea.Engine

	resolver *layer0.KnowledgeResolver
	sor      *layer2.Sor
	governor *layer2.ContextGovernor
	ast      *layer3.ASTRewriteHandler

	routes *Router
	client layer3.WorkerClient
	mode   ValidationMode
	clock  func() time.Time

	bus *telemetry.EventBus

	mu        sync.Mutex
	indexOnce bool
}

// Option configures an Engine.
type Option func(*Engine)

// WithEventBus wires the Layer 5 telemetry bus. A nil bus (the default)
// disables event emission; the engine still runs headlessly.
func WithEventBus(bus *telemetry.EventBus) Option {
	return func(e *Engine) { e.bus = bus }
}

// WithRouter installs the intent-based model router. A nil router (the
// default) falls back to a default router.
func WithRouter(r *Router) Option {
	return func(e *Engine) {
		if r != nil {
			e.routes = r
		}
	}
}

// WithClient installs the Layer 3 WorkerClient used for generative execution.
// When nil, generative requests fail with ErrNoClient.
func WithClient(c layer3.WorkerClient) Option {
	return func(e *Engine) { e.client = c }
}

// WithValidationMode selects how proposed patches are validated.
func WithValidationMode(m ValidationMode) Option {
	return func(e *Engine) { e.mode = m }
}

// WithClock overrides the engine clock (test seam).
func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		if now != nil {
			e.clock = now
		}
	}
}

// NewEngine returns a pipeline engine over the workspace at root. The lea
// engine supplies the Layer 2 System of Record; it may be nil, in which case
// context assembly degrades to an empty context and structural validation
// runs against the proposed patches alone. The engine lazily indexes the lea
// engine once when the graph is empty.
func NewEngine(root string, leaEng *lea.Engine, opts ...Option) *Engine {
	e := &Engine{
		root:     root,
		leaEng:   leaEng,
		routes:   NewRouter(),
		mode:     ValidationRAMOnly,
		clock:    time.Now,
		resolver: layer0.NewKnowledgeResolver(root),
	}
	for _, o := range opts {
		o(e)
	}
	e.sor = layer2.NewSor(leaEng)
	e.governor = layer2.NewContextGovernor(e.sor)
	e.ast = layer3.NewASTRewriteHandler(e.sor)
	return e
}

// Root returns the workspace root the engine operates on.
func (e *Engine) Root() string { return e.root }

// Router returns the intent-based model router.
func (e *Engine) Router() *Router { return e.routes }

// Bus returns the wired Layer 5 telemetry bus, if any.
func (e *Engine) Bus() *telemetry.EventBus { return e.bus }

// Governor returns the Layer 2 context governor.
func (e *Engine) Governor() *layer2.ContextGovernor { return e.governor }

// Guard returns a Layer 3 policy guard over the detected workspace
// capabilities. It is safe for concurrent use; each call produces an
// immutable guard over the current capability surface.
func (e *Engine) Guard() *layer3.PolicyGuard {
	g, err := e.detectCapabilities()
	if err != nil {
		return layer3.NewPolicyGuard(nil)
	}
	return layer3.NewPolicyGuard(g)
}

// Sor returns the Layer 2 System of Record facade.
func (e *Engine) Sor() *layer2.Sor { return e.sor }

// emit publishes a telemetry event when a bus is wired.
func (e *Engine) emit(ev telemetry.Event) {
	if e.bus != nil && ev != nil {
		e.bus.Publish(ev)
	}
}

// ensureIndexed indexes the lea engine once when its graph is empty so Layer 2
// and Layer 4 have a real System of Record to work from.
func (e *Engine) ensureIndexed(ctx context.Context) error {
	if e.leaEng == nil {
		return nil
	}
	if g := e.leaEng.Graph(); g != nil && g.Stats().FileCount > 0 {
		return nil
	}
	e.mu.Lock()
	if e.indexOnce {
		e.mu.Unlock()
		return nil
	}
	e.indexOnce = true
	e.mu.Unlock()
	_, err := e.leaEng.Index(ctx)
	if err != nil {
		return fmt.Errorf("pipeline: index workspace: %w", err)
	}
	return nil
}

// Knowledge resolves the workspace knowledge through Layer 0. It is Step 1 of
// the pipeline: it determines the absolute constraints and directives that
// every subsequent layer must honor.
func (e *Engine) Knowledge(ctx context.Context) (*layer0.ResolvedKnowledge, error) {
	start := e.clock()
	k, err := e.resolver.Resolve()
	dur := e.clock().Sub(start)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoKnowledge, err)
	}
	e.emit(telemetry.NewKnowledgeResolved(k, dur))
	return k, nil
}

// Capabilities detects the workspace capability graph through Layer 1. It is
// Step 2 of the pipeline: it is the single authoritative source for the
// workspace stack and its toolchain commands.
func (e *Engine) Capabilities() (*layer1.Graph, error) {
	start := e.clock()
	g, err := e.detectCapabilities()
	if err != nil {
		return nil, err
	}
	e.emit(telemetry.NewCapabilityDetected(g, e.clock().Sub(start)))
	return g, nil
}

// detectCapabilities runs the Layer 1 detection without emitting telemetry.
func (e *Engine) detectCapabilities() (*layer1.Graph, error) {
	g, err := layer1.Detect(e.root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoCapability, err)
	}
	return g, nil
}

// SystemPromptHeader renders the Layer 1 capability header that must be
// injected into every LLM system prompt, eliminating tech-stack
// hallucinations.
func (e *Engine) SystemPromptHeader() (string, error) {
	g, err := e.Capabilities()
	if err != nil {
		return "", err
	}
	return CapabilityHeader(g), nil
}

// RouteForMode resolves the intent-based model route for a mode name.
func (e *Engine) RouteForMode(mode string) RouteProfile {
	return e.routes.RouteForMode(mode)
}

// Context assembles the immutable ExecutionContext for a request through Layer
// 2 under the given policy. It is Step 3 of the pipeline and replaces all
// legacy file extraction: the only context source is the lea SoR.
func (e *Engine) Context(ctx context.Context, req Request, policy layer2.ContextPolicy) (*layer2.ExecutionContext, error) {
	if err := e.ensureIndexed(ctx); err != nil {
		return nil, err
	}
	l2req := layer2.ContextRequest{
		TargetFile:   req.TargetFile,
		TargetSymbol: req.TargetSymbol,
	}
	if l2req.TargetFile == "" && l2req.TargetSymbol == "" {
		exec := emptyContext(policy)
		e.emit(telemetry.NewContextGoverned(l2req, exec, 0))
		return exec, nil
	}
	start := e.clock()
	exec, err := e.governor.Build(l2req, policy)
	e.emit(telemetry.NewContextGoverned(l2req, exec, e.clock().Sub(start)))
	if err != nil {
		return nil, fmt.Errorf("pipeline: assemble context: %w", err)
	}
	return exec, nil
}

// Validate runs proposed patches through the Layer 4 validation DAG engine. It
// is Step 5 of the pipeline: RAM structural checks always run first, and
// capability-backed stages (lint/build/test) are only scheduled when the
// workspace exposes them and ValidationCapability is selected.
func (e *Engine) Validate(ctx context.Context, patches []layer3.FilePatch) (*layer4.Result, error) {
	g, err := e.Capabilities()
	if err != nil {
		return nil, err
	}
	return e.validateWithGraph(ctx, patches, g)
}

// validateWithGraph runs the validation DAG against a pre-detected capability
// graph without re-emitting Layer 1 telemetry.
func (e *Engine) validateWithGraph(ctx context.Context, patches []layer3.FilePatch, g *layer1.Graph) (*layer4.Result, error) {
	if err := e.ensureIndexed(ctx); err != nil {
		return nil, err
	}
	start := e.clock()
	var dag *layer4.DAG
	var err error
	if e.mode == ValidationCapability {
		res := layer4.NewResolver(g, layer4.WithStack(g.Stack()))
		dag, err = res.BuildDAG(func(stage layer4.Stage) (layer4.Validator, error) {
			switch stage {
			case layer4.StageStructural:
				return layer4.NewStructuralValidator(e.sor), nil
			case layer4.StageSyntax:
				return layer4.NewSyntaxValidator(e.root), nil
			case layer4.StageLint:
				return layer4.LintValidator(g, e.root)
			case layer4.StageBuild:
				return layer4.BuildValidator(g, e.root)
			case layer4.StageTest:
				return layer4.TestValidator(g, e.root)
			default:
				return nil, layer4.ErrNoValidator
			}
		})
		if err != nil {
			return nil, fmt.Errorf("pipeline: build validation plan: %w", err)
		}
	} else {
		dag = layer4.New()
		if err := dag.AddNode(string(layer4.StageStructural), layer4.StageStructural, layer4.NewStructuralValidator(e.sor)); err != nil {
			return nil, err
		}
		if err := dag.AddNode(string(layer4.StageSyntax), layer4.StageSyntax, layer4.NewSyntaxValidator(e.root)); err != nil {
			return nil, err
		}
	}
	res, err := dag.Execute(ctx, patches)
	e.emit(telemetry.NewValidationDAG(res, e.clock().Sub(start)))
	if err != nil && res == nil {
		return res, err
	}
	return res, err
}

// Run executes the full six-step pipeline for a request:
//
//	Step 1  Layer 0 knowledge resolution (absolute constraints)
//	Step 2  Layer 1 capability detection (authoritative stack)
//	Step 3  Layer 2 governed ExecutionContext from the lea SoR
//	Step 4  Layer 3 PolicyGuard + stateless worker under the routed model
//	Step 5  Layer 4 validation DAG (RAM structural checks first)
//	Step 6  Layer 5 telemetry events published throughout
//
// All lifecycle events are published to the telemetry bus. Run never falls
// back to legacy context gathering or heuristic stack detection.
func (e *Engine) Run(ctx context.Context, req Request) (*Result, error) {
	k, err := e.Knowledge(ctx)
	if err != nil {
		return nil, err
	}
	g, err := e.Capabilities()
	if err != nil {
		return nil, err
	}

	route := e.RouteForMode(req.Mode)
	exec, err := e.Context(ctx, req, route.Policy)
	if err != nil {
		return nil, err
	}

	execIntent := req.Intent
	if execIntent == "" {
		execIntent = layer3.IntentRefactor
	}
	l3req := e.toLayer3Request(req, execIntent)

	pipeline := e.newPipeline(route, exec)
	run, err := pipeline.Execute(ctx, l3req)
	if err != nil {
		e.emitPipelineSteps(run)
		return &Result{
			Knowledge: k, Capabilities: g, Context: exec, Route: route,
			Tokens: runTokens(run),
		}, err
	}

	patches := make([]layer3.FilePatch, 0)
	if run != nil && run.Result() != nil {
		patches = run.Result().Patches
	}
	e.emitPipelineSteps(run)

	val, verr := e.validateWithGraph(ctx, patches, g)

	result := &Result{
		Knowledge:    k,
		Capabilities: g,
		Context:      exec,
		Route:        route,
		Patches:      patches,
		Run:          run,
		Validation:   val,
		Tokens:       runTokens(run),
	}
	if verr != nil {
		return result, fmt.Errorf("pipeline: validation: %w", verr)
	}
	return result, nil
}

// toLayer3Request projects a pipeline request onto a Layer 3 request.
func (e *Engine) toLayer3Request(req Request, intent layer3.Intent) layer3.Request {
	return layer3.Request{
		Intent:       intent,
		TargetFile:   req.TargetFile,
		TargetSymbol: req.TargetSymbol,
		NewName:      req.NewName,
		NewImport:    req.NewImport,
		Description:  req.Description,
		Scope:        req.Scope,
	}
}

// newPipeline builds a fresh Layer 3 pipeline for one run. The pipeline is
// per-run so the routed worker, the pre-assembled context and the policy guard
// (over the freshly detected capability graph) never leak across concurrent
// requests.
func (e *Engine) newPipeline(route RouteProfile, exec *layer2.ExecutionContext) *layer3.Pipeline {
	g, err := e.detectCapabilities()
	if err != nil {
		g = nil
	}
	opts := []layer3.PipelineOption{
		layer3.WithExecutionContextProvider(fixedContextProvider{exec: exec}),
		layer3.WithValidator(layer3.StructuralValidator{Root: e.root}),
		layer3.WithRoot(e.root),
		layer3.WithClock(e.clock),
	}
	if e.client != nil {
		opts = append(opts, layer3.WithWorker(newRouteWorker(route, e.client)))
	}
	return layer3.NewPipeline(layer3.NewPolicyGuard(g), e.ast, opts...)
}

// emitPipelineSteps publishes the Layer 3 stage transitions of a finished run
// to the telemetry bus.
func (e *Engine) emitPipelineSteps(run *layer3.Run) {
	if run == nil {
		return
	}
	st := run.State()
	intent := st.Intent
	for _, ev := range st.Events {
		d := ev.At.Sub(st.StartedAt)
		switch ev.State {
		case layer3.StateFailed:
			e.emit(telemetry.NewPipelineStepFailed(st.ID, intent, st.Route, ev.Stage, ev.Index, errors.New(ev.Err), d))
		case layer3.StateCancelled:
			e.emit(telemetry.NewPipelineStepCancelled(st.ID, intent, st.Route, ev.Stage, ev.Index, errors.New(ev.Err), d))
		default:
			e.emit(telemetry.NewPipelineStepDone(st.ID, intent, st.Route, ev.Stage, ev.Index, patchCountOf(st), tokensOf(run), d))
		}
	}
}

// patchCountOf returns the number of patches in a pipeline state snapshot.
func patchCountOf(st layer3.PipelineState) int {
	if st.Result == nil {
		return 0
	}
	return len(st.Result.Patches)
}

// fixedContextProvider returns the pre-assembled Layer 2 context.
type fixedContextProvider struct {
	exec *layer2.ExecutionContext
}

// Provide implements layer3.ExecutionContextProvider.
func (p fixedContextProvider) Provide(_ context.Context, _ layer3.Request) (*layer2.ExecutionContext, error) {
	return p.exec, nil
}

// newRouteWorker builds a stateless worker executing under the routed model.
func newRouteWorker(route RouteProfile, client layer3.WorkerClient) layer3.Worker {
	return &routeWorker{
		provider:  route.Provider,
		model:     route.Model,
		client:    client,
		parser:    layer3.LinePatchParser{},
		maxPrompt: 64000,
	}
}

// routeWorker is the routed stateless worker. It is identical to the Layer 3
// StatelessWorker but resolves the model from the route at execution time so a
// single engine can serve every intent tier.
type routeWorker struct {
	provider  string
	model     string
	client    layer3.WorkerClient
	parser    layer3.PatchParser
	maxPrompt int
}

// Name implements layer3.Worker.
func (w *routeWorker) Name() string { return w.provider }

// Execute implements layer3.Worker.
func (w *routeWorker) Execute(ctx context.Context, exec *layer2.ExecutionContext, req layer3.Request) (*layer3.WorkerResult, error) {
	if w.client == nil {
		return nil, ErrNoClient
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prompt := layer3.BuildPrompt(exec, req, w.maxPrompt)
	resp, err := w.client.Complete(ctx, &layer3.CompletionRequest{
		Provider: layer3.Provider(w.provider),
		Model:    w.model,
		Prompt:   prompt,
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline: %s completion: %w", w.provider, err)
	}
	patches, err := w.parser.Parse(resp.Text)
	if err != nil {
		return nil, err
	}
	return &layer3.WorkerResult{
		Patches: patches,
		Reason:  req.Description,
		Tokens:  resp.Tokens,
		Raw:     resp.Text,
	}, nil
}

// runTokens returns the token usage of a finished run.
func runTokens(run *layer3.Run) layer3.TokenUsage {
	if run == nil || run.Result() == nil {
		return layer3.TokenUsage{}
	}
	return run.Result().Tokens
}

func tokensOf(run *layer3.Run) int {
	return runTokens(run).Total()
}

// emptyContext returns a policy-governed but empty execution context for
// requests with no explicit target. It is the deliberate non-fallback: the
// engine never fabricates a target to satisfy Layer 2.
func emptyContext(policy layer2.ContextPolicy) *layer2.ExecutionContext {
	return &layer2.ExecutionContext{
		Files:   []layer2.FileContext{},
		Symbols: []layer2.SymbolInfo{},
		Imports: map[string][]string{},
		Stats: layer2.ContextStats{
			BudgetTokens: policy.MaxTokenBudget,
			BudgetMet:    true,
		},
		Policy:  policy,
		BuiltAt: time.Now(),
	}
}
