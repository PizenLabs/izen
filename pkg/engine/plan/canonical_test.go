package plan

import (
	"testing"

	"github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/pkg/engine/intent"
)

func TestCanonicalType(t *testing.T) {
	cases := []struct {
		kind StepKind
		want task.TaskType
		ok   bool
	}{
		{StepCreate, task.TaskFileMutate, true},
		{StepModify, task.TaskFileEdit, true},
		{StepDelete, task.TaskFileMutate, true},
		{StepRun, task.TaskShellExec, true},
		{StepVerify, task.TaskVerify, true},
		{StepRead, "", false},
	}
	for _, c := range cases {
		got, ok := canonicalType(c.kind)
		if got != c.want || ok != c.ok {
			t.Errorf("canonicalType(%s) = (%q, %v), want (%q, %v)", c.kind, got, ok, c.want, c.ok)
		}
	}
}

func TestLogicalPlanCanonical(t *testing.T) {
	lp := NewLogicalPlan(mutatingGoal(t, intent.FamilyGreenfield), []Step{
		NewStep(StepCreate, "index.html", WithID("logical-0"), WithReason("greenfield")),
		NewStep(StepModify, "styles.css", WithID("logical-1"), WithReason("mutating goal")),
		NewStep(StepVerify, "verify", WithID("logical-2"), WithReason("goal requires verification")),
		NewStep(StepRead, "notes.md", WithID("logical-3"), WithReason("read-only goal")),
	}, "mutating: 3 step(s)")

	p := lp.Canonical()
	if p == nil {
		t.Fatal("expected a canonical plan")
	}
	// Read steps are excluded.
	if len(p.Tasks) != 3 {
		t.Fatalf("Tasks length = %d, want 3 (read step excluded)", len(p.Tasks))
	}
	if p.Tasks[0].Type != task.TaskFileMutate || p.Tasks[0].Target != "index.html" {
		t.Fatalf("task[0] = %+v", p.Tasks[0])
	}
	if p.Tasks[1].Type != task.TaskFileEdit || p.Tasks[1].Target != "styles.css" {
		t.Fatalf("task[1] = %+v", p.Tasks[1])
	}
	if p.Tasks[2].Type != task.TaskVerify {
		t.Fatalf("task[2] = %+v", p.Tasks[2])
	}
	// Sequential step numbers, all hardcoded and idle.
	for i, tk := range p.Tasks {
		if tk.StepNum != i+1 {
			t.Fatalf("task %d StepNum = %d, want %d", i, tk.StepNum, i+1)
		}
		if !tk.IsHardcoded {
			t.Fatalf("task %d must be hardcoded", i)
		}
		if tk.Status != task.StatusIdle {
			t.Fatalf("task %d status = %q", i, tk.Status)
		}
	}
}

func TestLogicalPlanCanonicalOrdersDependencies(t *testing.T) {
	// Dependents declared before their dependencies.
	lp := NewLogicalPlan(mutatingGoal(t, intent.FamilyGreenfield), []Step{
		NewStep(StepModify, "b.go", WithID("logical-0"), WithDependencies("logical-1"), WithReason("dep on a")),
		NewStep(StepCreate, "a.go", WithID("logical-1"), WithReason("base")),
	}, "2 step(s)")

	p := lp.Canonical()
	if len(p.Tasks) != 2 {
		t.Fatalf("Tasks = %d, want 2", len(p.Tasks))
	}
	// a.go (dependency) must appear before b.go.
	if p.Tasks[0].Target != "a.go" || p.Tasks[1].Target != "b.go" {
		t.Fatalf("dependency order not respected: %+v", p.Tasks)
	}
}

func TestExecutablePlanCanonical(t *testing.T) {
	lp := NewLogicalPlan(mutatingGoal(t, intent.FamilyGreenfield), []Step{
		NewStep(StepCreate, "index.html", WithID("logical-0"), WithReason("greenfield")),
		NewStep(StepRun, "npm install", WithID("logical-1"), WithReason("setup")),
	}, "2 step(s)")
	np, err := NewPlanNormalizer().Normalize(lp)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	vp, err := NewPlanValidator().Validate(np)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	ep, err := NewPlanLowerer(".").Lower(vp)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	p := ep.Canonical()
	if p == nil || len(p.Tasks) == 0 {
		t.Fatal("expected canonical executable plan")
	}
	foundRun := false
	for _, tk := range p.Tasks {
		if tk.Type == task.TaskShellExec && tk.Target != "" {
			foundRun = true
		}
	}
	if !foundRun {
		t.Fatalf("expected a SHELL_EXEC task in executable projection: %+v", p.Tasks)
	}
}
