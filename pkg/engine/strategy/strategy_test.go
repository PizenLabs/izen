package strategy

import (
	"strings"
	"testing"

	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/context"
	"github.com/PizenLabs/izen/pkg/engine/intent"
)

func buildPC(t *testing.T, prompt string) context.PlanningContext {
	t.Helper()
	c := context.NewCollector()
	c.Register(context.ProviderPrompt, context.NewPromptProvider(prompt))
	c.Register(context.ProviderEnvironment, context.NewEnvironmentProvider())
	pc, err := c.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return pc
}

func TestNewGoalValidates(t *testing.T) {
	if _, err := NewGoal(intent.Intent{}); err == nil {
		t.Fatal("expected error for zero intent")
	}
	in := intent.Must(intent.FamilyFeature)
	if _, err := NewGoal(in, WithOutcome("x")); err != nil {
		t.Fatalf("valid goal rejected: %v", err)
	}
	if _, err := NewGoal(in, WithConstraint(Constraint{Kind: "bogus", Value: "x"})); err == nil {
		t.Fatal("expected error for invalid constraint kind")
	}
}

func TestGoalImmutability(t *testing.T) {
	in := intent.Must(intent.FamilyFeature)
	g, err := NewGoal(in,
		WithOutcome("add payments"),
		WithTargets("internal/payments.go"),
		WithConstraint(Constraint{Kind: ConstraintPermittedRoot, Value: "/ws"}),
		WithCriteria("tests pass"),
	)
	if err != nil {
		t.Fatal(err)
	}
	targets := g.Targets()
	targets[0] = "tampered"
	if g.Targets()[0] != "internal/payments.go" {
		t.Fatal("Goal leaked its slice through Targets()")
	}
	crit := g.Criteria()
	crit[0] = "x"
	if g.Criteria()[0] != "tests pass" {
		t.Fatal("Goal leaked its slice through Criteria()")
	}
	cons := g.Constraints()
	cons[0].Value = "evil"
	if g.Constraints()[0].Value != "/ws" {
		t.Fatal("Goal leaked its slice through Constraints()")
	}
}

func TestGoalRequiresVerify(t *testing.T) {
	in := intent.Must(intent.FamilyFeature)
	g, _ := NewGoal(in, WithConstraint(Constraint{Kind: ConstraintRequireVerify, Value: "true"}))
	if !g.RequiresVerify() {
		t.Fatal("goal should require verify")
	}
	g2, _ := NewGoal(intent.Must(intent.FamilyQuestion))
	if g2.RequiresVerify() {
		t.Fatal("read-only goal must not require verify")
	}
}

func TestPromptStrategyDeterministic(t *testing.T) {
	in := intent.Must(intent.FamilyFeature)
	s := NewPromptStrategy()
	pc := buildPC(t, "implement a health endpoint at @internal/http.go")
	g, err := s.DetermineGoal(in, pc)
	if err != nil {
		t.Fatalf("DetermineGoal: %v", err)
	}
	if g.Outcome() == "" {
		t.Fatal("outcome must not be empty")
	}
	targets := g.Targets()
	if len(targets) != 1 || targets[0] != "internal/http.go" {
		t.Fatalf("targets = %v, want [internal/http.go]", targets)
	}
	if !g.RequiresVerify() {
		t.Fatal("feature intent should produce a require_verify goal")
	}
	if g.IsZero() {
		t.Fatal("goal is zero")
	}
}

func TestPromptStrategyWithModel(t *testing.T) {
	in := intent.Classify("Create a new project from scratch with a health endpoint")
	s := NewPromptStrategy().WithModel(NewGoalGeneratorFunc("gemma-4-26b", func(_ stdctx.Context, req GoalRequest) (GoalResult, error) {
		return GoalResult{
			Outcome:  "A new service exposing /health",
			NewFiles: []string{"cmd/server/main.go", "internal/api/health.go"},
			Criteria: []string{"/health returns 200"},
		}, nil
	}))
	pc := buildPC(t, "Create a new project from scratch with a health endpoint")
	g, err := s.DetermineGoal(in, pc)
	if err != nil {
		t.Fatalf("DetermineGoal: %v", err)
	}
	if g.Intent().Family() != intent.FamilyGreenfield {
		t.Fatalf("family = %s, want greenfield", g.Intent().Family())
	}
	if len(g.NewFiles()) != 2 {
		t.Fatalf("new files = %v", g.NewFiles())
	}
	if g.Outcome() != "A new service exposing /health" {
		t.Fatalf("outcome = %q", g.Outcome())
	}
}

func TestPromptStrategyModelError(t *testing.T) {
	s := NewPromptStrategy().WithModel(NewGoalGeneratorFunc("gemma-4-26b", func(_ stdctx.Context, _ GoalRequest) (GoalResult, error) {
		return GoalResult{}, stdctx.Canceled
	}))
	if _, err := s.DetermineGoal(intent.Must(intent.FamilyGreenfield), buildPC(t, "x")); err == nil {
		t.Fatal("expected model error to propagate")
	}
}

func TestGuidedStrategyAddsContextConstraints(t *testing.T) {
	in := intent.Must(intent.FamilyQuestion)
	s := NewGuidedStrategy("/ws", 25)
	pc := buildPC(t, "explain the routing layer")
	g, err := s.DetermineGoal(in, pc)
	if err != nil {
		t.Fatalf("DetermineGoal: %v", err)
	}
	found := map[ConstraintKind]bool{}
	for _, c := range g.Constraints() {
		found[c.Kind] = true
	}
	if !found[ConstraintPermittedRoot] {
		t.Fatal("guided strategy must add permitted_root")
	}
	if !found[ConstraintForbidShell] {
		t.Fatal("read-only intent must forbid shell")
	}
	if !found[ConstraintMaxSteps] {
		t.Fatal("guided strategy must add max_steps")
	}
}

func TestExtractTargets(t *testing.T) {
	tests := []struct {
		prompt string
		want   []string
	}{
		{"update @a/b.go and @c.go", []string{"a/b.go", "c.go"}},
		{"no targets here", nil},
		{"@/@ invalid ref", nil},
		{"duplicate @x.go @x.go", []string{"x.go"}},
	}
	for _, tt := range tests {
		got := extractTargets(tt.prompt)
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("extractTargets(%q) = %v, want %v", tt.prompt, got, tt.want)
		}
	}
}

func TestPlanningStrategyFunc(t *testing.T) {
	s := NewPlanningStrategyFunc("custom", func(in intent.Intent, pc context.PlanningContext) (Goal, error) {
		return NewGoal(in, WithOutcome("custom outcome"))
	})
	g, err := s.DetermineGoal(intent.Must(intent.FamilyFeature), buildPC(t, "x"))
	if err != nil {
		t.Fatal(err)
	}
	if g.Outcome() != "custom outcome" {
		t.Fatalf("outcome = %q", g.Outcome())
	}
	if s.Name() != "custom" {
		t.Fatalf("name = %q", s.Name())
	}
}
