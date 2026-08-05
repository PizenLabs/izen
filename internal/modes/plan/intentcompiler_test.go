package plan

import (
	"strings"
	"testing"

	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/adapter"
	"github.com/PizenLabs/izen/pkg/engine/inference"
	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
	"github.com/PizenLabs/izen/pkg/engine/lowerer"
	"github.com/PizenLabs/izen/pkg/engine/planner"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// verificationPrompt is the exact TUI verification scenario: a greenfield
// static website generation request with HTML, CSS and JavaScript.
const verificationPrompt = "Design a website introducing JAY, describing your job as a software engineer, using HTML, CSS, and JavaScript."

// TestIntentCompiler_VerificationPromptStagesExplicitTargets is the end-to-end
// verification of the IR-driven intent compiler: the prompt must be planned
// deterministically into index.html, styles.css and script.js FileArtifacts
// via the Static HTML/CSS/JS framework adapter — never a generic CODE_MOD
// [Target 1/1] task and never a heuristic extraction.
func TestIntentCompiler_VerificationPromptStagesExplicitTargets(t *testing.T) {
	p := NewIntentCompilerPlanner(t.TempDir())
	tasks, handled, err := p.TryPlan(stdctx.Background(), verificationPrompt)
	if err != nil {
		t.Fatalf("TryPlan: %v", err)
	}
	if !handled {
		t.Fatal("intent compiler must own a greenfield website prompt")
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d, want 3 (index.html, styles.css, script.js): %+v", len(tasks), tasks)
	}

	wantTargets := []string{"index.html", "styles.css", "script.js"}
	for i, want := range wantTargets {
		if tasks[i].Target != want {
			t.Errorf("task %d target = %q, want %q", i, tasks[i].Target, want)
		}
		if tasks[i].Type != "FILE_MUTATE" {
			t.Errorf("task %d type = %q, want FILE_MUTATE", i, tasks[i].Type)
		}
		if !strings.HasPrefix(tasks[i].Description, "CREATE "+want) {
			t.Errorf("task %d description = %q, want CREATE %s", i, tasks[i].Description, want)
		}
		if !tasks[i].IsHardcoded {
			t.Errorf("task %d must be hardcoded to survive evidence filters", i)
		}
	}

	// Zero heuristic/empty targets — the hard-kill contract.
	for _, tk := range tasks {
		if strings.TrimSpace(tk.Target) == "" {
			t.Fatal("intent compiler produced an empty target — generic CODE_MOD fallback would do this")
		}
		if strings.Contains(tk.Description, "model reasoning") {
			t.Fatalf("task %q references the legacy heuristic fallback", tk.Description)
		}
	}
}

// TestIntentCompiler_NotApplicable verifies non-generation prompts are not
// owned by the intent compiler (the legacy LLM synthesis remains for them).
func TestIntentCompiler_NotApplicable(t *testing.T) {
	p := NewIntentCompilerPlanner(t.TempDir())
	for _, prompt := range []string{
		"the handler crashes with a nil pointer on startup",
		"refactor the checkout module to decouple payments",
		"explain how the routing layer works",
		"",
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

// TestIntentCompiler_EmptyWorkspaceReady verifies the intent compiler succeeds
// in an empty workspace (no config, no dependencies — prompt evidence only).
func TestIntentCompiler_EmptyWorkspaceReady(t *testing.T) {
	ws := t.TempDir()
	p := NewIntentCompilerPlanner(ws)
	tasks, handled, err := p.TryPlan(stdctx.Background(), verificationPrompt)
	if err != nil {
		t.Fatalf("TryPlan must succeed in an empty workspace: %v", err)
	}
	if !handled || len(tasks) == 0 {
		t.Fatal("greenfield prompt must be handled in an empty workspace")
	}
}

// TestIntentCompiler_CandidateOrder verifies the compiler scans candidates in
// order and falls through to a later greenfield prompt.
func TestIntentCompiler_CandidateOrder(t *testing.T) {
	p := NewIntentCompilerPlanner(t.TempDir())
	tasks, handled, err := p.TryPlan(stdctx.Background(),
		"frontend ui intent detected — hand off to plan",
		verificationPrompt,
	)
	if err != nil {
		t.Fatalf("TryPlan: %v", err)
	}
	if !handled || len(tasks) != 3 {
		t.Fatalf("handled=%v tasks=%d, want handled with 3 tasks", handled, len(tasks))
	}
}

// TestIntentCompilerPipeline_EndToEnd exercises the five mandatory pipeline
// stages explicitly: inspect → infer → policy → IR plan → lower.
func TestIntentCompilerPipeline_EndToEnd(t *testing.T) {
	// 1. Collect WorkspaceFacts via the WorkspaceInspector.
	inspector := inference.NewWorkspaceInspector(t.TempDir())
	facts := inspector.Inspect()
	if len(facts.Files) != 0 {
		t.Fatalf("expected an empty surface, got %v", facts.Files)
	}

	// 2. Run multi-hypothesis inference.
	set := inference.NewInferenceEngine().Infer(facts, inference.PromptSlots{Raw: verificationPrompt})
	if set.ResolvedFramework() != "Static HTML/CSS/JS" {
		t.Fatalf("resolved framework = %q, want Static HTML/CSS/JS", set.ResolvedFramework())
	}

	// 3. Evaluate the policy.
	verdict := inference.NewPolicyEngine().Evaluate(set, inference.TypeFramework)
	if verdict.Decision != inference.DecisionProceed {
		t.Fatalf("policy decision = %q, want proceed (%s)", verdict.Decision, verdict.Reason)
	}

	// 4. Generate the LogicalPlan (IR nodes).
	lp, err := planner.NewIRPlanner().Generate(verificationPrompt)
	if err != nil {
		t.Fatalf("IRPlanner.Generate: %v", err)
	}
	if lp.KindCount(ir.NodeCreatePage) != 1 || lp.KindCount(ir.NodeCreateStyle) != 1 || lp.KindCount(ir.NodeCreateScript) != 1 {
		t.Fatalf("unexpected IR node mix: pages=%d styles=%d scripts=%d",
			lp.KindCount(ir.NodeCreatePage), lp.KindCount(ir.NodeCreateStyle), lp.KindCount(ir.NodeCreateScript))
	}

	// 5. Lower through the capability graph → framework adapter.
	fw, ok := lowerer.ResolveFramework(set.ResolvedFramework())
	if !ok {
		t.Fatal("framework not resolvable")
	}
	if fw != adapter.FrameworkStaticWeb {
		t.Fatalf("framework = %q, want static-web", fw)
	}
	artifacts, err := lowerer.NewPlanLowerer(lowerer.DefaultRegistry()).Lower(lp, fw)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("artifacts = %d, want 3", len(artifacts))
	}
	want := []string{"index.html", "styles.css", "script.js"}
	for i, a := range artifacts {
		if a.Path != want[i] {
			t.Errorf("artifact %d path = %q, want %q", i, a.Path, want[i])
		}
		if a.Content == "" {
			t.Errorf("artifact %d has empty content", i)
		}
	}
}

// TestStrategyGreenfieldWebRecognizesDesignPrompt pins the classifier fix: the
// verification prompt must classify as a greenfield web request.
func TestStrategyGreenfieldWebRecognizesDesignPrompt(t *testing.T) {
	if !strategy.IsGreenfieldWebPrompt(verificationPrompt) {
		t.Fatal("verification prompt must be recognized as a greenfield web request")
	}
}
