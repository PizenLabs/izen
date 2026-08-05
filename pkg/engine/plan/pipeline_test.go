package plan

import (
	"testing"

	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/context"
	"github.com/PizenLabs/izen/pkg/engine/intent"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// gemma426B is the deterministic stand-in for the Gemma 4 26B model used in
// the greenfield generation test pass. It mirrors the model's role in the
// pipeline: given a greenfield prompt it proposes the outcome, the files to
// create and the success criteria.
func gemma426B() strategy.GoalGenerator {
	return strategy.NewGoalGeneratorFunc("gemma-4-26b", func(_ stdctx.Context, req strategy.GoalRequest) (strategy.GoalResult, error) {
		return strategy.GoalResult{
			Outcome:  "A Go HTTP server exposing a /health endpoint",
			NewFiles: []string{"cmd/server/main.go", "go.mod", "internal/api/health.go"},
			Criteria: []string{"/health returns 200"},
		}, nil
	})
}

// runGreenfieldPipeline executes the full immutable microkernel pipeline in a
// fresh empty workspace and returns every intermediate artifact.
func runGreenfieldPipeline(t *testing.T, prompt string) (goal strategy.Goal, logical *LogicalPlan, normalized *NormalizedPlan, validated *ValidatedPlan, decision PolicyDecision, executable *ExecutablePlan, report *PreconditionReport, pc context.PlanningContext) {
	t.Helper()
	ws := t.TempDir()

	// ── 1. Context providers assemble without assumptions ──────────────
	collector := context.NewCollector()
	collector.Register(context.ProviderPrompt, context.NewPromptProvider(prompt))
	collector.Register(context.ProviderFilesystem, context.NewFilesystemProvider(ws, 0, true))
	collector.Register(context.ProviderEnvironment, context.NewEnvironmentProvider())
	collector.Register(context.ProviderRepository, context.NewRepositoryProvider(ws))
	pc, err := collector.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("context assembly: %v", err)
	}

	// ── 2. Strategy determines the Goal ────────────────────────────────
	in := intent.Classify(prompt)
	if in.Family() != intent.FamilyGreenfield {
		t.Fatalf("family = %s, want greenfield for %q", in.Family(), prompt)
	}
	goal, err = strategy.NewPromptStrategy().WithModel(gemma426B()).DetermineGoal(in, pc)
	if err != nil {
		t.Fatalf("DetermineGoal: %v", err)
	}

	// ── 3. Planner derives the LogicalPlan ─────────────────────────────
	logical, err = NewPlanner().Derive(goal)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	// ── 4. Normalizer → NormalizedPlan ─────────────────────────────────
	normalized, err = NewPlanNormalizer().Normalize(logical)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	// ── 5. Validator → ValidatedPlan ───────────────────────────────────
	validated, err = NewPlanValidator().Validate(normalized)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !validated.Valid() {
		t.Fatalf("plan invalid: %v", validated.FailedResults())
	}

	// ── 6. PolicyEngine approves ───────────────────────────────────────
	engine := NewPolicyEngine(NewDefaultPolicy(ws))
	decision = engine.Evaluate(validated)
	if !decision.Approved() {
		t.Fatalf("policy rejected: %v", decision.Summary())
	}

	// ── 7. Lowerer → ExecutablePlan ────────────────────────────────────
	executable, err = NewPlanLowerer(ws).Lower(validated)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}

	// ── 8. ExecutionPreconditions pass in the empty workspace ──────────
	report, err = NewExecutionPreconditions(ws).Check(executable)
	if err != nil {
		t.Fatalf("Preconditions: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("not ready: %v", report.Failed())
	}
	return goal, logical, normalized, validated, decision, executable, report, pc
}

func TestGreenfieldGenerationPipeline(t *testing.T) {
	const prompt = "Create a new Go HTTP server project from scratch with a /health endpoint"
	goal, logical, normalized, validated, decision, executable, report, pc := runGreenfieldPipeline(t, prompt)

	// Context assembled without assuming an existing workspace.
	if got := pc.Errors(); len(got) != 0 {
		t.Fatalf("unexpected context failures: %v", got)
	}
	if chunk, ok := pc.Get(context.ProviderFilesystem); !ok || !chunk.Empty() {
		t.Fatal("filesystem chunk must be empty for a fresh workspace")
	}

	// Strategy produced a Goal; Planner produced a LogicalPlan.
	if goal.Outcome() != "A Go HTTP server exposing a /health endpoint" {
		t.Fatalf("outcome = %q", goal.Outcome())
	}
	if len(goal.NewFiles()) != 3 {
		t.Fatalf("new files = %v", goal.NewFiles())
	}
	if logical.Goal().Outcome() != goal.Outcome() {
		t.Fatal("logical plan does not carry the goal")
	}

	// LogicalPlan steps: 3 create + 1 verify (feature-derived greenfield
	// still carries the model's criteria, but greenfield has no test facet).
	kinds := map[StepKind]int{}
	for _, s := range logical.Steps() {
		kinds[s.Kind()]++
	}
	if kinds[StepCreate] != 3 {
		t.Fatalf("create steps = %d, want 3: %v", kinds[StepCreate], kinds)
	}

	// Normalized: ids renumbered deterministically, no dupes.
	if normalized.StepCount() != logical.StepCount() {
		t.Fatalf("normalized %d != logical %d", normalized.StepCount(), logical.StepCount())
	}
	for i, s := range normalized.Steps() {
		if want := "s" + itoa(i+1); s.ID() != want {
			t.Fatalf("step %d id = %q, want %s", i, s.ID(), want)
		}
	}

	// Validated: schema and logic pass.
	if !validated.Valid() {
		t.Fatalf("validated plan failed: %v", validated.FailedResults())
	}

	// Policy approved.
	if !decision.Approved() {
		t.Fatalf("policy decision: %v", decision.Summary())
	}

	// ExecutablePlan: physical, absolute, no shell steps (greenfield has no
	// verify/test requirement).
	if executable.StepCount() != normalized.StepCount() {
		t.Fatalf("executable %d != normalized %d", executable.StepCount(), normalized.StepCount())
	}
	for _, es := range executable.Steps() {
		if es.Shell() {
			t.Fatalf("greenfield plan must not shell out: %+v", es)
		}
		if es.WorkDir() == "" || es.ResolvedTarget() == "" {
			t.Fatalf("executable step missing physical detail: %+v", es)
		}
	}

	// Preconditions ready.
	if !report.Ready() {
		t.Fatalf("preconditions not ready: %v", report.Failed())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestPipelineStagesDoNotMutateInputs(t *testing.T) {
	const prompt = "Create a new Go HTTP server project from scratch"
	_, logical, _, validated, _, executable, _, _ := runGreenfieldPipeline(t, prompt)

	// Snapshot every artifact through its accessors.
	logicalBefore := logical.Steps()
	goalBefore := logical.Goal().Outcome()

	np2, err := NewPlanNormalizer().Normalize(logical)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStepOrder(logicalBefore, logical.Steps()) || logical.Goal().Outcome() != goalBefore {
		t.Fatal("Normalize mutated the LogicalPlan")
	}

	normBefore := np2.Steps()
	vp2, err := NewPlanValidator().Validate(np2)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStepOrder(normBefore, np2.Steps()) {
		t.Fatal("Validate mutated the NormalizedPlan")
	}

	valBefore := vp2.Steps()
	_ = NewPolicyEngine(NewDefaultPolicy(t.TempDir())).Evaluate(vp2)
	if !sameStepOrder(valBefore, vp2.Steps()) {
		t.Fatal("PolicyEngine mutated the ValidatedPlan")
	}

	exeBefore := executable.Steps()
	_ = NewPolicyEngine(NewDefaultPolicy(t.TempDir())).Evaluate(validated)
	if len(executable.Steps()) != len(exeBefore) {
		t.Fatal("PolicyEngine mutated the ExecutablePlan")
	}
}

func TestPipelineRejectsForbiddenGenerationTarget(t *testing.T) {
	ws := t.TempDir()
	collector := context.NewCollector()
	collector.Register(context.ProviderPrompt, context.NewPromptProvider("create a project"))
	collector.Register(context.ProviderFilesystem, context.NewFilesystemProvider(ws, 0, true))
	pc, err := collector.Collect(stdctx.Background())
	if err != nil {
		t.Fatal(err)
	}
	goal, err := strategy.NewPromptStrategy().DetermineGoal(intent.Classify("create a project"), pc)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a model proposing a file outside the workspace.
	goal, err = strategy.NewGoal(goal.Intent(),
		strategy.WithOutcome(goal.Outcome()),
		strategy.WithNewFiles("../../outside.tmp"),
	)
	if err != nil {
		t.Fatal(err)
	}
	logical, err := NewPlanner().Derive(goal)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NewPlanNormalizer().Normalize(logical)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := NewPlanValidator().Validate(normalized)
	if err != nil {
		t.Fatal(err)
	}
	decision := NewPolicyEngine(NewDefaultPolicy(ws)).Evaluate(validated)
	if decision.Approved() {
		t.Fatal("plan proposing an out-of-workspace file must be denied by policy")
	}
	if _, err := NewPlanLowerer(ws).Lower(validated); err == nil {
		t.Fatal("lowerer must reject an escaping target")
	}
}
