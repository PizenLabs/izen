package investigate

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
// proves an investigate use case delegates its Layer 4 validation to the
// injected facade.
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

// TestInvestigateValidateTargetFileThroughFacade proves the /investigate Mode
// UseCase can execute Layer 4 validation of a candidate target through the
// injected pipeline.Facade.
func TestInvestigateValidateTargetFileThroughFacade(t *testing.T) {
	dir := t.TempDir()
	target := "broken.go"
	if err := os.WriteFile(filepath.Join(dir, target), []byte("package broken\n\nfunc Compute() { return 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	fac := &fakeFacade{vr: &pipeline.ValidationResult{OK: true}}
	e := NewEngine(dir, "probe", &mockRetriever{}, nil).WithPipelineFacade(fac)

	vr, err := e.ValidateTargetFile(context.Background(), target)
	if err != nil {
		t.Fatalf("ValidateTargetFile: %v", err)
	}
	if fac.validateCount() != 1 {
		t.Fatalf("ValidatePatch calls = %d, want exactly 1", fac.validateCount())
	}
	if vr == nil || !vr.OK {
		t.Fatalf("ValidateTargetFile result = %+v, want OK", vr)
	}
}

// TestInvestigateWithoutFacadeSkipsValidation proves the offline path is
// preserved when no facade is wired.
func TestInvestigateWithoutFacadeSkipsValidation(t *testing.T) {
	dir := t.TempDir()
	target := "a.go"
	if err := os.WriteFile(filepath.Join(dir, target), []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(dir, "probe", &mockRetriever{}, nil) // no facade

	vr, err := e.ValidateTargetFile(context.Background(), target)
	if err != nil {
		t.Fatalf("ValidateTargetFile: %v", err)
	}
	if vr != nil {
		t.Fatalf("ValidateTargetFile result = %+v, want nil without a facade", vr)
	}
}
