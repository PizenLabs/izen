package plan

import (
	"os"
	"strings"
	"testing"

	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/intent"
	eplan "github.com/PizenLabs/izen/pkg/engine/plan"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// greenfieldPrompt is the verification prompt: a greenfield static website
// generation request.
const greenfieldPrompt = "make the website introduce for JAY with your job is software engineer using html, css and js"

func TestMicrokernelPlannerGreenfieldProducedExplicitTargets(t *testing.T) {
	p := NewMicrokernelPlanner(t.TempDir())
	tasks, handled, err := p.TryPlan(stdctx.Background(), greenfieldPrompt)
	if err != nil {
		t.Fatalf("TryPlan: %v", err)
	}
	if !handled {
		t.Fatal("microkernel should handle a greenfield web prompt")
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d, want 3 (index.html, styles.css, script.js)", len(tasks))
	}

	want := []struct {
		target string
		desc   string
	}{
		{"index.html", "CREATE index.html"},
		{"styles.css", "CREATE styles.css"},
		{"script.js", "CREATE script.js"},
	}
	for i, w := range want {
		if tasks[i].Target != w.target {
			t.Errorf("task %d target = %q, want %q", i, tasks[i].Target, w.target)
		}
		if tasks[i].Type != "FILE_MUTATE" {
			t.Errorf("task %d type = %q, want FILE_MUTATE", i, tasks[i].Type)
		}
		if !strings.HasPrefix(tasks[i].Description, w.desc) {
			t.Errorf("task %d description = %q, want prefix %q", i, tasks[i].Description, w.desc)
		}
		if tasks[i].StepNum != i+1 {
			t.Errorf("task %d StepNum = %d, want %d", i, tasks[i].StepNum, i+1)
		}
	}
	// No heuristic fallback: every task must carry an explicit file target.
	for _, tk := range tasks {
		if strings.TrimSpace(tk.Target) == "" {
			t.Fatal("microkernel task has an empty target — heuristic fallback would produce this")
		}
	}
}

func TestMicrokernelPlannerEmptyWorkspaceReady(t *testing.T) {
	ws := t.TempDir()
	p := NewMicrokernelPlanner(ws)
	tasks, handled, err := p.TryPlan(stdctx.Background(), greenfieldPrompt)
	if err != nil {
		t.Fatalf("TryPlan must succeed in an empty workspace: %v", err)
	}
	if !handled || len(tasks) == 0 {
		t.Fatal("greenfield prompt must be handled in an empty workspace")
	}
}

func TestMicrokernelPlannerNotApplicable(t *testing.T) {
	p := NewMicrokernelPlanner(t.TempDir())
	for _, prompt := range []string{
		"the handler crashes with a nil pointer on startup",
		"refactor the checkout module to decouple payments",
		"explain how the routing layer works",
		"", // empty candidate
	} {
		tasks, handled, err := p.TryPlan(stdctx.Background(), prompt)
		if err != nil {
			t.Fatalf("TryPlan(%q): %v", prompt, err)
		}
		if handled {
			t.Fatalf("TryPlan(%q) handled=%v, want false", prompt, handled)
		}
		if tasks != nil {
			t.Fatalf("TryPlan(%q) tasks=%v, want nil", prompt, tasks)
		}
	}
}

func TestMicrokernelPlannerCandidateOrder(t *testing.T) {
	p := NewMicrokernelPlanner(t.TempDir())
	// The first candidate is a non-web handoff marker; the second carries the
	// greenfield prompt. TryPlan must fall through to the greenfield one.
	tasks, handled, err := p.TryPlan(stdctx.Background(),
		"frontend ui intent detected — hand off to plan",
		greenfieldPrompt,
	)
	if err != nil {
		t.Fatalf("TryPlan: %v", err)
	}
	if !handled || len(tasks) != 3 {
		t.Fatalf("handled=%v tasks=%d, want handled with 3 tasks", handled, len(tasks))
	}
}

func TestMicrokernelPlannerRejectionReasonSurfacesPolicy(t *testing.T) {
	// A policy root that cannot contain the greenfield files forces a policy
	// rejection whose reason must be explicit and human-readable.
	ws := t.TempDir()
	// Point the lowerer at a *different* root than the policy bound by
	// building a planner whose root is the workspace but whose policy path is
	// constrained. The default policy roots at the workspace, so an escaping
	// target is the cleanest trigger — but greenfield targets never escape.
	// Instead we assert the rejection path is well-formed when the workspace
	// root is a file (not a directory), which fails preconditions cleanly.
	blocked := ws + "/blocked-file"
	if err := writeTestFile(t, blocked, "x"); err != nil {
		t.Fatal(err)
	}
	p := NewMicrokernelPlanner(blocked)
	_, handled, err := p.TryPlan(stdctx.Background(), greenfieldPrompt)
	if !handled {
		t.Fatal("microkernel should own the request even when a stage rejects it")
	}
	if err == nil {
		t.Fatal("expected a rejection error when the workspace root is not a directory")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "microkernel") {
		t.Fatalf("rejection reason should be microkernel-labelled: %v", err)
	}
}

func TestConvertExecutableToTasks(t *testing.T) {
	g, err := strategy.NewGoal(intent.Must(intent.FamilyGreenfield),
		strategy.WithOutcome("generate a website"),
		strategy.WithNewFiles("index.html"),
	)
	if err != nil {
		t.Fatal(err)
	}
	steps := []eplan.Step{
		eplan.NewStep(eplan.StepCreate, "index.html", eplan.WithID("s1")),
		eplan.NewStep(eplan.StepModify, "styles.css", eplan.WithID("s2")),
		eplan.NewStep(eplan.StepDelete, "old.txt", eplan.WithID("s3")),
		eplan.NewStep(eplan.StepRun, "go vet ./...", eplan.WithID("s4")),
		eplan.NewStep(eplan.StepRead, "README.md", eplan.WithID("s5")),
	}
	vp := eplan.NewValidatedPlan(g, steps, nil, true)
	ep, err := eplan.NewPlanLowerer(t.TempDir()).Lower(vp)
	if err != nil {
		t.Fatal(err)
	}
	tasks := convertExecutableToTasks(ep)
	if len(tasks) != 4 {
		t.Fatalf("tasks = %d, want 4 (read step dropped)", len(tasks))
	}
	if tasks[0].Description != "CREATE index.html" || tasks[0].Target != "index.html" {
		t.Fatalf("task 0 = %+v", tasks[0])
	}
	if tasks[1].Description != "WRITE styles.css" || tasks[1].Target != "styles.css" {
		t.Fatalf("task 1 = %+v", tasks[1])
	}
	if tasks[2].Description != "DELETE old.txt" {
		t.Fatalf("task 2 = %+v", tasks[2])
	}
	if tasks[3].Type != "SHELL_EXEC" || tasks[3].Target != "go vet ./..." {
		t.Fatalf("task 3 = %+v", tasks[3])
	}
	for _, tk := range tasks {
		if !tk.IsHardcoded {
			t.Fatalf("microkernel task %+v must be hardcoded to survive filters", tk)
		}
	}
}

func writeTestFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}
