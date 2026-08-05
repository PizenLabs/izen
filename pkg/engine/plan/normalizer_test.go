package plan

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/intent"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

func logicalWithSteps(t *testing.T, steps ...Step) *LogicalPlan {
	t.Helper()
	g, err := strategy.NewGoal(intent.Must(intent.FamilyFeature), strategy.WithOutcome("x"))
	if err != nil {
		t.Fatal(err)
	}
	return NewLogicalPlan(g, steps, "test plan")
}

func TestNormalizerDeduplicates(t *testing.T) {
	steps := []Step{
		NewStep(StepModify, "a.go", WithID("logical-0")),
		NewStep(StepModify, "a.go", WithID("logical-1")), // duplicate content
		NewStep(StepCreate, "b.go", WithID("logical-2")),
	}
	np, err := NewPlanNormalizer().Normalize(logicalWithSteps(t, steps...))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := np.StepCount(); got != 2 {
		t.Fatalf("steps = %d, want 2", got)
	}
	if np.Metrics().Deduped != 1 || np.Metrics().InputCount != 3 {
		t.Fatalf("metrics = %+v", np.Metrics())
	}
}

func TestNormalizerStandardizesActions(t *testing.T) {
	steps := []Step{
		NewStep(StepModify, "a.go", WithID("logical-0"), WithAction("update")),
		NewStep(StepCreate, "b.go", WithID("logical-1"), WithAction("generate")),
	}
	np, err := NewPlanNormalizer().Normalize(logicalWithSteps(t, steps...))
	if err != nil {
		t.Fatal(err)
	}
	got := np.Steps()
	if got[0].Action() != "modify" {
		t.Fatalf("action = %q, want modify", got[0].Action())
	}
	if got[1].Action() != "create" {
		t.Fatalf("action = %q, want create", got[1].Action())
	}
	if np.Metrics().Standardized != 2 {
		t.Fatalf("standardized = %d, want 2", np.Metrics().Standardized)
	}
}

func TestNormalizerOrdersByDependencies(t *testing.T) {
	steps := []Step{
		NewStep(StepModify, "b.go", WithID("logical-1"), WithDependencies("logical-0")),
		NewStep(StepCreate, "a.go", WithID("logical-0")),
	}
	np, err := NewPlanNormalizer().Normalize(logicalWithSteps(t, steps...))
	if err != nil {
		t.Fatal(err)
	}
	got := np.Steps()
	if got[0].ID() != "s1" || got[0].Target() != "a.go" {
		t.Fatalf("first step = %+v, want a.go renumbered s1", got[0])
	}
	if got[1].Target() != "b.go" {
		t.Fatalf("second step = %+v, want b.go", got[1])
	}
	if len(got[1].DependsOn()) != 1 || got[1].DependsOn()[0] != "s1" {
		t.Fatalf("dep remap failed: %v", got[1].DependsOn())
	}
	if !np.Metrics().Reordered {
		t.Fatal("metrics should report reorder")
	}
}

func TestNormalizerCycleError(t *testing.T) {
	steps := []Step{
		NewStep(StepModify, "a.go", WithID("logical-0"), WithDependencies("logical-1")),
		NewStep(StepModify, "b.go", WithID("logical-1"), WithDependencies("logical-0")),
	}
	if _, err := NewPlanNormalizer().Normalize(logicalWithSteps(t, steps...)); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestNormalizerRejectsEmptyAndNil(t *testing.T) {
	if _, err := NewPlanNormalizer().Normalize(nil); err == nil {
		t.Fatal("expected nil error")
	}
	if _, err := NewPlanNormalizer().Normalize(logicalWithSteps(t)); err == nil {
		t.Fatal("expected empty plan error")
	}
}

func TestNormalizerDoesNotMutateInput(t *testing.T) {
	steps := []Step{
		NewStep(StepModify, "a.go", WithID("logical-0"), WithAction("update")),
		NewStep(StepModify, "a.go", WithID("logical-1")),
	}
	in := logicalWithSteps(t, steps...)
	before := in.Steps()
	_, err := NewPlanNormalizer().Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	after := in.Steps()
	if !sameStepOrder(before, after) {
		t.Fatal("normalizer mutated its input")
	}
	// The original action verb must be untouched.
	if after[0].Action() != "update" {
		t.Fatalf("input action changed to %q", after[0].Action())
	}
}

func TestNormalizerUniqueIDsAfterDedup(t *testing.T) {
	steps := []Step{
		NewStep(StepModify, "a.go", WithID("logical-0")),
		NewStep(StepCreate, "b.go", WithID("logical-0")), // duplicate id, different content
		NewStep(StepRead, "c.go", WithID("logical-1")),
	}
	np, err := NewPlanNormalizer().Normalize(logicalWithSteps(t, steps...))
	if err != nil {
		t.Fatal(err)
	}
	got := np.Steps()
	if len(got) != 2 {
		t.Fatalf("steps = %d, want 2", len(got))
	}
	if got[0].ID() == got[1].ID() {
		t.Fatalf("duplicate ids survived: %q %q", got[0].ID(), got[1].ID())
	}
	if !strings.HasPrefix(got[0].ID(), "s") || !strings.HasPrefix(got[1].ID(), "s") {
		t.Fatalf("ids not renumbered: %q %q", got[0].ID(), got[1].ID())
	}
}
