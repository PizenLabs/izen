package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/pkg/runtime/metrics"
	"github.com/PizenLabs/izen/pkg/runtime/policy"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// stubStrategy is a configurable strategy plugin for engine tests. It is
// safe for concurrent use.
type stubStrategy struct {
	name  string
	res   *registry.Result
	err   error
	mu    sync.Mutex
	tasks []registry.Task
}

func (s *stubStrategy) Name() string { return s.name }

func (s *stubStrategy) Execute(_ context.Context, task registry.Task) (*registry.Result, error) {
	s.mu.Lock()
	s.tasks = append(s.tasks, task)
	s.mu.Unlock()
	return s.res, s.err
}

// captured returns the executed tasks.
func (s *stubStrategy) captured() []registry.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]registry.Task(nil), s.tasks...)
}

// permissivePolicy grants the patch strategy and its required capabilities.
func permissivePolicy() []policy.Rule {
	return []policy.Rule{{
		ID:     "allow_all",
		When:   policy.Matcher{},
		Allow:  []string{"strategy:patch", "capability:coding", "capability:tool_use"},
		Reason: "test policy",
	}}
}

// harness bundles a configured engine with its test doubles.
type harness struct {
	eng     *Engine
	dir     string
	metrics []metrics.Metric
	mu      sync.Mutex
}

func (h *harness) recordMetrics(m metrics.Metric) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.metrics = append(h.metrics, m)
	return nil
}

func (h *harness) metricPhases() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	phases := make([]string, 0, len(h.metrics))
	for _, m := range h.metrics {
		phases = append(phases, m.Phase+":"+string(m.Status))
	}
	return phases
}

// newHarness builds an engine over a temp workspace containing a.go.
func newHarness(t *testing.T, s registry.Strategy, recovery RecoverFunc, rules []policy.Rule, opts ...Option) *harness {
	t.Helper()
	h := &harness{dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(h.dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	strategies := registry.NewStrategyRegistry()
	if s != nil {
		if err := strategies.Register(s.Name(), s, registry.CapabilityCoding, registry.CapabilityToolUse); err != nil {
			t.Fatal(err)
		}
	}
	capabilities := registry.NewCapabilityRegistry()
	if err := capabilities.Register(registry.CapabilityCoding, "anthropic"); err != nil {
		t.Fatal(err)
	}
	if err := capabilities.Register(registry.CapabilityToolUse, "shell"); err != nil {
		t.Fatal(err)
	}

	eng := New(h.dir,
		WithStrategies(strategies),
		WithCapabilities(capabilities),
		WithPolicy(policy.New(rules...)),
		WithRecovery(recovery),
		WithMetrics(metrics.NewCollector().Sink(&recordingSink{emit: h.recordMetrics})),
	)
	for _, o := range opts {
		o(eng)
	}
	h.eng = eng
	return h
}

// recordingSink adapts a function into a metrics.Sink.
type recordingSink struct {
	emit func(metrics.Metric) error
}

func (s *recordingSink) Emit(m metrics.Metric) error {
	if s.emit == nil {
		return nil
	}
	return s.emit(m)
}

func TestRunHappyPath(t *testing.T) {
	strategy := &stubStrategy{
		name: "patch",
		res:  &registry.Result{Status: registry.StatusOK, Tokens: 12},
	}
	h := newHarness(t, strategy, nil, permissivePolicy())

	res, err := h.eng.Run(context.Background(), Request{
		ID:      "run-1",
		Input:   "fix the bug",
		Targets: []string{"a.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != StateDone {
		t.Errorf("State = %s, want done", res.State)
	}
	if res.Recovered {
		t.Error("happy path should not be recovered")
	}
	if res.Facts == nil || res.Facts.Intent != "bug_fix" {
		t.Errorf("facts intent = %v, want bug_fix", res.Facts.Intent)
	}
	if res.Plan == nil || !res.Plan.RequireTest {
		t.Error("bug_fix plan should require tests")
	}
	if res.Execution == nil || res.Execution.Status != registry.StatusOK {
		t.Error("execution result should be ok")
	}

	// Verify the task handed to the strategy carried the plan flags.
	captured := strategy.captured()
	if len(captured) != 1 {
		t.Fatalf("strategy executed %d times, want 1", len(captured))
	}
	task := captured[0]
	if !task.Checkpoint || !task.RollbackEnabled || task.RunID != "run-1" {
		t.Errorf("task = %+v", task)
	}

	// Verify the full transition chain reached done.
	got := statesOf(res.Transitions)
	want := []State{StateIdle, StateReceived, StateAnalyzed, StatePlanned, StatePolicyOK, StateExecuting, StateValidating, StateDone}
	if !equalStates(got, want) {
		t.Errorf("transitions = %v, want %v", got, want)
	}

	// Verify per-phase metrics.
	phases := h.metricPhases()
	for _, wantPhase := range []string{"receive:ok", "analyze:ok", "plan:ok", "policy:ok", "execute:ok", "validate:skipped"} {
		if !containsStr(phases, wantPhase) {
			t.Errorf("metrics %v missing %s", phases, wantPhase)
		}
	}
	if len(res.Metrics) != len(h.metrics) {
		t.Errorf("result metrics = %d, collector = %d", len(res.Metrics), len(h.metrics))
	}
}

func TestRunExecuteFailureRecovers(t *testing.T) {
	h := newHarness(t, &stubStrategy{
		name: "patch",
		err:  errors.New("boom"),
	}, func(_ context.Context, run *Result) error {
		run.Recovered = true
		return nil
	}, permissivePolicy())

	res, err := h.eng.Run(context.Background(), Request{ID: "run-2", Input: "fix the bug", Targets: []string{"a.go"}})
	if err != nil {
		t.Fatalf("recovered run should return nil error, got %v", err)
	}
	if res.State != StateRecovered {
		t.Errorf("State = %s, want recovered", res.State)
	}
	if !res.Recovered {
		t.Error("Recovered should be true")
	}
	if res.Err == nil {
		t.Error("Err should record the original cause")
	}
	got := statesOf(res.Transitions)
	want := []State{StateIdle, StateReceived, StateAnalyzed, StatePlanned, StatePolicyOK, StateExecuting, StateRecovering, StateRecovered}
	if !equalStates(got, want) {
		t.Errorf("transitions = %v, want %v", got, want)
	}
	phases := h.metricPhases()
	if !containsStr(phases, "execute:failed") || !containsStr(phases, "recover:ok") {
		t.Errorf("metrics %v missing failure/recovery entries", phases)
	}
}

func TestRunExecuteFailureNoRecovery(t *testing.T) {
	h := newHarness(t, &stubStrategy{
		name: "patch",
		err:  errors.New("boom"),
	}, nil, permissivePolicy())

	res, err := h.eng.Run(context.Background(), Request{ID: "run-3", Input: "fix the bug", Targets: []string{"a.go"}})
	if err == nil {
		t.Fatal("expected error when no recovery handler is configured")
	}
	if !errors.Is(err, ErrNoRecovery) {
		t.Errorf("err = %v, want ErrNoRecovery", err)
	}
	if res.State != StateFailed {
		t.Errorf("State = %s, want failed", res.State)
	}
	got := statesOf(res.Transitions)
	want := []State{StateIdle, StateReceived, StateAnalyzed, StatePlanned, StatePolicyOK, StateExecuting, StateRecovering, StateFailed}
	if !equalStates(got, want) {
		t.Errorf("transitions = %v, want %v", got, want)
	}
}

func TestRunRecoveryFails(t *testing.T) {
	h := newHarness(t, &stubStrategy{
		name: "patch",
		res:  &registry.Result{Status: registry.StatusFailed, Err: errors.New("boom")},
	}, func(_ context.Context, _ *Result) error {
		return errors.New("rollback failed")
	}, permissivePolicy())

	res, err := h.eng.Run(context.Background(), Request{ID: "run-4", Input: "fix the bug", Targets: []string{"a.go"}})
	if err == nil || !errors.Is(err, ErrRecoveryFailed) {
		t.Errorf("err = %v, want ErrRecoveryFailed", err)
	}
	if res.State != StateFailed {
		t.Errorf("State = %s, want failed", res.State)
	}
	phases := h.metricPhases()
	if !containsStr(phases, "recover:failed") {
		t.Errorf("metrics %v missing recover:failed", phases)
	}
}

func TestRunPolicyDenial(t *testing.T) {
	rules := []policy.Rule{{
		ID:     "deny_all",
		When:   policy.Matcher{},
		Deny:   []string{"strategy:patch"},
		Reason: "policy blocks patch",
	}}
	strategy := &stubStrategy{name: "patch", res: &registry.Result{Status: registry.StatusOK}}
	h := newHarness(t, strategy, nil, rules)

	res, err := h.eng.Run(context.Background(), Request{ID: "run-5", Input: "fix the bug", Targets: []string{"a.go"}})
	if err == nil || !errors.Is(err, ErrPolicyDenied) {
		t.Errorf("err = %v, want ErrPolicyDenied", err)
	}
	if res.State != StateFailed {
		t.Errorf("State = %s, want failed", res.State)
	}
	if len(strategy.captured()) != 0 {
		t.Error("denied run must not execute the strategy")
	}
	// Policy denial is unrecoverable: no recovering transition.
	for _, tr := range res.Transitions {
		if tr.To == StateRecovering {
			t.Error("policy denial must not enter recovering")
		}
	}
}

func TestRunStrategyNotRegistered(t *testing.T) {
	h := newHarness(t, nil, nil, permissivePolicy())
	res, err := h.eng.Run(context.Background(), Request{ID: "run-6", Input: "fix the bug", Targets: []string{"a.go"}})
	if err == nil || !errors.Is(err, ErrNoStrategy) {
		t.Errorf("err = %v, want ErrNoStrategy", err)
	}
	if res.State != StateFailed {
		t.Errorf("State = %s, want failed", res.State)
	}
}

func TestRunCapabilityUnmet(t *testing.T) {
	strategy := &stubStrategy{name: "patch", res: &registry.Result{Status: registry.StatusOK}}
	h := newHarness(t, strategy, nil, permissivePolicy())
	// Drop the tool_use provider so the required capability is unmet.
	empty := registry.NewCapabilityRegistry()
	if err := empty.Register(registry.CapabilityCoding, "anthropic"); err != nil {
		t.Fatal(err)
	}
	h.eng.capabilities = empty

	res, err := h.eng.Run(context.Background(), Request{ID: "run-7", Input: "fix the bug", Targets: []string{"a.go"}})
	if err == nil || !errors.Is(err, ErrCapabilityUnmet) {
		t.Errorf("err = %v, want ErrCapabilityUnmet", err)
	}
	if res.State != StateFailed {
		t.Errorf("State = %s, want failed", res.State)
	}
	if len(strategy.captured()) != 0 {
		t.Error("capability-unmet run must not execute the strategy")
	}
}

func TestRunValidationFailureRecovers(t *testing.T) {
	h := newHarness(t, &stubStrategy{name: "patch", res: &registry.Result{Status: registry.StatusOK}}, nil, permissivePolicy())
	h.eng.validations.Add(&failingValidator{})

	res, err := h.eng.Run(context.Background(), Request{ID: "run-8", Input: "fix the bug", Targets: []string{"a.go"}})
	if err == nil || !errors.Is(err, ErrNoRecovery) {
		t.Errorf("err = %v, want ErrNoRecovery", err)
	}
	if res.State != StateFailed {
		t.Errorf("State = %s, want failed", res.State)
	}
	if res.Validation == nil || res.Validation.OK {
		t.Error("validation result should exist and be failing")
	}
	got := statesOf(res.Transitions)
	want := []State{StateIdle, StateReceived, StateAnalyzed, StatePlanned, StatePolicyOK, StateExecuting, StateValidating, StateRecovering, StateFailed}
	if !equalStates(got, want) {
		t.Errorf("transitions = %v, want %v", got, want)
	}
}

func TestRunConcurrent(t *testing.T) {
	strategy := &stubStrategy{name: "patch", res: &registry.Result{Status: registry.StatusOK, Tokens: 1}}
	h := newHarness(t, strategy, nil, permissivePolicy())
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = h.eng.Run(ctx, Request{ID: "run-conc", Input: "fix the bug", Targets: []string{"a.go"}})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d failed: %v", i, err)
		}
	}
	if got := len(strategy.captured()); got != 20 {
		t.Errorf("strategy executed %d times, want 20", got)
	}
}

// failingValidator always fails.
type failingValidator struct{}

func (failingValidator) Name() string { return "fail" }
func (failingValidator) Validate(_ context.Context, path string) (*registry.ValidationReport, error) {
	return &registry.ValidationReport{Name: "fail", Path: path, OK: false, Err: errors.New("nope")}, nil
}

func statesOf(transitions []Transition) []State {
	states := make([]State, 0, len(transitions)+1)
	for _, tr := range transitions {
		if len(states) == 0 {
			states = append(states, tr.From)
		}
		states = append(states, tr.To)
	}
	return states
}

func equalStates(a, b []State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
