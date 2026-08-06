package review

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
)

// fakeFacade is a recording test double for the pipeline.Facade boundary. It
// proves a review use case delegates its Layer 4 validation to the injected
// facade.
type fakeFacade struct {
	mu       sync.Mutex
	validate int
	vr       *pipeline.ValidationResult
}

func (f *fakeFacade) ExecutePlan(_ context.Context, _ pipeline.Request) (*pipeline.Result, error) {
	return &pipeline.Result{}, nil
}

func (f *fakeFacade) ValidatePatch(_ context.Context, _ []layer3.FilePatch) (*pipeline.ValidationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validate++
	return f.vr, nil
}

func (f *fakeFacade) validateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.validate
}

// TestReviewVerifyStateExecutesThroughFacade proves the /review Mode UseCase
// verify step executes through the injected pipeline.Facade: the changed files
// are run through the Layer 4 RAM validation DAG during stateVerify.
func TestReviewVerifyStateExecutesThroughFacade(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte("package svc\n\nfunc Compute(n int) int { return n * 2 }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	fac := &fakeFacade{vr: &pipeline.ValidationResult{OK: true}}
	e := NewEngine(dir, &mockRetriever{}, nil).WithPipelineFacade(fac)
	driveToVerify(t, e)

	result := &ReviewResult{FilesChanged: []DiffFile{{Path: "service.go", Language: "go"}}}
	if err := e.stateVerify(result); err != nil {
		t.Fatalf("stateVerify: %v", err)
	}
	if fac.validateCount() != 1 {
		t.Fatalf("ValidatePatch calls = %d, want exactly 1", fac.validateCount())
	}
	if result.Ledger == nil {
		t.Fatal("stateVerify did not populate the review ledger")
	}
}

// driveToVerify advances the state machine along the canonical review path to
// the Verify state so stateVerify's terminal transition is legal.
func driveToVerify(t *testing.T, e *Engine) {
	t.Helper()
	for _, s := range []State{StateAnalyzeDiff, StateImpactRadius, StateRiskAudit, StateVerify} {
		if err := e.State.Transition(s); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
	}
}

// TestReviewWithoutFacadeSkipsValidation proves the legacy verify-only path is
// preserved when no facade is wired.
func TestReviewWithoutFacadeSkipsValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte("package svc\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(dir, &mockRetriever{}, nil) // no facade
	driveToVerify(t, e)

	result := &ReviewResult{FilesChanged: []DiffFile{{Path: "service.go", Language: "go"}}}
	if err := e.stateVerify(result); err != nil {
		t.Fatalf("stateVerify: %v", err)
	}
}
