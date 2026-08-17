package autonomy

import (
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// Engine is the human-authorized autonomous runtime: the decision layer above
// the execution modes. It owns intent classification (V2), capability-based
// workspace selection, the autonomy controller, the session grant ledger, and
// the autonomous loop. Every decision is observable through canonical events on
// the shared bus.
//
// Engine is safe for concurrent use: its configuration is immutable after
// construction, the grant ledger is internally synchronized, and event
// publishing is non-blocking.
type Engine struct {
	controller   *AutonomyController
	grants       *GrantLedger
	semantic     ClassifyFunc
	bus          *events.Bus
	scope        string
	riskFunc     func(target string) MutationRiskInput
	rollbackFunc func() bool
}

// Option configures the autonomy engine during construction.
type Option func(*Engine)

// WithEventBus wires the shared event bus so decisions, grants, loop steps and
// context compilations are published. Nil disables emission.
func WithEventBus(bus *events.Bus) Option {
	return func(e *Engine) {
		if bus != nil {
			e.bus = bus
		}
	}
}

// WithSemanticClassifier wires the semantic fallback used when the
// deterministic intent classifier finds no signal.
func WithSemanticClassifier(fn ClassifyFunc) Option {
	return func(e *Engine) {
		e.semantic = fn
	}
}

// WithScope sets the default grant scope (e.g. the workspace root or
// "current repository"). Defaults to "repository".
func WithScope(scope string) Option {
	return func(e *Engine) {
		if scope != "" {
			e.scope = scope
		}
	}
}

// WithRiskFunc wires a risk assessment function invoked with the resolved
// target. It lets the caller plug the execution RiskClassifier into the
// decision model. When unset, mutation risk is low.
func WithRiskFunc(fn func(target string) MutationRiskInput) Option {
	return func(e *Engine) {
		e.riskFunc = fn
	}
}

// WithRollbackFunc wires a rollback-availability probe. When unset, rollback
// is assumed available (the execution engine owns a checkpoint manager).
func WithRollbackFunc(fn func() bool) Option {
	return func(e *Engine) {
		e.rollbackFunc = fn
	}
}

// NewEngine builds the autonomous runtime. Grants is a fresh empty ledger;
// use Grants() to inspect or Grant() to authorize.
func NewEngine(opts ...Option) *Engine {
	e := &Engine{
		controller: NewAutonomyController(),
		grants:     NewGrantLedger(),
		scope:      "repository",
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Grants returns the session grant ledger.
func (e *Engine) Grants() *GrantLedger {
	if e == nil {
		return NewGrantLedger()
	}
	return e.grants
}

// Scope returns the default grant scope.
func (e *Engine) Scope() string {
	if e == nil {
		return "repository"
	}
	return e.scope
}

// Classify runs the intent V2 classifier and returns the intent result. It is
// a pure function of the input — no workspace, no risk, no grants.
func (e *Engine) Classify(input string) IntentResult {
	if e == nil {
		return Classify(input, nil)
	}
	return Classify(input, e.semantic)
}

// WorkspaceFor selects the capability domain for a classified intent, applying
// the mutation risk of the resolved target.
func (e *Engine) WorkspaceFor(res IntentResult) WorkspaceRoute {
	risk := RiskLow
	if res.RequiresMutation() {
		risk = e.riskFor(res.Target())
	}
	return SelectWorkspace(res.Intent, risk, res.Required)
}

// Authority reports whether an active session grant covers the required
// capabilities for the engine scope.
func (e *Engine) Authority(required CapabilitySet) bool {
	return e.Grants().Has(e.Scope(), required)
}

// Grant issues a session capability grant and publishes the CapabilityGranted
// event. After the grant, the runtime may act inside the boundary without
// asking again.
func (e *Engine) Grant(scope string, caps ...Capability) Grant {
	if scope == "" {
		scope = e.Scope()
	}
	g := e.Grants().GrantCapability(scope, caps...)
	if e != nil && e.bus != nil {
		capNames := make([]string, 0, len(g.Capabilities))
		for _, c := range g.Capabilities {
			capNames = append(capNames, string(c))
		}
		expires := ""
		if !g.ExpiresAt.IsZero() {
			expires = g.ExpiresAt.Format(time.RFC3339)
		}
		e.bus.Publish(events.NewCapabilityGranted(string(g.ID), g.Scope, capNames, expires))
	}
	return g
}

// GrantDefault is Grant against the engine's default scope.
func (e *Engine) GrantDefault(caps ...Capability) Grant {
	return e.Grant(e.Scope(), caps...)
}

// Trace is the full observable decision record for one prompt: the classified
// intent, the selected workspace, the controller verdict, and the capability
// grant request (when one is needed).
type Trace struct {
	Input     string
	Intent    IntentResult
	Route     WorkspaceRoute
	Decision  DecisionOutput
	Grant     GrantRequest
	Risk      RiskLevel
	ScopeSize int
	// Rollback reports whether a rollback checkpoint is available at decision
	// time. It is carried on the trace so the ask_user proposal surface can
	// present rollback availability without re-probing the execution layer.
	Rollback bool
}

// Decide is the main decision entry point. It classifies the input, selects the
// capability domain, evaluates the decision model against the session grants,
// and returns the full trace. It publishes the AutonomyDecision event so the
// verdict is observable.
func (e *Engine) Decide(input string, opts ...DecideOption) Trace {
	res := e.Classify(input)
	trace := Trace{Input: input, Intent: res}

	if !res.Intent.RequiresWorkspace() {
		out := DecisionOutput{
			Decision: DecisionDirectResponse,
			Reason:   "conversation intent — direct response, no workspace",
		}
		trace.Route = WorkspaceRoute{
			Workspace: WorkspaceAsk, Covers: true,
			Reason: "conversation intent — direct response, no workspace",
		}
		trace.Decision = out
		trace.Risk = RiskLow
		e.emitDecision(trace)
		return trace
	}

	risk := e.riskFor(res.Target())
	trace.Risk = risk

	route := e.WorkspaceFor(res)
	trace.Route = route

	do := DecideOptions{scopeSize: 1}
	for _, opt := range opts {
		opt(&do)
	}
	targetConfidence := 0.95
	if res.Target() == "" {
		targetConfidence = 0.5
	}
	if do.targetConfidence > 0 {
		targetConfidence = do.targetConfidence
	}

	scope := e.Scope()
	granted := e.Grants().ActiveCaps(scope)

	rollback := e.rollbackAvailable()
	trace.Rollback = rollback

	decInput := DecisionInput{
		Intent:           res.Intent,
		IntentConfidence: res.Confidence,
		TargetConfidence: targetConfidence,
		Target:           res.Target(),
		MutationRisk: MutationRiskInput{
			Level: risk,
		},
		AffectedScope:     do.scopeSize,
		RollbackAvailable: rollback,
		Granted:           granted,
	}
	out := e.controller.Decide(decInput)
	trace.Decision = out
	trace.ScopeSize = do.scopeSize

	if out.Decision == DecisionAskUser && len(out.Missing) > 0 {
		trace.Grant = NewGrantRequest(scope, out.Missing, res.Intent, res.Target(), risk, do.scopeSize)
	}

	e.emitDecision(trace)
	return trace
}

// DecideOption configures a single Decide call.
type DecideOption func(*DecideOptions)

// DecideOptions carries per-call overrides for the decision model.
type DecideOptions struct {
	scopeSize        int
	targetConfidence float64
}

// WithAffectedScope overrides the affected file count for the decision.
func WithAffectedScope(n int) DecideOption {
	return func(o *DecideOptions) {
		if n > 0 {
			o.scopeSize = n
		}
	}
}

// WithTargetConfidence overrides the target confidence for the decision.
func WithTargetConfidence(c float64) DecideOption {
	return func(o *DecideOptions) {
		if c > 0 {
			o.targetConfidence = c
		}
	}
}

// riskFor evaluates mutation risk through the wired risk function. Unknown
// targets and unset risk functions default to low.
func (e *Engine) riskFor(target string) RiskLevel {
	if e == nil || e.riskFunc == nil {
		return RiskLow
	}
	return e.riskFunc(target).Level
}

// rollbackAvailable probes rollback availability through the wired probe.
// Unset probes assume rollback is available (checkpoint manager exists).
func (e *Engine) rollbackAvailable() bool {
	if e == nil || e.rollbackFunc == nil {
		return true
	}
	return e.rollbackFunc()
}

// emitDecision publishes the autonomy decision event when a bus is wired.
func (e *Engine) emitDecision(t Trace) {
	if e == nil || e.bus == nil {
		return
	}
	missing := make([]string, 0, len(t.Decision.Missing))
	for _, c := range t.Decision.Missing {
		missing = append(missing, string(c))
	}
	e.bus.Publish(events.NewAutonomyDecision(
		string(t.Decision.Decision),
		string(t.Intent.Intent),
		t.Intent.Confidence,
		string(t.Route.Workspace),
		t.Risk.String(),
		missing,
		t.Decision.Reason,
	))
}

// CompileContext builds the structural understanding of an artifact and
// publishes the ContextCompiled event. The caller owns file access. It is the
// bus-observable entry point of the context intelligence pipeline.
func (e *Engine) CompileContext(path, content string) ArtifactContext {
	return e.Analyze(path, content)
}

// Analyze runs the full context intelligence pipeline over an artifact and
// publishes the ContextCompiled event carrying the intelligence fingerprint
// (language, analysis strategy, aggregate confidence). The caller owns file
// access. This is the engine-truth entry point: it compiles deterministic
// structural evidence BEFORE any AI reasoning is invoked.
func (e *Engine) Analyze(path, content string) ArtifactContext {
	ctx := CompileContext(path, content)
	if e != nil && e.bus != nil {
		e.bus.Publish(events.NewContextCompiledIntel(
			ctx.Path,
			string(ctx.Kind),
			len(ctx.Evidence()),
			ctx.Intelligence.Language,
			ctx.Intelligence.Strategy.String(),
			ctx.AggregateConfidence(),
		))
	}
	return ctx
}

// PublishTransitions publishes loop transitions onto the bus. Callers drive
// the autonomous loop and hand the recorded steps here so they stay observable.
func (e *Engine) PublishTransitions(trans []LoopTransition) {
	if e == nil || e.bus == nil || len(trans) == 0 {
		return
	}
	for _, t := range trans {
		e.bus.Publish(events.NewLoopTransition(string(t.From), string(t.To), string(t.Event), t.Reason))
	}
}
