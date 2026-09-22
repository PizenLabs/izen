package scheduler

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"golang.org/x/net/html"
)

type authorizedTestSink struct {
	path    string
	calls   int
	patches int
	err     error
}

func (s *authorizedTestSink) Apply(proposal string) (int, error) {
	s.calls++
	if s.err != nil || s.patches == 0 {
		return s.patches, s.err
	}
	if filepath.Ext(s.path) == ".html" {
		if _, err := html.Parse(strings.NewReader(proposal)); err != nil {
			return 0, err
		}
	} else if _, err := parser.ParseFile(token.NewFileSet(), s.path, proposal, parser.AllErrors); err != nil {
		return 0, err
	}
	if err := os.WriteFile(s.path, []byte(proposal), 0600); err != nil {
		return 0, err
	}
	return s.patches, nil
}

func ptr[T any](v T) *T {
	return &v
}

func recoverySpec(t *testing.T) (TaskSpec, *durable.TaskState, *authorizedTestSink) {
	t.Helper()
	state := &durable.TaskState{
		ID: "task", Intent: "create implementation", ActiveTargetScope: []string{"main.go"},
		CurrentStepID: "original", Status: durable.TaskRunning,
		Cursor: &durable.ExecutionCursor{OperationID: "authorized-operation"},
	}
	spec := TaskSpec{
		Objective: "create implementation", Targets: []string{"main.go"},
		EstimatedSizes: map[string]int{"main.go": 1800}, TotalEstimatedSize: 1800,
		TaskRemainingBudget: 10000, RequestedStepBudget: 4096,
		Provider:   ptr(dprovider.DetectCapability("test", "model", 4096, 32768, false, false)),
		Operations: map[string]string{"main.go": "CREATE"}, DiskSizes: map[string]int64{"main.go": 0},
		StateFingerprints: map[string]string{"main.go": "absent"}, State: state,
	}
	return spec, state, &authorizedTestSink{path: filepath.Join(t.TempDir(), "main.go"), patches: 1}
}

func refreshDisk(t *testing.T, spec *TaskSpec, sink *authorizedTestSink) {
	t.Helper()
	data, err := os.ReadFile(sink.path)
	if err != nil {
		t.Fatal(err)
	}
	spec.DiskSizes["main.go"] = int64(len(data))
	spec.StateFingerprints["main.go"] = fmt.Sprintf("%x", sha256.Sum256(data))
	spec.TargetASTs = map[string]string{"main.go": string(data)}
}

func streamWorker(payload, finish string, observed int) StepWorker {
	return func(_ context.Context, _ ExecutionStep, _ ContextSlice, buffer *executor.ProposalStagingBuffer) (StreamResult, error) {
		_, err := buffer.Write([]byte(payload))
		return StreamResult{FinishReason: finish, ObservedTokens: observed}, err
	}
}

func TestScheduler_ProvenancePrecedence(t *testing.T) {
	spec, state, sink := recoverySpec(t)
	scheduler := NewStepScheduler()
	spec.Provider.RecordOutputLimit(dprovider.OutputLimit{Value: 256, Source: dprovider.LimitPolicy})
	if got := scheduler.EffectiveBudget(spec); got != 4096 {
		t.Fatalf("advertised precedence: %d", got)
	}
	first, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc broken(", "length", 980), evidence.VerdictPassed, sink)
	if err != nil || first.Outcome != StepOutcomePartial || first.Patches != 0 || sink.calls != 0 {
		t.Fatalf("first attempt: %+v, %v, calls=%d", first, err, sink.calls)
	}
	if _, err := os.Stat(sink.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial touched disk: %v", err)
	}
	spec.Provider.RecordOutputLimit(dprovider.OutputLimit{Value: 8192, Source: dprovider.LimitAdvertised})
	if got := scheduler.EffectiveBudget(spec); got != 980 {
		t.Fatalf("observed precedence through metadata copy: %d", got)
	}
	if state.RecoveryContext.LastCleanByteOffset != len("package main\n") || state.RecoveryContext.LastCleanLine != 1 {
		t.Fatalf("missing staging recovery offsets: %+v", state.RecoveryContext)
	}
	if state.RecoveryContext.ObservedTokens != 980 {
		t.Fatalf("recovery observation: %+v", state.RecoveryContext)
	}
	spec.RequestedStepBudget = 500
	if got := scheduler.EffectiveBudget(spec); got != 500 {
		t.Fatalf("request cap: %d", got)
	}
	spec.TaskRemainingBudget = 300
	spec.ReasoningMargin = 25
	if got := scheduler.EffectiveBudget(spec); got != 275 {
		t.Fatalf("remaining budget and margin: %d", got)
	}
}

func TestRecovery_StrategyMutationOnTruncation(t *testing.T) {
	spec, state, sink := recoverySpec(t)
	scheduler := NewStepScheduler()
	original := *state
	first, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc unfinished(", "length", 980), evidence.VerdictPassed, sink)
	if err != nil || first.Step.Strategy != DIRECT_CREATE || first.Outcome != StepOutcomePartial || first.Reason != OutputCeilingReason {
		t.Fatalf("direct: %+v, %v", first, err)
	}
	if sink.calls != 0 || state.RecoveryContext.Phase != durable.RecoveryRequired {
		t.Fatalf("partial escaped isolation: %+v", state)
	}
	if _, err := os.Stat(sink.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file: %v", err)
	}
	second, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc main() {}\n", "stop", 12), evidence.VerdictPassed, sink)
	if err != nil || second.Step.Strategy != SKELETON_CREATE || second.Step.StepBudget != 199 || second.Patches != 1 || !second.NeedsContinuation {
		t.Fatalf("skeleton: %+v, %v", second, err)
	}
	if state.RecoveryContext.Phase != durable.BaselineEstablished {
		t.Fatalf("baseline not committed: %+v", state.RecoveryContext)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), sink.path, nil, parser.AllErrors); err != nil {
		t.Fatalf("invalid baseline: %v", err)
	}
	refreshDisk(t, &spec, sink)
	third, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc main() {}\nfunc value() int { return 1 }\n", "stop", 25), evidence.VerdictPassed, sink)
	if err != nil || third.Step.Strategy != BOUNDED_EXPANSION || third.Step.StepBudget > 980 || third.Patches != 1 {
		t.Fatalf("expansion: %+v, %v", third, err)
	}
	actual := *state
	actual.RecoveryContext = original.RecoveryContext
	if !reflect.DeepEqual(original, actual) || !reflect.DeepEqual(spec.Targets, original.ActiveTargetScope) {
		t.Fatalf("durable state or scope changed: %+v", actual)
	}
}

func TestRecovery_RepeatedCeilingBlocks(t *testing.T) {
	spec, state, sink := recoverySpec(t)
	scheduler := NewStepScheduler()
	// First OUTPUT_CEILING with zero mutations consumes the single bounded
	// recovery turn (Zero-Delta Recovery Limit Invariant: max 1 allowed).
	first, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc broken(", "length", 980), evidence.VerdictPassed, sink)
	if err != nil || first.Step.Strategy != DIRECT_CREATE || first.Outcome != StepOutcomePartial {
		t.Fatalf("attempt DIRECT_CREATE: %+v, %v", first, err)
	}
	if state.RecoveryContext.ConsecutiveZeroDeltas != 1 {
		t.Fatalf("zero-delta counter = %d, want 1", state.RecoveryContext.ConsecutiveZeroDeltas)
	}
	// Second consecutive zero-delta OUTPUT_CEILING halts the continuation
	// loop immediately with ErrRecoveryHalted (bounded recovery exhausted).
	second, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc broken(", "length", 980), evidence.VerdictPassed, sink)
	if !errors.Is(err, ErrRecoveryHalted) || second.Outcome != StepOutcomePartial || second.Patches != 0 {
		t.Fatalf("attempt SKELETON_CREATE: %+v, %v", second, err)
	}
	if state.RecoveryContext.ConsecutiveZeroDeltas != 2 {
		t.Fatalf("zero-delta counter = %d, want 2", state.RecoveryContext.ConsecutiveZeroDeltas)
	}
	if second.NeedsContinuation {
		t.Fatalf("halted step must not request continuation: %+v", second)
	}
	called := false
	worker := func(context.Context, ExecutionStep, ContextSlice, *executor.ProposalStagingBuffer) (StreamResult, error) {
		called = true
		return StreamResult{}, nil
	}
	_, err = scheduler.RunNext(t.Context(), spec, state, worker, evidence.VerdictPassed, sink)
	var noProgress *NoProgressError
	if !errors.As(err, &noProgress) || called || sink.calls != 0 || noProgress.Strategy != SKELETON_CREATE {
		t.Fatalf("repeat not blocked: %v, worker=%v, calls=%d", err, called, sink.calls)
	}
}

// TestRecovery_ZeroDeltaHaltAndReset pins the Zero-Delta Recovery Limit
// Invariant directly against PostStepEvaluation: consecutive OUTPUT_CEILING
// zero-mutation partials halt at the threshold, a nonzero commit resets the
// counter, and non-ceiling partials never increment it.
func TestRecovery_ZeroDeltaHaltAndReset(t *testing.T) {
	spec, state, sink := recoverySpec(t)
	scheduler := NewStepScheduler()
	// Two truncated responses with zero file mutations: at most one bounded
	// recovery turn before gracefully halting with ErrRecoveryHalted.
	if _, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc broken(", "length", 980), evidence.VerdictPassed, sink); err != nil {
		t.Fatalf("first zero-delta: %v", err)
	}
	if _, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\nfunc broken(", "length", 980), evidence.VerdictPassed, sink); !errors.Is(err, ErrRecoveryHalted) {
		t.Fatalf("second zero-delta must halt: %v", err)
	}
	// A successful nonzero workspace commit resets the counter.
	state.RecoveryContext.ConsecutiveZeroDeltas = 1
	if err := PostStepEvaluation(state, StepOutcomeComplete, "", 1); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if state.RecoveryContext.ConsecutiveZeroDeltas != 0 {
		t.Fatalf("counter = %d, want 0 after nonzero commit", state.RecoveryContext.ConsecutiveZeroDeltas)
	}
	// Non-ceiling partials (e.g. REDUNDANT_SYMBOL) never increment.
	if err := PostStepEvaluation(state, StepOutcomePartial, "REDUNDANT_SYMBOL", 0); err != nil {
		t.Fatalf("non-ceiling: %v", err)
	}
	if state.RecoveryContext.ConsecutiveZeroDeltas != 0 {
		t.Fatalf("counter = %d, want 0 for non-ceiling partial", state.RecoveryContext.ConsecutiveZeroDeltas)
	}
	// Nil state is a safe no-op.
	if err := PostStepEvaluation(nil, StepOutcomePartial, OutputCeilingReason, 0); err != nil {
		t.Fatalf("nil state: %v", err)
	}
}

func TestRecovery_BaselineRequiresNonzeroVerifiedCommit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict evidence.EvidenceState
		patches int
		payload string
		sinkErr error
	}{
		{name: "failed evidence", verdict: evidence.VerdictFailed, patches: 1, payload: "package main\n"},
		{name: "inconclusive evidence", verdict: evidence.VerdictInconclusive, patches: 1, payload: "package main\n"},
		{name: "zero commit", verdict: evidence.VerdictPassed, payload: "package main\n"},
		{name: "empty proposal", verdict: evidence.VerdictPassed, patches: 1},
		{name: "sink failure", verdict: evidence.VerdictPassed, patches: 1, payload: "package main\n", sinkErr: errors.New("denied")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, state, sink := recoverySpec(t)
			scheduler := NewStepScheduler()
			_, err := scheduler.RunNext(t.Context(), spec, state, streamWorker("package main\n", "length", 980), evidence.VerdictPassed, sink)
			if err != nil {
				t.Fatal(err)
			}
			sink.patches, sink.err = tc.patches, tc.sinkErr
			result, err := scheduler.RunNext(t.Context(), spec, state, streamWorker(tc.payload, "stop", 10), tc.verdict, sink)
			if !errors.Is(err, tc.sinkErr) || result.Patches != 0 || state.RecoveryContext.Phase != durable.RecoveryRequired {
				t.Fatalf("invalid baseline transition: %+v, %+v, %v", result, state.RecoveryContext, err)
			}
			if _, err := os.Stat(sink.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected disk mutation: %v", err)
			}
		})
	}
}

func TestScheduler_TargetStrategiesRemainSeparate(t *testing.T) {
	spec, state, _ := recoverySpec(t)
	spec.Targets = []string{"main.go", "other.go", "existing.go"}
	spec.Operations["other.go"], spec.Operations["existing.go"] = "CREATE", "MODIFY"
	spec.DiskSizes["other.go"], spec.DiskSizes["existing.go"] = 0, 50
	spec.StateFingerprints["other.go"], spec.StateFingerprints["existing.go"] = "other-absent", "existing"
	state.RecoveryContext = durable.RecoveryContext{
		Target: "main.go", Phase: durable.RecoveryRequired, Strategy: string(DIRECT_CREATE),
		Reason: OutputCeilingReason, StateFingerprint: "absent",
	}
	steps := NewStepScheduler().Schedule(spec)
	want := []StepStrategy{SKELETON_CREATE, DIRECT_CREATE, BOUNDED_PATCH}
	if len(steps) != len(want) {
		t.Fatalf("mixed targets: %+v", steps)
	}
	for i, step := range steps {
		if len(step.Targets) != 1 || step.Targets[0] != spec.Targets[i] || step.Strategy != want[i] {
			t.Fatalf("step %d: %+v", i, step)
		}
	}
}

func TestScheduler_AcceptStepFreezesEveryField(t *testing.T) {
	step := ExecutionStep{
		ID: "step", Type: StepTypeMutation, Targets: []string{"main.go"},
		EstimatedMutationSize: 100, StepBudget: 199, Sequence: 1, TotalSteps: 2,
		Strategy: SKELETON_CREATE, Operation: "CREATE", StateFingerprint: "absent",
	}
	for i := 0; i < reflect.TypeOf(step).NumField(); i++ {
		changed := step
		value := reflect.ValueOf(&changed).Elem().Field(i)
		switch value.Kind() {
		case reflect.String:
			value.SetString("changed")
		case reflect.Int:
			value.SetInt(value.Int() + 1)
		case reflect.Slice:
			value.Set(reflect.ValueOf([]string{"intruder.go"}))
		default:
			t.Fatalf("untested field kind: %v", value.Kind())
		}
		if err := AcceptStep(step, changed); err == nil {
			t.Fatalf("accepted changed %s", reflect.TypeOf(step).Field(i).Name)
		}
	}
}

func TestPlanner_SkeletonBudgetAndInstructions(t *testing.T) {
	spec, _, _ := recoverySpec(t)
	spec.RequestedStepBudget = 100
	step := NewStepScheduler().Schedule(spec)[0]
	if step.Strategy != SKELETON_CREATE || step.StepBudget != 100 {
		t.Fatalf("skeleton exceeded current budget: %+v", step)
	}
	slice := NewContextPlanner(3).Assemble(spec.Objective, TaskStateSnapshot{}, step, "", nil)
	prompt := slice.RenderPrompt()
	for _, text := range []string{MutationSystemProtocol, "minimal valid AST skeleton", "below 200 tokens", "bounded next expansions", "STEP_BUDGET: 100"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q: %s", text, prompt)
		}
	}
}

func TestResolveStrategy_NoRepeatedTriplet(t *testing.T) {
	for _, tc := range []struct {
		operation string
		disk      int64
		prior     StepStrategy
		want      StepStrategy
	}{
		{"CREATE", 0, DIRECT_CREATE, SKELETON_CREATE},
		{"CREATE", 0, SKELETON_CREATE, ""},
		{"CREATE", 50, BOUNDED_EXPANSION, ""},
		{"MODIFY", 50, BOUNDED_PATCH, ""},
	} {
		t.Run(string(tc.prior), func(t *testing.T) {
			task := Task{
				Operation: tc.operation, EstimatedWork: 10, StateFingerprint: "unchanged",
				RecoveryContext: durable.RecoveryContext{
					Phase: durable.RecoveryRequired, Strategy: string(tc.prior),
					StateFingerprint: "unchanged", Reason: OutputCeilingReason,
				},
			}
			if got := ResolveStrategy(task, 980, tc.disk, []StepOutcome{StepOutcomePartial}); got != tc.want {
				t.Fatalf("strategy=%s, want %s", got, tc.want)
			}
			task.StateFingerprint = "changed"
			if got := ResolveStrategy(task, 980, tc.disk, nil); got == "" {
				t.Fatal("changed state incorrectly blocked")
			}
		})
	}
}

func TestRecovery_CompleteOversizedSkeletonRejected(t *testing.T) {
	spec, state, sink := recoverySpec(t)
	scheduler := NewStepScheduler()
	spec.RequestedStepBudget = 199
	worker := func(_ context.Context, _ ExecutionStep, _ ContextSlice, buffer *executor.ProposalStagingBuffer) (StreamResult, error) {
		buffer.AppendString("package main\n" + strings.Repeat("var x int\n", 100))
		return StreamResult{FinishReason: "stop"}, nil
	}
	result, err := scheduler.RunNext(t.Context(), spec, state, worker, evidence.VerdictPassed, sink)
	if err != nil || result.Outcome != StepOutcomePartial || sink.calls != 0 || state.RecoveryContext.Phase != durable.RecoveryRequired {
		t.Fatalf("oversized skeleton committed: %+v, %v", result, err)
	}
}

func TestRecovery_ProactiveCeilingAndCancellation(t *testing.T) {
	for _, proactive := range []bool{true, false} {
		t.Run(fmt.Sprint(proactive), func(t *testing.T) {
			spec, state, sink := recoverySpec(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			worker := func(_ context.Context, _ ExecutionStep, _ ContextSlice, buffer *executor.ProposalStagingBuffer) (StreamResult, error) {
				if proactive {
					_, _ = buffer.Write([]byte(strings.Repeat("x", 20000)))
				} else {
					buffer.AppendString("package main\n")
					cancel()
				}
				return StreamResult{FinishReason: "stop"}, nil
			}
			result, err := NewStepScheduler().RunNext(ctx, spec, state, worker, evidence.VerdictPassed, sink)
			if proactive {
				if err != nil || result.Outcome != StepOutcomePartial || result.Reason != OutputCeilingReason || state.RecoveryContext.Strategy != string(DIRECT_CREATE) {
					t.Fatalf("proactive: %+v, %v", result, err)
				}
			} else if !errors.Is(err, context.Canceled) || result.Outcome != StepOutcomeFailed || state.RecoveryContext.Phase != "" {
				t.Fatalf("cancellation: %+v, %v", result, err)
			}
			if sink.calls != 0 {
				t.Fatal("canceled stream reached sink")
			}
		})
	}
}
