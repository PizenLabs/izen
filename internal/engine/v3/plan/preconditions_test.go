package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreconditionsReadyInEmptyWorkspace(t *testing.T) {
	ws := t.TempDir()
	vp := validateOf(t,
		NewStep(StepCreate, "cmd/main.go", WithID("s1")),
		NewStep(StepCreate, "go.mod", WithID("s2")),
	)
	ep, err := NewPlanLowerer(ws).Lower(vp)
	if err != nil {
		t.Fatal(err)
	}
	report, err := NewExecutionPreconditions(ws).Check(ep)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("plan should be ready in an empty workspace, failed: %v", report.Failed())
	}
}

func TestPreconditionsWarnOnMissingWorkdir(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "does-not-exist")
	vp := validateOf(t, NewStep(StepCreate, "a.go", WithID("s1")))
	ep, err := NewPlanLowerer(ws).Lower(vp)
	if err != nil {
		t.Fatal(err)
	}
	report, _ := NewExecutionPreconditions(ws).Check(ep)
	if report.Ready() {
		t.Fatal("missing working directory must block readiness")
	}
}

func TestPreconditionsRejectRootMismatch(t *testing.T) {
	ws := t.TempDir()
	other := t.TempDir()
	vp := validateOf(t, NewStep(StepCreate, "a.go", WithID("s1")))
	ep, _ := NewPlanLowerer(ws).Lower(vp)
	report, _ := NewExecutionPreconditions(other).Check(ep)
	if report.Ready() {
		t.Fatal("plan rooted elsewhere must not be ready")
	}
}

func TestPreconditionsWarnOnClobber(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "existing.go")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	vp := validateOf(t, NewStep(StepCreate, "existing.go", WithID("s1")))
	ep, _ := NewPlanLowerer(ws).Lower(vp)
	report, _ := NewExecutionPreconditions(ws).Check(ep)
	if !report.Ready() {
		t.Fatalf("clobber warning must not block readiness: %v", report.Failed())
	}
	found := false
	for _, c := range report.Checks() {
		if c.Name == "filesystem:clobber" && !c.OK && !c.Fatal {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a non-fatal clobber warning")
	}
}

func TestPreconditionsFlagMissingTool(t *testing.T) {
	ws := t.TempDir()
	vp := validateOf(t, NewStep(StepRun, "definitely-not-a-real-tool-xyz --version", WithID("s1")))
	ep, _ := NewPlanLowerer(ws).Lower(vp)
	report, _ := NewExecutionPreconditions(ws).Check(ep)
	if report.Ready() {
		t.Fatal("missing required tool must block readiness")
	}
	found := false
	for _, c := range report.Failed() {
		if c.Name == "environment:tool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected environment:tool fatal check, got %v", report.Checks())
	}
}

func TestPreconditionsParentIsFile(t *testing.T) {
	ws := t.TempDir()
	// Create a file where a directory is expected.
	if err := os.WriteFile(filepath.Join(ws, "blocked"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	vp := validateOf(t, NewStep(StepCreate, "blocked/main.go", WithID("s1")))
	ep, _ := NewPlanLowerer(ws).Lower(vp)
	report, _ := NewExecutionPreconditions(ws).Check(ep)
	if report.Ready() {
		t.Fatal("parent being a file must block readiness")
	}
}

func TestPreconditionsDoesNotMutatePlan(t *testing.T) {
	ws := t.TempDir()
	vp := validateOf(t, NewStep(StepCreate, "a.go", WithID("s1")))
	ep, _ := NewPlanLowerer(ws).Lower(vp)
	before := ep.Steps()
	if _, err := NewExecutionPreconditions(ws).Check(ep); err != nil {
		t.Fatal(err)
	}
	if len(ep.Steps()) != len(before) {
		t.Fatal("preconditions mutated the plan")
	}
}

func TestPreconditionsNilPlan(t *testing.T) {
	if _, err := NewExecutionPreconditions(t.TempDir()).Check(nil); err == nil {
		t.Fatal("expected nil plan error")
	}
}
