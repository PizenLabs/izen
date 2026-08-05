package plan

import (
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/intent"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

func normalizedFrom(t *testing.T, steps ...Step) *NormalizedPlan {
	t.Helper()
	g, err := strategy.NewGoal(intent.Must(intent.FamilyFeature), strategy.WithOutcome("x"))
	if err != nil {
		t.Fatal(err)
	}
	return NewNormalizedPlan(g, steps, NormalizeMetrics{InputCount: len(steps)})
}

func TestValidatorAcceptsWellFormedPlan(t *testing.T) {
	steps := []Step{
		NewStep(StepCreate, "a.go", WithID("s1")),
		NewStep(StepModify, "b.go", WithID("s2"), WithDependencies("s1")),
	}
	vp, err := NewPlanValidator().Validate(normalizedFrom(t, steps...))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !vp.Valid() {
		t.Fatalf("plan should be valid, failed: %v", vp.FailedResults())
	}
	if vp.StepCount() != 2 {
		t.Fatalf("step count = %d", vp.StepCount())
	}
}

func TestValidatorRejectsBadKind(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepKind("explode"), "a.go", WithID("s1")),
	))
	if vp.Valid() {
		t.Fatal("invalid kind should fail validation")
	}
	if !hasRule(vp, "schema:kind") {
		t.Fatal("expected schema:kind violation")
	}
}

func TestValidatorRejectsEmptyTarget(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepModify, "", WithID("s1")),
	))
	if vp.Valid() {
		t.Fatal("empty target should fail")
	}
	if !hasRule(vp, "schema:target") {
		t.Fatal("expected schema:target violation")
	}
}

func TestValidatorRejectsEmptyPlan(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t))
	if vp.Valid() {
		t.Fatal("empty plan should fail")
	}
	if !hasRule(vp, "schema:nonempty") {
		t.Fatal("expected schema:nonempty violation")
	}
}

func TestValidatorRejectsDuplicateSteps(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepModify, "a.go", WithID("s1")),
		NewStep(StepModify, "a.go", WithID("s2")),
	))
	if vp.Valid() {
		t.Fatal("duplicate steps should fail")
	}
	if !hasRule(vp, "logic:duplicate") {
		t.Fatal("expected logic:duplicate violation")
	}
}

func TestValidatorRejectsDanglingDependency(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepCreate, "a.go", WithID("s1"), WithDependencies("ghost")),
	))
	if vp.Valid() {
		t.Fatal("dangling dependency should fail")
	}
	if !hasRule(vp, "logic:dangling_dependency") {
		t.Fatal("expected logic:dangling_dependency violation")
	}
}

func TestValidatorRejectsSelfDependency(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepCreate, "a.go", WithID("s1"), WithDependencies("s1")),
	))
	if vp.Valid() {
		t.Fatal("self dependency should fail")
	}
	if !hasRule(vp, "logic:self_dependency") {
		t.Fatal("expected logic:self_dependency violation")
	}
}

func TestValidatorRejectsCycle(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepModify, "a.go", WithID("s1"), WithDependencies("s2")),
		NewStep(StepModify, "b.go", WithID("s2"), WithDependencies("s1")),
	))
	if vp.Valid() {
		t.Fatal("cycle should fail")
	}
	if !hasRule(vp, "logic:acyclic") {
		t.Fatal("expected logic:acyclic violation")
	}
}

func TestValidatorNilInput(t *testing.T) {
	if _, err := NewPlanValidator().Validate(nil); err == nil {
		t.Fatal("expected nil input error")
	}
}

func hasRule(vp *ValidatedPlan, rule string) bool {
	for _, r := range vp.Results() {
		if r.Rule == rule {
			return true
		}
	}
	return false
}
