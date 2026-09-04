package layer3

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/internal/runtime/substrate"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// Sentinel errors returned by the Pipeline engine.
var (
	ErrNoWorker         = errors.New("layer3: no execution worker configured")
	ErrNoAstHandler     = errors.New("layer3: no AST rewriter configured")
	ErrStageFailed      = errors.New("layer3: pipeline stage failed")
	ErrValidationFailed = errors.New("layer3: validation failed")
	ErrEmptyPatch       = errors.New("layer3: worker produced no patches")
)

// Stage identifies a pipeline execution stage.
type Stage string

const (
	// StageUnderstand analyzes the request and gathers execution context.
	StageUnderstand Stage = "understand"
	// StagePlan builds an execution plan for generative tasks.
	StagePlan Stage = "plan"
	// StageExecute performs the task via the AST rewriter or the worker.
	StageExecute Stage = "execute"
	// StageReview inspects the proposed patches deterministically.
	StageReview Stage = "review"
	// StageValidate validates the outcome.
	StageValidate Stage = "validate"
)

// String returns the machine-readable stage label.
func (s Stage) String() string { return string(s) }

// State is a pipeline lifecycle state.
type State string

const (
	// StatePending is the run before execution starts.
	StatePending State = "pending"
	// StateRunning is the run while a stage executes.
	StateRunning State = "running"
	// StateDone is the run after all stages succeed.
	StateDone State = "done"
	// StateFailed is the run after a stage fails.
	StateFailed State = "failed"
	// StateCancelled is the run after the context is cancelled.
	StateCancelled State = "cancelled"
)

// String returns the machine-readable state label.
func (s State) String() string { return string(s) }

// StageEvent records a stage transition.
type StageEvent struct {
	Stage  Stage
	Index  int
	State  State
	Detail string
	Err    string
	At     time.Time
}

// RunResult is the outcome of a completed pipeline run.
type RunResult struct {
	Patches   []FilePatch
	Summary   string
	Tokens    TokenUsage
	Validated bool
}

// PipelineState is the immutable snapshot of a run. Transitions produce fresh
// values; callers must treat returned snapshots as read-only.
type PipelineState struct {
	ID        string
	Intent    Intent
	Route     Route
	Stages    []Stage
	Current   int
	Phase     Stage
	State     State
	Result    *RunResult
	Err       error
	Events    []StageEvent
	StartedAt time.Time
	UpdatedAt time.Time
}

// Validator validates a run's proposed patches.
type Validator interface {
	Validate(ctx context.Context, patches []FilePatch) (*ValidationResult, error)
}

// ValidationResult reports the outcome of a validation pass.
type ValidationResult struct {
	OK     bool
	Output string
}

// ExecutionContextProvider supplies the Layer 2 execution context for a
// generative run.
type ExecutionContextProvider interface {
	Provide(ctx context.Context, req Request) (*layer2.ExecutionContext, error)
}

// Pipeline is the dynamic state-machine pipeline engine. It is immutable
// after construction and safe for concurrent use: each run owns its own
// PipelineState and transitions it exclusively.
type Pipeline struct {
	guard        *PolicyGuard
	ast          *ASTRewriteHandler
	worker       Worker
	validator    Validator
	execProvider ExecutionContextProvider
	root         string
	now          func() time.Time
}

// PipelineOption configures a Pipeline.
type PipelineOption func(*Pipeline)

// WithValidator installs the Validator used by the Validate stage. It
// defaults to a StructuralValidator.
func WithValidator(v Validator) PipelineOption {
	return func(p *Pipeline) { p.validator = v }
}

// WithWorker installs the generative execution worker.
func WithWorker(w Worker) PipelineOption {
	return func(p *Pipeline) { p.worker = w }
}

// WithExecutionContextProvider installs the Layer 2 context provider.
func WithExecutionContextProvider(prov ExecutionContextProvider) PipelineOption {
	return func(p *Pipeline) { p.execProvider = prov }
}

// WithRoot sets the workspace root used for patch containment checks.
func WithRoot(root string) PipelineOption {
	return func(p *Pipeline) { p.root = root }
}

// WithClock overrides the pipeline clock (test seam).
func WithClock(now func() time.Time) PipelineOption {
	return func(p *Pipeline) {
		if now != nil {
			p.now = now
		}
	}
}

// NewPipeline returns a pipeline wired to the given guard and AST rewriter.
func NewPipeline(guard *PolicyGuard, ast *ASTRewriteHandler, opts ...PipelineOption) *Pipeline {
	p := &Pipeline{
		guard: guard,
		ast:   ast,
		now:   time.Now,
	}
	for _, o := range opts {
		o(p)
	}
	if p.validator == nil {
		p.validator = StructuralValidator{Root: p.root}
	}
	return p
}

// Guard returns the policy guard backing the pipeline.
func (p *Pipeline) Guard() *PolicyGuard { return p.guard }

// Route inspects the request through the policy guard.
func (p *Pipeline) Route(req Request) (Route, error) {
	return p.guard.Route(req)
}

// StagesFor returns the dynamically constructed stage plan for req:
//
//	simple:     Understand -> Execute -> Validate
//	generative: Understand -> Plan -> Execute -> Review -> Validate
//
// The workspace capabilities shape the Validate strategy (selected via
// guard.ValidationMode) rather than the presence of the stage.
func (p *Pipeline) StagesFor(req Request) []Stage {
	route, err := p.guard.Route(req)
	if err != nil || route == RouteASTRewrite {
		return []Stage{StageUnderstand, StageExecute, StageValidate}
	}
	return []Stage{StageUnderstand, StagePlan, StageExecute, StageReview, StageValidate}
}

var runIDCounter atomic.Uint64

func newRunID() string {
	return fmt.Sprintf("run-%d", runIDCounter.Add(1))
}

// NewRun builds a run for req, validating it through the policy guard and
// wiring the route-specific stage plan.
func (p *Pipeline) NewRun(req Request) (*Run, error) {
	if p.guard == nil {
		return nil, fmt.Errorf("%w: no policy guard", ErrInvalidRequest)
	}
	route, err := p.guard.Route(req)
	if err != nil {
		return nil, err
	}
	if route == RouteASTRewrite && p.ast == nil {
		return nil, ErrNoAstHandler
	}
	if route == RouteGenerative && p.worker == nil {
		return nil, ErrNoWorker
	}
	now := p.now()
	return &Run{
		p:     p,
		req:   req,
		route: route,
		state: PipelineState{
			ID:        newRunID(),
			Intent:    req.Intent,
			Route:     route,
			Stages:    p.StagesFor(req),
			Current:   -1,
			State:     StatePending,
			StartedAt: now,
			UpdatedAt: now,
		},
	}, nil
}

// Execute builds a run for req and drives it to completion.
func (p *Pipeline) Execute(ctx context.Context, req Request) (*Run, error) {
	run, err := p.NewRun(req)
	if err != nil {
		return nil, err
	}
	if _, err := run.Execute(ctx); err != nil {
		return run, err
	}
	return run, nil
}

// Run is a single pipeline execution. It owns an immutable PipelineState that
// is advanced only through transition methods; State returns deep copies, so
// concurrent readers never observe or corrupt in-flight transitions.
type Run struct {
	p     *Pipeline
	req   Request
	route Route

	mu    sync.Mutex
	state PipelineState

	// exec is the Layer 2 execution context gathered by the Understand stage.
	// It is written and read exclusively by the Execute goroutine.
	exec *layer2.ExecutionContext
}

// State returns an immutable snapshot of the current pipeline state.
func (r *Run) State() PipelineState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneState(r.state)
}

// Result returns the run result snapshot, if the run has produced one.
func (r *Run) Result() *RunResult {
	st := r.State()
	if st.Result == nil {
		return nil
	}
	res := *st.Result
	res.Patches = append([]FilePatch(nil), st.Result.Patches...)
	return &res
}

func (r *Run) set(mut func(*PipelineState)) PipelineState {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneState(r.state)
	mut(&next)
	next.UpdatedAt = r.p.now()
	r.state = next
	return next
}

func (r *Run) recordEvent(stage Stage, index int, state State, detail, errMsg string) {
	r.set(func(s *PipelineState) {
		s.Events = append(s.Events, StageEvent{
			Stage:  stage,
			Index:  index,
			State:  state,
			Detail: detail,
			Err:    errMsg,
			At:     r.p.now(),
		})
	})
}

// Execute drives the state machine to completion. It should be called once
// per run; concurrent calls observe the final state and return it.
func (r *Run) Execute(ctx context.Context) (PipelineState, error) {
	r.set(func(s *PipelineState) { s.State = StateRunning })
	for {
		st := r.State()
		if st.State == StateFailed || st.State == StateCancelled {
			return r.State(), nil
		}
		next := st.Current + 1
		if next >= len(st.Stages) {
			return r.finish(ctx)
		}
		stage := st.Stages[next]
		r.set(func(s *PipelineState) {
			s.Current = next
			s.Phase = stage
			s.State = StateRunning
		})
		r.recordEvent(stage, next, StateRunning, "stage started", "")
		if err := ctx.Err(); err != nil {
			return r.fail(stage, next, err)
		}
		if err := r.runStage(ctx, stage, next); err != nil {
			return r.fail(stage, next, err)
		}
	}
}

func (r *Run) finish(ctx context.Context) (PipelineState, error) {
	if err := ctx.Err(); err != nil {
		return r.cancel(err)
	}
	r.set(func(s *PipelineState) {
		s.State = StateDone
		if s.Result == nil {
			s.Result = &RunResult{}
		}
		s.Result.Summary = summarize(r.req)
	})
	return r.State(), nil
}

func (r *Run) fail(stage Stage, index int, cause error) (PipelineState, error) {
	r.recordEvent(stage, index, StateFailed, "stage failed", cause.Error())
	r.set(func(s *PipelineState) {
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			s.State = StateCancelled
		} else {
			s.State = StateFailed
		}
		s.Phase = stage
		s.Err = cause
	})
	return r.State(), cause
}

func (r *Run) cancel(cause error) (PipelineState, error) {
	r.set(func(s *PipelineState) {
		s.State = StateCancelled
		s.Err = cause
	})
	return r.State(), cause
}

func summarize(req Request) string {
	if req.Description != "" {
		return req.Description
	}
	if req.TargetSymbol != "" {
		return string(req.Intent) + " " + req.TargetSymbol
	}
	return string(req.Intent)
}

func (r *Run) runStage(ctx context.Context, stage Stage, index int) error {
	switch stage {
	case StageUnderstand:
		return r.understand(ctx, index)
	case StagePlan:
		return r.plan(ctx, index)
	case StageExecute:
		return r.execute(ctx, index)
	case StageReview:
		return r.review(ctx, index)
	case StageValidate:
		return r.validate(ctx, index)
	default:
		return fmt.Errorf("%w: unknown stage %q", ErrStageFailed, stage)
	}
}

func (r *Run) understand(ctx context.Context, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	detail := fmt.Sprintf("understood %s task %q", r.route, r.req.Intent)
	if r.route == RouteGenerative && r.p.execProvider != nil {
		exec, err := r.p.execProvider.Provide(ctx, r.req)
		if err != nil {
			return fmt.Errorf("%w: understand: %w", ErrStageFailed, err)
		}
		r.exec = exec
		if exec != nil {
			detail = fmt.Sprintf("understood %s task %q over %d files", r.route, r.req.Intent, len(exec.Files))
		}
	}
	r.recordEvent(StageUnderstand, index, StateDone, detail, "")
	return nil
}

func (r *Run) plan(ctx context.Context, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	worker := "none"
	if r.p.worker != nil {
		worker = r.p.worker.Name()
	}
	r.recordEvent(StagePlan, index, StateDone, fmt.Sprintf("planned generative task via %s worker", worker), "")
	return nil
}

func (r *Run) execute(ctx context.Context, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch r.route {
	case RouteASTRewrite:
		pr, err := r.p.ast.Handle(ctx, r.req)
		if err != nil {
			return fmt.Errorf("%w: execute: %w", ErrStageFailed, err)
		}
		if pr == nil {
			return fmt.Errorf("%w: execute returned no result", ErrStageFailed)
		}
		r.setASTResult(pr)
	case RouteGenerative:
		if r.p.worker == nil {
			return ErrNoWorker
		}
		res, err := r.p.worker.Execute(ctx, r.exec, r.req)
		if err != nil {
			return fmt.Errorf("%w: execute: %w", ErrStageFailed, err)
		}
		if res == nil {
			return fmt.Errorf("%w: worker returned no result", ErrStageFailed)
		}
		r.setWorkerResult(res)
	}
	r.recordEvent(StageExecute, index, StateDone, "execute produced patches", "")
	return nil
}

func (r *Run) setASTResult(pr *PatchResult) {
	r.set(func(s *PipelineState) {
		if s.Result == nil {
			s.Result = &RunResult{}
		}
		s.Result.Patches = clonePatches(pr.Files)
		s.Result.Summary = fmt.Sprintf("%s: %d file(s) changed", r.req.Intent, pr.ChangedCount())
	})
}

func (r *Run) setWorkerResult(res *WorkerResult) {
	r.set(func(s *PipelineState) {
		if s.Result == nil {
			s.Result = &RunResult{}
		}
		s.Result.Patches = clonePatches(res.Patches)
		s.Result.Tokens = res.Tokens
		s.Result.Summary = res.Reason
	})
}

func (r *Run) review(ctx context.Context, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res := r.State().Result
	if res == nil || len(res.Patches) == 0 {
		return fmt.Errorf("%w: %w", ErrStageFailed, ErrEmptyPatch)
	}
	seen := make(map[string]bool)
	for _, p := range res.Patches {
		if p.Path == "" {
			return fmt.Errorf("%w: patch with empty path", ErrStageFailed)
		}
		if p.Path != filepath.Clean(p.Path) {
			return fmt.Errorf("%w: non-clean patch path %q", ErrStageFailed, p.Path)
		}
		if seen[p.Path] {
			return fmt.Errorf("%w: duplicate patch path %q", ErrStageFailed, p.Path)
		}
		seen[p.Path] = true
		if r.p.root != "" {
			if _, err := resolveWithin(r.p.root, p.Path); err != nil {
				return fmt.Errorf("%w: %w", ErrStageFailed, err)
			}
		}
		if strings.HasSuffix(p.Path, ".go") && !goParses(p.New) {
			return fmt.Errorf("%w: patch %q does not parse as Go", ErrStageFailed, p.Path)
		}
	}
	r.recordEvent(StageReview, index, StateDone, fmt.Sprintf("reviewed %d patch(es)", len(res.Patches)), "")
	return nil
}

func (r *Run) validate(ctx context.Context, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res := r.State().Result
	if res == nil || len(res.Patches) == 0 {
		return fmt.Errorf("%w: %w", ErrStageFailed, ErrEmptyPatch)
	}
	vres, err := r.p.validator.Validate(ctx, res.Patches)
	if err != nil {
		return fmt.Errorf("%w: validate: %w", ErrStageFailed, err)
	}
	if vres == nil {
		return fmt.Errorf("%w: validator returned no result", ErrStageFailed)
	}
	r.set(func(s *PipelineState) {
		if s.Result == nil {
			s.Result = &RunResult{}
		}
		s.Result.Validated = vres.OK
	})
	if !vres.OK {
		return fmt.Errorf("%w: %v", ErrValidationFailed, vres.Output)
	}
	r.recordEvent(StageValidate, index, StateDone, "validation passed", "")
	return nil
}

func clonePatches(in []FilePatch) []FilePatch {
	if len(in) == 0 {
		return nil
	}
	out := make([]FilePatch, len(in))
	copy(out, in)
	return out
}

func cloneState(s PipelineState) PipelineState {
	out := s
	out.Stages = append([]Stage(nil), s.Stages...)
	if s.Result != nil {
		res := *s.Result
		res.Patches = clonePatches(s.Result.Patches)
		out.Result = &res
	}
	out.Events = append([]StageEvent(nil), s.Events...)
	return out
}

// StructuralValidator is the deterministic, in-process validator. It checks
// that patch paths are contained within the workspace root and that every Go
// patch still parses. It never executes external commands.
type StructuralValidator struct {
	Root string
}

// Validate implements Validator.
func (v StructuralValidator) Validate(ctx context.Context, patches []FilePatch) (*ValidationResult, error) {
	for i := range patches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if res := checkStructural(patches[i], v.Root); res != nil {
			return res, nil
		}
	}
	return &ValidationResult{OK: true, Output: fmt.Sprintf("%d patch(es) structurally valid", len(patches))}, nil
}

// checkStructural validates a single patch, returning a non-nil result when it
// fails and nil when it is structurally sound.
func checkStructural(p FilePatch, root string) *ValidationResult {
	if p.Path == "" {
		return &ValidationResult{OK: false, Output: "empty patch path"}
	}
	if root != "" {
		if _, err := resolveWithin(root, p.Path); err != nil {
			return &ValidationResult{OK: false, Output: err.Error()}
		}
	}
	if strings.HasSuffix(p.Path, ".go") && !goParses(p.New) {
		return &ValidationResult{OK: false, Output: p.Path + ": does not parse as Go"}
	}
	return nil
}

// CommandValidator validates a run by executing a workspace capability
// command (build/test/lint). The proposed patches must already be applied to
// the working tree before Validate is called; the validator reports the
// command exit status.
type CommandValidator struct {
	Root string
	Cmd  string
}

// Validate implements Validator.
func (v CommandValidator) Validate(ctx context.Context, patches []FilePatch) (*ValidationResult, error) {
	return v.run(ctx), nil
}

// run executes via substrate helper; semantic layer never invokes exec directly.
func (v CommandValidator) run(ctx context.Context) *ValidationResult {
	fields := strings.Fields(v.Cmd)
	if len(fields) == 0 {
		return &ValidationResult{OK: false, Output: "no validation command configured"}
	}
	res := substrate.ExecCommand(ctx, v.Root, nil, fields)
	if res.Err != nil {
		if res.ExitCode == -1 {
			return &ValidationResult{OK: false, Output: res.Err.Error()}
		}
		return &ValidationResult{OK: false, Output: res.Stdout + res.Stderr}
	}
	if res.ExitCode != 0 {
		return &ValidationResult{OK: false, Output: res.Stdout + res.Stderr}
	}
	return &ValidationResult{OK: true, Output: res.Stdout}
}

func goParses(src string) bool {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "patch.go", []byte(src), parser.SkipObjectResolution)
	return err == nil
}
