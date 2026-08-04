package layer3

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

type fakeExecProvider struct{}

func (fakeExecProvider) Provide(_ context.Context, _ Request) (*layer2.ExecutionContext, error) {
	return minimalExec(), nil
}

func newTestPipeline(t *testing.T, worker Worker) (*Pipeline, string) {
	t.Helper()
	sor := newTestSor(t, goFixture())
	ast := NewASTRewriteHandler(sor)
	guard := NewPolicyGuard(nil)
	p := NewPipeline(guard, ast,
		WithRoot(sor.Root()),
		WithWorker(worker),
		WithExecutionContextProvider(fakeExecProvider{}),
	)
	return p, sor.Root()
}

func renamePipelineReq() Request {
	return Request{Intent: IntentRename, TargetSymbol: "Compute", NewName: "ComputeAll"}
}

func generativeReq() Request {
	return Request{Intent: IntentNewFeature, Description: "add health endpoint"}
}

func TestPipelineStagesFor(t *testing.T) {
	p, _ := newTestPipeline(t, nil)

	simple := p.StagesFor(renamePipelineReq())
	if want := []Stage{StageUnderstand, StageExecute, StageValidate}; !equalStages(simple, want) {
		t.Errorf("simple stages = %v, want %v", simple, want)
	}

	gen := p.StagesFor(generativeReq())
	if want := []Stage{StageUnderstand, StagePlan, StageExecute, StageReview, StageValidate}; !equalStages(gen, want) {
		t.Errorf("generative stages = %v, want %v", gen, want)
	}
}

func equalStages(a, b []Stage) bool {
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

func TestPipelineExecuteRenameEndToEnd(t *testing.T) {
	p, _ := newTestPipeline(t, nil)
	run, err := p.Execute(context.Background(), renamePipelineReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	st := run.State()
	if st.State != StateDone {
		t.Fatalf("state = %v, err = %v", st.State, st.Err)
	}
	if st.Route != RouteASTRewrite {
		t.Errorf("route = %v", st.Route)
	}
	if st.Result == nil || len(st.Result.Patches) != 2 {
		t.Errorf("patches = %+v", st.Result)
	}
	if !st.Result.Validated {
		t.Error("expected structural validation to pass")
	}
	if len(st.Events) == 0 {
		t.Fatal("no events recorded")
	}
	if got := st.Events[len(st.Events)-1]; got.Stage != StageValidate || got.State != StateDone {
		t.Errorf("last event = %+v", got)
	}
	if st.Current != len(st.Stages)-1 {
		t.Errorf("current = %d, want %d", st.Current, len(st.Stages)-1)
	}
}

func TestPipelineGenerativeSuccess(t *testing.T) {
	worker := FuncWorker(func(_ context.Context, exec *layer2.ExecutionContext, req Request) (*WorkerResult, error) {
		if exec == nil || len(exec.Files) == 0 {
			return nil, errors.New("expected execution context")
		}
		return &WorkerResult{
			Reason:  req.Description,
			Tokens:  TokenUsage{Input: 10, Output: 5},
			Patches: []FilePatch{{Path: "svc/feature.go", Language: "go", New: "package svc\n\nfunc NewFeature() int { return 1 }\n", Changed: true}},
		}, nil
	})
	p, _ := newTestPipeline(t, worker)
	run, err := p.Execute(context.Background(), generativeReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	st := run.State()
	if st.State != StateDone {
		t.Fatalf("state = %v, err = %v", st.State, st.Err)
	}
	if st.Result == nil || len(st.Result.Patches) != 1 {
		t.Fatalf("patches = %+v", st.Result)
	}
	if !st.Result.Validated {
		t.Error("expected validation to pass")
	}
	if st.Result.Tokens.Total() != 15 {
		t.Errorf("tokens = %+v", st.Result.Tokens)
	}
	for _, want := range []Stage{StageUnderstand, StagePlan, StageExecute, StageReview, StageValidate} {
		found := false
		for _, ev := range st.Events {
			if ev.Stage == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("stage %s not present in events", want)
		}
	}
}

func TestPipelineGenerativeValidationFailure(t *testing.T) {
	worker := FuncWorker(func(_ context.Context, _ *layer2.ExecutionContext, _ Request) (*WorkerResult, error) {
		return &WorkerResult{
			Patches: []FilePatch{{Path: "svc/feature.go", Language: "go", New: "package svc\n\nfunc NewFeature() int { return 1 }\n", Changed: true}},
		}, nil
	})
	sor := newTestSor(t, goFixture())
	guard := NewPolicyGuard(nil)
	p := NewPipeline(guard, NewASTRewriteHandler(sor),
		WithWorker(worker),
		WithExecutionContextProvider(fakeExecProvider{}),
		WithValidator(failValidator{}),
	)
	_, err := p.Execute(context.Background(), generativeReq())
	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("err = %v, want ErrValidationFailed", err)
	}
	if guard.ValidationMode() != ValidationStructural {
		t.Fatal("empty caps must yield structural validation")
	}
}

type failValidator struct{}

func (failValidator) Validate(_ context.Context, _ []FilePatch) (*ValidationResult, error) {
	return &ValidationResult{OK: false, Output: "policy veto"}, nil
}

func TestPipelineWorkerEmptyPatchFailsReview(t *testing.T) {
	worker := FuncWorker(func(_ context.Context, _ *layer2.ExecutionContext, _ Request) (*WorkerResult, error) {
		return &WorkerResult{}, nil
	})
	p, _ := newTestPipeline(t, worker)
	run, err := p.Execute(context.Background(), generativeReq())
	if err == nil {
		t.Fatal("expected review failure")
	}
	if !errors.Is(err, ErrStageFailed) || !errors.Is(err, ErrEmptyPatch) {
		t.Errorf("err = %v", err)
	}
	if run.State().State != StateFailed {
		t.Errorf("state = %v", run.State().State)
	}
	if !errors.Is(run.State().Err, ErrEmptyPatch) {
		t.Errorf("state err = %v", run.State().Err)
	}
}

func TestPipelineGenerativeNoWorker(t *testing.T) {
	p, _ := newTestPipeline(t, nil)
	_, err := p.Execute(context.Background(), generativeReq())
	if !errors.Is(err, ErrNoWorker) {
		t.Errorf("err = %v, want ErrNoWorker", err)
	}
}

func TestPipelineRequestValidation(t *testing.T) {
	p, _ := newTestPipeline(t, nil)
	_, err := p.Execute(context.Background(), Request{Intent: IntentRename, TargetSymbol: "Compute"})
	if !errors.Is(err, ErrMissingNewName) {
		t.Errorf("err = %v, want ErrMissingNewName", err)
	}
	_, err = p.Execute(context.Background(), Request{Intent: Intent("bogus")})
	if !errors.Is(err, ErrInvalidIntent) {
		t.Errorf("err = %v, want ErrInvalidIntent", err)
	}
}

func TestPipelineCancellation(t *testing.T) {
	p, _ := newTestPipeline(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run, err := p.Execute(ctx, renamePipelineReq())
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if run.State().State != StateCancelled {
		t.Errorf("state = %v, want cancelled", run.State().State)
	}
}

func TestPipelineStateImmutability(t *testing.T) {
	p, _ := newTestPipeline(t, nil)
	run, err := p.Execute(context.Background(), renamePipelineReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	before := run.State()
	before.Stages[0] = "mutated"
	if before.Result != nil {
		before.Result.Patches = nil
	}
	after := run.State()
	if after.Stages[0] != StageUnderstand {
		t.Errorf("stages mutated by caller: %v", after.Stages)
	}
	if after.Result == nil || len(after.Result.Patches) != 2 {
		t.Errorf("result patches mutated by caller: %+v", after.Result)
	}
}

func TestPipelineConcurrentRuns(t *testing.T) {
	p, _ := newTestPipeline(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := p.Execute(context.Background(), renamePipelineReq())
			if err != nil {
				t.Errorf("Execute: %v", err)
				return
			}
			if st := run.State(); st.State != StateDone {
				t.Errorf("state = %v", st.State)
			}
		}()
	}
	wg.Wait()
}

func TestPipelineRunIDsUnique(t *testing.T) {
	p, _ := newTestPipeline(t, nil)
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		run, err := p.NewRun(renamePipelineReq())
		if err != nil {
			t.Fatalf("NewRun: %v", err)
		}
		id := run.State().ID
		if seen[id] {
			t.Fatalf("duplicate run id %q", id)
		}
		seen[id] = true
	}
}

func TestPipelineCommandValidation(t *testing.T) {
	guard := NewPolicyGuard(fakeCaps{})
	if guard.ValidationMode() != ValidationStructural {
		t.Fatal("empty caps must yield structural validation")
	}
	v := CommandValidator{Root: t.TempDir(), Cmd: "echo ok"}
	res, err := v.Validate(context.Background(), []FilePatch{{Path: "a.go", New: "package a\n", Changed: true}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Errorf("command failed: %s", res.Output)
	}

	fail := CommandValidator{Root: t.TempDir(), Cmd: "false"}
	res, err = fail.Validate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK {
		t.Error("expected failing command to report failure")
	}

	if _, err := (CommandValidator{}).Validate(context.Background(), nil); err != nil {
		t.Errorf("empty command err = %v", err)
	}
}

func TestStructuralValidator(t *testing.T) {
	v := StructuralValidator{Root: t.TempDir()}
	res, err := v.Validate(context.Background(), []FilePatch{{
		Path:    "svc/x.go",
		New:     "package svc\n\nfunc X() {}\n",
		Changed: true,
	}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Errorf("valid patch rejected: %s", res.Output)
	}

	res, err = v.Validate(context.Background(), []FilePatch{{Path: "svc/bad.go", New: "not valid go {{{", Changed: true}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK {
		t.Error("invalid Go accepted")
	}

	res, err = v.Validate(context.Background(), []FilePatch{{Path: "../escape.go", New: "package x", Changed: true}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK {
		t.Error("escaping path accepted")
	}
}
