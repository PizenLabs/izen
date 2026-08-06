package plan

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
)

// fakeFacade is a recording test double for the pipeline.Facade boundary. It
// lets the integration tests prove a Mode UseCase delegates its generative
// work to the injected facade instead of a direct provider.
type fakeFacade struct {
	mu       sync.Mutex
	execute  int
	validate int
	planRes  *pipeline.Result
	execErr  error
	valRes   *pipeline.ValidationResult
	lastReq  pipeline.Request
}

func (f *fakeFacade) ExecutePlan(_ context.Context, req pipeline.Request) (*pipeline.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execute++
	f.lastReq = req
	if f.execErr != nil {
		return nil, f.execErr
	}
	return f.planRes, nil
}

func (f *fakeFacade) ValidatePatch(_ context.Context, _ []layer3.FilePatch) (*pipeline.ValidationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validate++
	return f.valRes, nil
}

func (f *fakeFacade) executeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execute
}

// TestPlanEngineExecutesThroughFacade proves the /plan Mode UseCase executes
// its generative core work via the injected pipeline.Facade: when no direct
// provider is wired, ProcessFromLedger delegates to ExecutePlan and projects
// the returned file patches onto canonical plan Tasks.
func TestPlanEngineExecutesThroughFacade(t *testing.T) {
	fac := &fakeFacade{
		planRes: &pipeline.Result{
			Patches: []layer3.FilePatch{
				{Path: "svc/service.go", New: "package svc", Changed: true},
				{Path: "cmd/app/main.go", New: "package main", Changed: true},
				{Path: "README.md", New: "docs", Changed: false},
			},
		},
	}
	e := NewEngine(NewPlanStore()).WithPipelineFacade(fac)

	tasks, err := e.ProcessFromLedger(context.Background(), "", "improve error handling across the service module", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if fac.executeCount() != 1 {
		t.Fatalf("ExecutePlan calls = %d, want exactly 1", fac.executeCount())
	}
	if fac.lastReq.Mode != "plan" {
		t.Errorf("facade request mode = %q, want plan", fac.lastReq.Mode)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2 (unchanged README patch dropped)", len(tasks))
	}
	if tasks[0].Target != "svc/service.go" || tasks[0].StepNum != 1 {
		t.Errorf("task[0] = %+v, want svc/service.go step 1", tasks[0])
	}
	if tasks[1].Target != "cmd/app/main.go" || tasks[1].StepNum != 2 {
		t.Errorf("task[1] = %+v, want cmd/app/main.go step 2", tasks[1])
	}
	for _, tsk := range tasks {
		if tsk.Type.String() != "FILE_MUTATE" {
			t.Errorf("task type = %s, want FILE_MUTATE", tsk.Type)
		}
	}
}

// TestPlanEngineFacadeErrorsSurface proves a facade failure propagates as a
// wrapped, actionable error from the Mode UseCase boundary.
func TestPlanEngineFacadeErrorsSurface(t *testing.T) {
	fac := &fakeFacade{execErr: context.DeadlineExceeded}
	e := NewEngine(NewPlanStore()).WithPipelineFacade(fac)

	_, err := e.ProcessFromLedger(context.Background(), "", "improve error handling across the service module", "test-model")
	if err == nil {
		t.Fatal("expected an error when the facade execution fails")
	}
	if fac.executeCount() != 1 {
		t.Fatalf("ExecutePlan calls = %d, want exactly 1", fac.executeCount())
	}
}

// TestPlanEngineFacadeEmptyResultErrors proves a facade that produces no
// patches yields a clear error instead of an empty plan.
func TestPlanEngineFacadeEmptyResultErrors(t *testing.T) {
	fac := &fakeFacade{planRes: &pipeline.Result{}}
	e := NewEngine(NewPlanStore()).WithPipelineFacade(fac)

	_, err := e.ProcessFromLedger(context.Background(), "", "improve error handling across the service module", "test-model")
	if err == nil {
		t.Fatal("expected an error when the facade produces no patches")
	}
}

// TestPlanEngineKeepsProviderWhenWired proves the legacy provider path remains
// authoritative when both a provider and a facade are present: the facade must
// not be consulted.
func TestPlanEngineKeepsProviderWhenWired(t *testing.T) {
	fac := &fakeFacade{planRes: &pipeline.Result{Patches: []layer3.FilePatch{{Path: "x", Changed: true}}}}
	e := NewEngine(NewPlanStore()).WithPipelineFacade(fac)
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return nil, errors.New("provider must not be called for direct mutation fast-track")
	})

	_, err := e.ProcessFromLedger(context.Background(), "", "refactor LICENSE from MIT to APACHE", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if fac.executeCount() != 0 {
		t.Fatalf("ExecutePlan calls = %d, want 0 when a direct provider is wired", fac.executeCount())
	}
}
