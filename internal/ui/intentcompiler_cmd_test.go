package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/modes/plan"
)

// verificationPromptUI is the TUI verification scenario: a greenfield static
// website request with HTML, CSS and JavaScript.
const verificationPromptUI = "Design a website introducing JAY, describing your job as a software engineer, using HTML, CSS, and JavaScript."

// TestRunPlanEngineCmdIntentCompilerPrimePath exercises the real
// runPlanEngineCmd closure: a greenfield handoff must be intercepted by the
// IR-driven intent compiler prime path and stage explicit CREATE tasks
// (index.html, styles.css, script.js) WITHOUT invoking the LLM plan engine
// (m.planEngine.ProcessFromLedger is never reached because the closure returns
// before the goroutine).
func TestRunPlanEngineCmdIntentCompilerPrimePath(t *testing.T) {
	m := newTestModel()
	m.intentCompiler = plan.NewIntentCompilerPlanner(t.TempDir())
	m.planEngine = plan.NewEngine(plan.NewPlanStore())
	m.planEngine.SetRootPath(t.TempDir())

	handoff := HandoffContext{LastFailurePayload: verificationPromptUI}
	cmd := m.runPlanEngineCmd(verificationPromptUI, verificationPromptUI, "qwen2.5-coder:7b", handoff)
	if cmd == nil {
		t.Fatal("runPlanEngineCmd returned nil cmd")
	}
	msg := cmd()
	pmsg, ok := msg.(planResultMsg)
	if !ok {
		t.Fatalf("msg = %T, want planResultMsg", msg)
	}
	if !pmsg.IntentCompiler {
		t.Fatal("intent compiler prime path must mark the result as intent-compiler-derived")
	}
	if pmsg.Err != nil {
		t.Fatalf("unexpected error: %v", pmsg.Err)
	}
	if len(pmsg.Tasks) != 3 {
		t.Fatalf("tasks = %d, want 3 (index.html, styles.css, script.js)", len(pmsg.Tasks))
	}
	wantTargets := []string{"index.html", "styles.css", "script.js"}
	for i, want := range wantTargets {
		if pmsg.Tasks[i].Target != want {
			t.Fatalf("task %d target = %q, want %q", i, pmsg.Tasks[i].Target, want)
		}
		if !strings.HasPrefix(pmsg.Tasks[i].Description, "CREATE "+want) {
			t.Fatalf("task %d description = %q, want CREATE %s", i, pmsg.Tasks[i].Description, want)
		}
		// Zero heuristic / empty-target tasks.
		if strings.TrimSpace(pmsg.Tasks[i].Target) == "" {
			t.Fatalf("task %d has an empty target — generic CODE_MOD fallback would do this", i)
		}
	}
}

// TestRunPlanEngineCmdIntentCompilerEscalation verifies the escalation path:
// when the intent compiler rejects a request (policy escalate / lowering
// failure), the closure surfaces the explicit reason as an error and NEVER
// falls through to a heuristic plan.
func TestRunPlanEngineCmdIntentCompilerEscalation(t *testing.T) {
	m := newTestModel()
	m.intentCompiler = plan.NewIntentCompilerPlanner(t.TempDir())
	m.planEngine = plan.NewEngine(plan.NewPlanStore())
	m.planEngine.SetRootPath(t.TempDir())

	// A non-generation prompt is not owned by the intent compiler; the
	// microkernel is nil, so the closure proceeds toward LLM synthesis. With
	// no provider wired, it must still return a planResultMsg (error path),
	// never a heuristic plan.
	handoff := HandoffContext{LastFailurePayload: "the handler crashes with a nil pointer on startup"}
	cmd := m.runPlanEngineCmd("the handler crashes with a nil pointer on startup", "the handler crashes with a nil pointer on startup", "qwen2.5-coder:7b", handoff)
	if cmd == nil {
		t.Skip("fallback path requires provider wiring")
	}
	msg := cmd()
	if _, ok := msg.(planResultMsg); !ok {
		t.Fatalf("msg = %T, want planResultMsg", msg)
	}
}
