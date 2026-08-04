package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
	"github.com/PizenLabs/izen/pkg/runtime/metrics"
	"github.com/PizenLabs/izen/pkg/runtime/planner"
	"github.com/PizenLabs/izen/pkg/runtime/policy"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// Sentinel errors returned by the engine.
var (
	// ErrNoStrategy is returned when the plan selects a strategy that is not
	// registered.
	ErrNoStrategy = errors.New("engine: strategy not registered")
	// ErrPolicyDenied is returned when the policy does not grant the
	// selected strategy or one of its required capabilities.
	ErrPolicyDenied = errors.New("engine: policy denied execution")
	// ErrCapabilityUnmet is returned when a required capability has no
	// registered provider.
	ErrCapabilityUnmet = errors.New("engine: required capability has no provider")
	// ErrNoRecovery is returned when a step fails and no recovery handler is
	// configured.
	ErrNoRecovery = errors.New("engine: no recovery handler configured")
	// ErrRecoveryFailed is returned when the recovery handler itself fails.
	ErrRecoveryFailed = errors.New("engine: recovery failed")
)

// Request is a single run handed to the engine.
type Request struct {
	ID       string
	Mode     string
	Input    string
	Targets  []string
	Strategy string
}

// Result is the immutable outcome of a run: every step's output, the final
// state, the transition history and the emitted metrics.
type Result struct {
	RunID       string
	Request     Request
	State       State
	Facts       *analyzer.Facts
	Plan        *planner.Plan
	Decision    *policy.Decision
	Execution   *registry.Result
	Validation  *registry.ValidationResult
	Recovered   bool
	Err         error
	Metrics     []metrics.Metric
	Transitions []Transition
}

// RecoverFunc rolls a failed run back to a safe point. It is invoked after
// Execute or Validate fails; returning nil marks the run as successfully
// recovered (terminal StateRecovered), returning an error marks it Failed.
type RecoverFunc func(ctx context.Context, run *Result) error

// Engine is the v1 runtime orchestrator. It owns only orchestration and
// state transitions; every step is delegated to the injected packages. The
// Engine is safe for concurrent use: each Run builds its own state machine
// and result.
type Engine struct {
	root         string
	analyzer     *analyzer.Analyzer
	planner      *planner.Planner
	policy       *policy.Evaluator
	strategies   *registry.StrategyRegistry
	capabilities *registry.CapabilityRegistry
	validations  *registry.ValidationRegistry
	metrics      *metrics.Collector
	recoverFn    RecoverFunc
	clock        func() time.Time
}

// Option configures an Engine.
type Option func(*Engine)

// WithAnalyzer injects the workspace analyzer.
func WithAnalyzer(a *analyzer.Analyzer) Option {
	return func(e *Engine) {
		if a != nil {
			e.analyzer = a
		}
	}
}

// WithPlanner injects the execution planner.
func WithPlanner(p *planner.Planner) Option {
	return func(e *Engine) {
		if p != nil {
			e.planner = p
		}
	}
}

// WithPolicy injects the declarative policy evaluator.
func WithPolicy(p *policy.Evaluator) Option {
	return func(e *Engine) {
		if p != nil {
			e.policy = p
		}
	}
}

// WithStrategies injects the strategy registry.
func WithStrategies(r *registry.StrategyRegistry) Option {
	return func(e *Engine) {
		if r != nil {
			e.strategies = r
		}
	}
}

// WithCapabilities injects the capability registry.
func WithCapabilities(r *registry.CapabilityRegistry) Option {
	return func(e *Engine) {
		if r != nil {
			e.capabilities = r
		}
	}
}

// WithValidations injects the validation pipeline registry.
func WithValidations(r *registry.ValidationRegistry) Option {
	return func(e *Engine) {
		if r != nil {
			e.validations = r
		}
	}
}

// WithMetrics injects the metrics collector.
func WithMetrics(c *metrics.Collector) Option {
	return func(e *Engine) {
		if c != nil {
			e.metrics = c
		}
	}
}

// WithRecovery installs the rollback/recovery hook invoked when a step
// fails. Without it, failures terminate in StateFailed.
func WithRecovery(fn RecoverFunc) Option {
	return func(e *Engine) {
		if fn != nil {
			e.recoverFn = fn
		}
	}
}

// WithClock overrides the engine clock (test seam).
func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		if now != nil {
			e.clock = now
		}
	}
}

// New returns an engine over the workspace at root with default in-process
// components. No strategies or capabilities are registered by default; call
// the registry accessors to install them before running.
func New(root string, opts ...Option) *Engine {
	e := &Engine{
		root:         root,
		analyzer:     analyzer.New(root),
		planner:      planner.New(),
		policy:       policy.New(),
		strategies:   registry.NewStrategyRegistry(),
		capabilities: registry.NewCapabilityRegistry(),
		validations:  registry.NewValidationRegistry(),
		metrics:      metrics.NewCollector(),
		clock:        time.Now,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Root returns the workspace root the engine operates on.
func (e *Engine) Root() string { return e.root }

// Strategies returns the strategy registry for plugin installation.
func (e *Engine) Strategies() *registry.StrategyRegistry { return e.strategies }

// Capabilities returns the capability registry for provider installation.
func (e *Engine) Capabilities() *registry.CapabilityRegistry { return e.capabilities }

// Validations returns the validation pipeline registry.
func (e *Engine) Validations() *registry.ValidationRegistry { return e.validations }

// Metrics returns the metrics collector.
func (e *Engine) Metrics() *metrics.Collector { return e.metrics }

// Policy returns the declarative policy evaluator.
func (e *Engine) Policy() *policy.Evaluator { return e.policy }

// runner bundles the per-run state machine, result and metric ledger.
type runner struct {
	e       *Engine
	sm      *StateMachine
	run     *Result
	emitted []metrics.Metric
}

// emit records a metric in the run ledger and forwards it to the collector.
func (r *runner) emit(phase string, status metrics.Status, tokens int, strategy string, dur time.Duration, err string) metrics.Metric {
	m := metrics.Metric{
		RunID: r.run.RunID, Phase: phase, Status: status,
		Tokens: tokens, Strategy: strategy, Latency: dur, Err: err,
	}
	r.e.metrics.Emit(m)
	r.emitted = append(r.emitted, m)
	return m
}

// to advances the state machine and mirrors the current state onto the
// result.
func (r *runner) to(next State) error {
	if err := r.sm.Transition(next); err != nil {
		return err
	}
	r.run.State = next
	return nil
}

// finalize snapshots the transition history and metric ledger onto the
// result.
func (r *runner) finalize() {
	r.run.Transitions = r.sm.History()
	r.run.Metrics = r.emitted
}

// Run drives the full state machine for a request:
//
//	Receive -> Analyze -> Plan -> EvaluatePolicy -> Execute -> Validate ->
//	Done, with Execute/Validate failures routing to Recover/Rollback.
//
// It never falls back or retries implicitly: the outcome is always a
// deterministic terminal state.
func (e *Engine) Run(ctx context.Context, req Request) (*Result, error) {
	runID := req.ID
	if runID == "" {
		runID = "run-" + strconv.FormatInt(e.clock().UnixNano(), 36)
	}
	r := &runner{
		e:       e,
		sm:      NewStateMachine(),
		run:     &Result{RunID: runID, Request: req, State: StateIdle},
		emitted: make([]metrics.Metric, 0, 8),
	}
	run := r.run

	// Receive.
	if err := r.to(StateReceived); err != nil {
		return run, err
	}
	r.emit("receive", metrics.StatusOK, 0, req.Strategy, 0, "")

	// Analyze.
	start := e.clock()
	facts, err := e.analyzer.Analyze(ctx, analyzer.Request{Input: req.Input, Targets: req.Targets})
	dur := e.clock().Sub(start)
	if err != nil {
		r.emit("analyze", metrics.StatusFailed, 0, req.Strategy, dur, err.Error())
		return r.failUnrecoverable(run, err)
	}
	run.Facts = facts
	if err := r.to(StateAnalyzed); err != nil {
		return run, err
	}
	r.emit("analyze", metrics.StatusOK, facts.TokenEstimate, req.Strategy, dur, "")

	// Plan.
	start = e.clock()
	plan, err := e.planner.Build(facts)
	dur = e.clock().Sub(start)
	if err != nil {
		r.emit("plan", metrics.StatusFailed, 0, req.Strategy, dur, err.Error())
		return r.failUnrecoverable(run, err)
	}
	run.Plan = plan
	if err := r.to(StatePlanned); err != nil {
		return run, err
	}
	r.emit("plan", metrics.StatusOK, 0, plan.Strategy, dur, "")

	// EvaluatePolicy.
	start = e.clock()
	decision := e.policy.Evaluate(facts)
	dur = e.clock().Sub(start)
	run.Decision = decision
	if err := r.to(StatePolicyOK); err != nil {
		return run, err
	}
	r.emit("policy", metrics.StatusOK, 0, plan.Strategy, dur, "")

	// Gate: strategy registered, granted by policy, capabilities resolved.
	strategyName := plan.Strategy
	if req.Strategy != "" {
		strategyName = req.Strategy
	}
	exec, required, ok := e.strategies.Get(strategyName)
	if !ok {
		return r.failUnrecoverable(run, fmt.Errorf("%w: %s", ErrNoStrategy, strategyName))
	}
	if !decision.ApprovedFor(strategyName, required) {
		return r.failUnrecoverable(run, fmt.Errorf("%w: strategy=%s", ErrPolicyDenied, strategyName))
	}
	for _, c := range required {
		if len(e.capabilities.ProvidersFor(c)) == 0 {
			return r.failUnrecoverable(run, fmt.Errorf("%w: %s", ErrCapabilityUnmet, c))
		}
	}

	// Execute.
	if err := r.to(StateExecuting); err != nil {
		return run, err
	}
	task := registry.Task{
		RunID:           runID,
		Input:           req.Input,
		Action:          plan.Strategy,
		Targets:         facts.TargetFiles,
		ExpectedOutputs: plan.ExpectedOutputs,
		Checkpoint:      plan.Checkpoint,
		RollbackEnabled: plan.RollbackEnabled,
		TokensBudget:    facts.TokenEstimate,
	}
	start = e.clock()
	res, execErr := exec.Execute(ctx, task)
	dur = e.clock().Sub(start)
	if execErr == nil && res == nil {
		execErr = errors.New("engine: strategy returned nil result")
	}
	if execErr != nil {
		r.emit("execute", metrics.StatusFailed, 0, strategyName, dur, execErr.Error())
		return r.attemptRecovery(ctx, execErr, strategyName)
	}
	if res.Status == registry.StatusFailed {
		err := res.Err
		if err == nil {
			err = errors.New("engine: strategy reported failure")
		}
		r.emit("execute", metrics.StatusFailed, 0, strategyName, dur, err.Error())
		return r.attemptRecovery(ctx, err, strategyName)
	}
	run.Execution = res
	r.emit("execute", metrics.StatusOK, res.Tokens, strategyName, dur, "")
	if err := r.to(StateValidating); err != nil {
		return run, err
	}

	// Validate.
	if validators := e.validations.Pipeline(); len(validators) > 0 {
		targets := facts.TargetFiles
		if len(targets) == 0 {
			r.emit("validate", metrics.StatusSkipped, 0, strategyName, 0, "no target files to validate")
		} else {
			start = e.clock()
			val := e.validations.Run(ctx, targets)
			dur = e.clock().Sub(start)
			run.Validation = val
			if val.OK {
				r.emit("validate", metrics.StatusOK, 0, strategyName, dur, "")
			} else {
				verr := errors.New("engine: validation failed")
				if val.Err != nil {
					verr = val.Err
				}
				r.emit("validate", metrics.StatusFailed, 0, strategyName, dur, verr.Error())
				return r.attemptRecovery(ctx, verr, strategyName)
			}
		}
	} else {
		r.emit("validate", metrics.StatusSkipped, 0, strategyName, 0, "no validators registered")
	}

	// Done.
	if err := r.to(StateDone); err != nil {
		return run, err
	}
	r.finalize()
	return run, nil
}

// failUnrecoverable terminates the run in StateFailed without attempting
// recovery. It is used for pre-execution step failures (analyze, plan,
// policy gate) where rollback is meaningless.
func (r *runner) failUnrecoverable(run *Result, err error) (*Result, error) {
	run.Err = err
	_ = r.sm.Transition(StateFailed)
	run.State = StateFailed
	r.finalize()
	return run, err
}

// attemptRecovery routes a failed Execute/Validate step through Recovering
// to either Recovered (successful rollback) or Failed.
func (r *runner) attemptRecovery(ctx context.Context, cause error, strategy string) (*Result, error) {
	r.run.Err = cause
	if err := r.sm.Transition(StateRecovering); err != nil {
		return r.failUnrecoverable(r.run, cause)
	}
	r.run.State = StateRecovering
	if r.e.recoverFn == nil {
		r.emit("recover", metrics.StatusFailed, 0, strategy, 0, "no recovery handler")
		return r.failUnrecoverable(r.run, fmt.Errorf("%w: %w", ErrNoRecovery, cause))
	}
	start := r.e.clock()
	recErr := r.e.recoverFn(ctx, r.run)
	dur := r.e.clock().Sub(start)
	if recErr != nil {
		r.emit("recover", metrics.StatusFailed, 0, strategy, dur, recErr.Error())
		return r.failUnrecoverable(r.run, fmt.Errorf("%w: %w", ErrRecoveryFailed, recErr))
	}
	r.emit("recover", metrics.StatusOK, 0, strategy, dur, "")
	_ = r.sm.Transition(StateRecovered)
	r.run.State = StateRecovered
	r.run.Recovered = true
	r.finalize()
	return r.run, nil
}
