package pipeline

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
)

// TestFacadeInterfaceImplementedByEngine proves the concrete Engine satisfies
// the Facade contract that Mode UseCases depend on.
func TestFacadeInterfaceImplementedByEngine(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	var _ Facade = eng
	if eng == nil {
		t.Fatal("engine is nil")
	}
}

// TestFacadeExecutePlanDelegatesToFullPipeline proves a Mode UseCase executing
// through the Facade receives the full Layer 0-5 result: knowledge, capability
// graph, governed context, routed patches and validation.
func TestFacadeExecutePlanDelegatesToFullPipeline(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	eng = NewEngine(eng.Root(), eng.Sor().Engine(),
		WithRouter(NewRouter(WithModel(IntentExecution, "facade-model"))),
		WithClient(patchCompletion(t, "facade-model")),
	)

	var f Facade = eng
	res, err := f.ExecutePlan(context.Background(), Request{
		Mode:        "build",
		Intent:      layer3.IntentNewFeature,
		TargetFile:  "svc/service.go",
		Description: "add Helper function",
	})
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	if res.Knowledge == nil || res.Capabilities == nil || res.Context == nil {
		t.Fatal("ExecutePlan returned an incomplete Layer 0-2 result")
	}
	if len(res.Patches) == 0 {
		t.Fatal("ExecutePlan produced no patches")
	}
	if res.Validation == nil || !res.Validation.OK {
		t.Fatalf("ExecutePlan validation not OK: %+v", res.Validation)
	}
}

// TestFacadeValidatePatchProjectsValidationResult proves the Facade validation
// entry point returns the narrow, mode-facing ValidationResult.
func TestFacadeValidatePatchProjectsValidationResult(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	var f Facade = eng

	vr, err := f.ValidatePatch(context.Background(), []layer3.FilePatch{{
		Path:    "svc/service.go",
		New:     serviceWithHelper,
		Old:     goPipelineFixture()["svc/service.go"],
		Changed: true,
	}})
	if err != nil {
		t.Fatalf("ValidatePatch: %v", err)
	}
	if vr == nil {
		t.Fatal("ValidatePatch returned a nil result")
	}
	if !vr.OK {
		t.Errorf("validation not OK: %+v", vr)
	}
	if len(vr.Order) == 0 {
		t.Error("validation result carries no execution order")
	}
	if vr.Duration <= 0 {
		t.Errorf("validation duration = %v, want > 0", vr.Duration)
	}
}

// TestFacadeValidatePatchRejectsBrokenPatches proves failed validation surfaces
// as a non-OK result on the Facade boundary (the mode-facing shape).
func TestFacadeValidatePatchRejectsBrokenPatches(t *testing.T) {
	eng, _, _ := indexedEngine(t, goPipelineFixture())
	var f Facade = eng

	vr, err := f.ValidatePatch(context.Background(), []layer3.FilePatch{{
		Path:    "svc/service.go",
		New:     "package svc\n\nfunc {", // syntactically broken
		Old:     goPipelineFixture()["svc/service.go"],
		Changed: true,
	}})
	if err != nil {
		t.Fatalf("ValidatePatch: %v", err)
	}
	if vr.OK {
		t.Error("broken patch passed validation")
	}
	if len(vr.Failed) == 0 && len(vr.Skipped) == 0 {
		t.Errorf("broken patch produced neither failed nor skipped nodes: %+v", vr)
	}
}
