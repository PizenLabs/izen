package planner

import (
	"errors"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
)

func factsFor(intent analyzer.Intent, targets []string) *analyzer.Facts {
	return &analyzer.Facts{
		Root:        "/ws",
		Input:       "test",
		Intent:      intent,
		TargetFiles: targets,
		Files:       len(targets),
		MaxFanout:   2,
		GeneratedAt: time.Now(),
	}
}

func TestBuildBugFixRequiresTest(t *testing.T) {
	p := New()
	plan, err := p.Build(factsFor(analyzer.IntentBugFix, []string{"a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequireTest {
		t.Error("bug_fix plan should require tests")
	}
	if !plan.Checkpoint {
		t.Error("default plan should enable checkpoint")
	}
	if !plan.RollbackEnabled {
		t.Error("plan with targets should enable rollback")
	}
	if plan.Strategy != DefaultStrategy {
		t.Errorf("Strategy = %s, want %s", plan.Strategy, DefaultStrategy)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2 (modify + test)", len(plan.Steps))
	}
	if plan.Steps[0].Action != "modify" || plan.Steps[1].Action != "test" {
		t.Errorf("unexpected step order: %v", plan.Steps)
	}
}

func TestBuildQuestionNoTest(t *testing.T) {
	p := New()
	plan, err := p.Build(factsFor(analyzer.IntentQuestion, nil))
	if err != nil {
		t.Fatal(err)
	}
	if plan.RequireTest {
		t.Error("question plan should not require tests")
	}
	if plan.RollbackEnabled {
		t.Error("plan without targets should not enable rollback")
	}
	if len(plan.Steps) != 0 {
		t.Errorf("Steps = %d, want 0", len(plan.Steps))
	}
}

func TestBuildExpectedOutputs(t *testing.T) {
	p := New()
	plan, err := p.Build(factsFor(analyzer.IntentFeature, []string{"b.go", "a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ExpectedOutputs) != 3 {
		t.Fatalf("ExpectedOutputs = %v, want 3 entries (2 files + test marker)", plan.ExpectedOutputs)
	}
	if plan.ExpectedOutputs[0] != "b.go" || plan.ExpectedOutputs[1] != "a.go" {
		t.Errorf("unexpected outputs: %v", plan.ExpectedOutputs)
	}
}

func TestBuildNilFacts(t *testing.T) {
	p := New()
	if _, err := p.Build(nil); !errors.Is(err, ErrNilFacts) {
		t.Fatalf("err = %v, want ErrNilFacts", err)
	}
}

func TestBuildDeterministic(t *testing.T) {
	p := New()
	f := factsFor(analyzer.IntentBugFix, []string{"x.go"})
	p1, err := p.Build(f)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := p.Build(f)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Reason != p2.Reason || len(p1.Steps) != len(p2.Steps) {
		t.Error("planner is not deterministic")
	}
}

func TestPlannerOptions(t *testing.T) {
	p := New(WithStrategy("gen"), WithCheckpoint(false))
	plan, err := p.Build(factsFor(analyzer.IntentBugFix, []string{"a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "gen" {
		t.Errorf("Strategy = %s, want gen", plan.Strategy)
	}
	if plan.Checkpoint {
		t.Error("checkpoint should be disabled")
	}
}

func TestPlannerStrategySelector(t *testing.T) {
	p := New(WithStrategySelector(func(f *analyzer.Facts) string {
		if f.MaxFanout >= 4 {
			return "iterative"
		}
		return "direct"
	}))
	small := factsFor(analyzer.IntentBugFix, []string{"a.go"})
	small.MaxFanout = 1
	plan, err := p.Build(small)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "direct" {
		t.Errorf("Strategy = %s, want direct", plan.Strategy)
	}
	large := factsFor(analyzer.IntentBugFix, []string{"a.go"})
	large.MaxFanout = 10
	plan, err = p.Build(large)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "iterative" {
		t.Errorf("Strategy = %s, want iterative", plan.Strategy)
	}
}
