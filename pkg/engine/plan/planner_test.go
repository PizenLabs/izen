package plan

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/intent"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

func mutatingGoal(t *testing.T, family intent.Family, opts ...strategy.GoalOption) strategy.Goal {
	t.Helper()
	opts = append([]strategy.GoalOption{
		strategy.WithOutcome("achieve the goal"),
		strategy.WithTargets("internal/app.go"),
	}, opts...)
	g, err := strategy.NewGoal(intent.Must(family), opts...)
	if err != nil {
		t.Fatalf("NewGoal: %v", err)
	}
	return g
}

func TestPlannerDeriveMutating(t *testing.T) {
	g := mutatingGoal(t, intent.FamilyFeature,
		strategy.WithNewFiles("cmd/main.go"),
	)
	p := NewPlanner()
	lp, err := p.Derive(g)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	steps := lp.Steps()
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3 (create, modify, verify)", len(steps))
	}
	if steps[0].Kind() != StepCreate || steps[0].Target() != "cmd/main.go" {
		t.Fatalf("step 0 = %+v, want create cmd/main.go", steps[0])
	}
	if steps[1].Kind() != StepModify || steps[1].Target() != "internal/app.go" {
		t.Fatalf("step 1 = %+v, want modify internal/app.go", steps[1])
	}
	if steps[2].Kind() != StepVerify {
		t.Fatalf("step 2 = %+v, want verify (feature requires test)", steps[2])
	}
	// Sequential chaining: each step depends on the previous.
	if len(steps[2].DependsOn()) != 1 || steps[2].DependsOn()[0] != steps[1].ID() {
		t.Fatalf("verify step deps = %v, want [%s]", steps[2].DependsOn(), steps[1].ID())
	}
}

func TestPlannerDeriveReadOnly(t *testing.T) {
	g, err := strategy.NewGoal(intent.Must(intent.FamilyQuestion),
		strategy.WithOutcome("explain the routing"),
		strategy.WithTargets("internal/router.go", "pkg/http.go"),
	)
	if err != nil {
		t.Fatal(err)
	}
	lp, err := NewPlanner().Derive(g)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	steps := lp.Steps()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	for _, s := range steps {
		if s.Kind() != StepRead {
			t.Fatalf("step kind = %s, want read", s.Kind())
		}
	}
	if !strings.Contains(lp.Summary(), "read-only") {
		t.Fatalf("summary = %q", lp.Summary())
	}
}

func TestPlannerDeriveGreenfieldNoVerify(t *testing.T) {
	g, err := strategy.NewGoal(intent.Must(intent.FamilyGreenfield),
		strategy.WithOutcome("generate a service"),
		strategy.WithNewFiles("cmd/server/main.go", "go.mod"),
	)
	if err != nil {
		t.Fatal(err)
	}
	lp, err := NewPlanner().Derive(g)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	steps := lp.Steps()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2 (no verify for greenfield)", len(steps))
	}
	for _, s := range steps {
		if s.Kind() != StepCreate {
			t.Fatalf("step kind = %s, want create", s.Kind())
		}
	}
}

func TestPlannerDeriveEmptyGoal(t *testing.T) {
	g, err := strategy.NewGoal(intent.Must(intent.FamilyFeature), strategy.WithOutcome("nothing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlanner().Derive(g); err == nil {
		t.Fatal("expected error for goal with no files or targets")
	}
}

func TestPlannerRespectsMaxStepsConstraint(t *testing.T) {
	g := mutatingGoal(t, intent.FamilyFeature,
		strategy.WithNewFiles("a.go", "b.go", "c.go"),
		strategy.WithConstraint(strategy.Constraint{Kind: strategy.ConstraintMaxSteps, Value: "2"}),
	)
	if _, err := NewPlanner().Derive(g); err == nil {
		t.Fatal("expected max_steps violation error")
	}
}

func TestPlannerGoalValidation(t *testing.T) {
	if _, err := NewPlanner().Derive(strategy.Goal{}); err == nil {
		t.Fatal("expected error for zero goal")
	}
}
