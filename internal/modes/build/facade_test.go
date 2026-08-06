package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
)

// fakeFacade is a recording test double for the pipeline.Facade boundary. It
// proves a build use case delegates its Layer 4 validation to the injected
// facade.
type fakeFacade struct {
	mu       sync.Mutex
	validate int
	vr       *pipeline.ValidationResult
	verr     error
}

func (f *fakeFacade) ExecutePlan(_ context.Context, _ pipeline.Request) (*pipeline.Result, error) {
	return &pipeline.Result{}, nil
}

func (f *fakeFacade) ValidatePatch(_ context.Context, _ []layer3.FilePatch) (*pipeline.ValidationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validate++
	return f.vr, f.verr
}

func (f *fakeFacade) validateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.validate
}

// TestBuildExecutorAppliesMutationThroughFacade proves the /build Mode UseCase
// executes its mutation application through the injected pipeline.Facade: the
// Layer 4 validation DAG is consulted for the proposed mutation while the
// mutation is still written to disk (the outcome is advisory by contract).
func TestBuildExecutorAppliesMutationThroughFacade(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.go"), []byte("package worker\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	approve(t, e, "worker.go", 1)

	fac := &fakeFacade{vr: &pipeline.ValidationResult{OK: true}}
	ex := NewExecutor(dir, e).WithPipelineFacade(fac)

	if err := ex.ApplyMutation(t.Context(), FileMutation{
		File:    "worker.go",
		Content: "package worker\n\nfunc Join(x int, y int) string { return \"ok\" }\n",
		TaskID:  1,
	}); err != nil {
		t.Fatalf("ApplyMutation: %v", err)
	}

	if fac.validateCount() != 1 {
		t.Fatalf("ValidatePatch calls = %d, want exactly 1", fac.validateCount())
	}
	data, err := os.ReadFile(filepath.Join(dir, "worker.go"))
	if err != nil {
		t.Fatalf("read mutated file: %v", err)
	}
	if !strings.Contains(string(data), "func Join") {
		t.Errorf("mutation was not applied through the facade gate:\n%s", data)
	}
}

// TestBuildExecutorWithoutFacadeSkipsValidation proves the legacy apply-only
// path is preserved when no facade is wired: no validation, mutation applied.
func TestBuildExecutorWithoutFacadeSkipsValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	approve(t, e, "a.go", 1)

	ex := NewExecutor(dir, e) // no facade

	if err := ex.ApplyMutation(t.Context(), FileMutation{File: "a.go", Content: "package a\n\nvar X = 1\n", TaskID: 1}); err != nil {
		t.Fatalf("ApplyMutation: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "var X") {
		t.Errorf("mutation not applied:\n%s", data)
	}
}

// TestBuildExecutorFacadeValidationAdvisory proves a non-OK Layer 4 outcome
// does not block the mutation: the advisory contract holds under failure.
func TestBuildExecutorFacadeValidationAdvisory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	approve(t, e, "b.go", 1)

	fac := &fakeFacade{vr: &pipeline.ValidationResult{OK: false}}
	ex := NewExecutor(dir, e).WithPipelineFacade(fac)

	if err := ex.ApplyMutation(t.Context(), FileMutation{File: "b.go", Content: "package b\n\nvar Y = 2\n", TaskID: 1}); err != nil {
		t.Fatalf("ApplyMutation must not be blocked by an advisory validation outcome: %v", err)
	}
	if fac.validateCount() != 1 {
		t.Fatalf("ValidatePatch calls = %d, want exactly 1", fac.validateCount())
	}
}
