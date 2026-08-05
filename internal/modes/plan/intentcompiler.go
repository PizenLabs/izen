package plan

import (
	"fmt"
	"strings"

	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/adapter"
	"github.com/PizenLabs/izen/pkg/engine/inference"
	"github.com/PizenLabs/izen/pkg/engine/lowerer"
	"github.com/PizenLabs/izen/pkg/engine/planner"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// IntentCompilerPlanner runs the IR-driven intent compiler pipeline end to end
// and converts the lowered FileArtifacts into the TUI plan.Task view-model. It
// is the deterministic prime path of the /plan handler: it replaces the legacy
// LLM plan synthesis (and its heuristic prose fallback) for generation
// requests.
//
// Pipeline:
//
//	inference.WorkspaceInspector.Inspect        (1. collect WorkspaceFacts)
//	    → inference.InferenceEngine.Infer       (2. multi-hypothesis inference)
//	    → inference.PolicyEngine.Evaluate       (3. policy separation)
//	    → planner.IRPlanner.Generate            (4. LogicalPlan of IR nodes)
//	    → lowerer.PlanLowerer.Lower             (5. capability graph → adapters)
//	    → FileArtifacts → []Task                (6. TUI staged plan)
//
// The same LogicalPlan lowers into different physical layouts depending on the
// resolved framework: a Static HTML/CSS/JS prompt yields index.html,
// styles.css and script.js through the StaticWebAdapter.
type IntentCompilerPlanner struct {
	rootPath  string
	inspector *inference.WorkspaceInspector
}

// NewIntentCompilerPlanner returns an intent compiler planner bound to the
// workspace root.
func NewIntentCompilerPlanner(rootPath string) *IntentCompilerPlanner {
	return &IntentCompilerPlanner{
		rootPath:  rootPath,
		inspector: inference.NewWorkspaceInspector(rootPath),
	}
}

// RootPath returns the workspace the planner is bound to.
func (p *IntentCompilerPlanner) RootPath() string { return p.rootPath }

// TryPlan runs the intent compiler against each candidate prompt in order. The
// boolean reports whether the intent compiler took ownership:
//
//   - false, nil → not a generation request the intent compiler owns; the
//     caller falls back to the remaining pipeline.
//   - true, tasks → the IR pipeline produced concrete file tasks.
//   - true, error → the pipeline rejected the request (policy escalation /
//     lowering failure); the error message carries the explicit reason and is
//     safe to surface in the TUI status bar.
func (p *IntentCompilerPlanner) TryPlan(ctx stdctx.Context, candidates ...string) ([]Task, bool, error) {
	if p == nil {
		return nil, false, nil
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		tasks, handled, err := p.tryOne(ctx, candidate)
		if err != nil || handled {
			return tasks, handled, err
		}
	}
	return nil, false, nil
}

// tryOne runs the full intent compiler pipeline for a single prompt.
func (p *IntentCompilerPlanner) tryOne(ctx stdctx.Context, prompt string) ([]Task, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, false, ctx.Err()
	}

	// 1. Collect WorkspaceFacts via the WorkspaceInspector.
	facts := p.inspector.Inspect()

	// 2. Run multi-hypothesis inference over the facts + prompt slots.
	set := inference.NewInferenceEngine().Infer(facts, inference.PromptSlots{Raw: prompt})

	// 3. Evaluate the policy on the framework dimension.
	verdict := inference.NewPolicyEngine().Evaluate(set, inference.TypeFramework)
	fw, resolved := lowerer.ResolveFramework(set.ResolvedFramework())

	switch verdict.Decision {
	case inference.DecisionEscalateToHuman:
		// Two credible framework hypotheses compete within the delta
		// threshold — never choose unilaterally; escalate to the human.
		return nil, true, fmt.Errorf("intent compiler: cannot choose a framework unilaterally — %s", verdict.Reason)
	case inference.DecisionFallback:
		// No confident framework hypothesis. A greenfield web request is still
		// owned deterministically with the static renderer; otherwise the
		// request is not the intent compiler's concern.
		if fw == "" && strategy.IsGreenfieldWebPrompt(prompt) {
			fw = adapter.FrameworkStaticWeb
			resolved = true
		}
	}

	if !resolved {
		return nil, false, nil
	}

	// The IR planner only owns greenfield website generation. A resolved
	// framework for a non-generation request (e.g. "add react to my app") is
	// not the intent compiler's concern — the caller falls back.
	if !strategy.IsGreenfieldWebPrompt(prompt) {
		return nil, false, nil
	}

	// 4. Generate the LogicalPlan (framework-agnostic IR nodes).
	lp, err := planner.NewIRPlanner().Generate(prompt)
	if err != nil {
		return nil, true, fmt.Errorf("intent compiler: IR plan generation failed: %w", err)
	}

	// 5. Lower the LogicalPlan through the capability graph into concrete
	// FileArtifacts via the resolved framework's adapters.
	artifacts, err := lowerer.NewPlanLowerer(lowerer.DefaultRegistry()).Lower(lp, fw)
	if err != nil {
		return nil, true, fmt.Errorf("intent compiler: lowering %s plan failed: %w", fw, err)
	}
	if len(artifacts) == 0 {
		return nil, true, fmt.Errorf("intent compiler: %s plan produced no file artifacts", fw)
	}

	// 6. Populate the TUI staged plan tasks directly from the FileArtifacts.
	tasks := artifactsToTasks(artifacts)
	return tasks, true, nil
}

// artifactsToTasks converts lowered FileArtifacts into hardcoded FILE_MUTATE
// tasks so the deterministic targets survive evidence-based existence filters.
func artifactsToTasks(artifacts []adapter.FileArtifact) []Task {
	out := make([]Task, 0, len(artifacts))
	for _, a := range artifacts {
		path := a.Path
		if path == "" {
			continue
		}
		out = append(out, Task{
			StepNum: len(out) + 1,

			Status:      "idle",
			Type:        "FILE_MUTATE",
			Target:      path,
			Description: "CREATE " + path,
			Rationale:   "Generated by the IR-driven intent compiler.",
			Solution:    "File generated by the resolved framework adapter.",
			IsHardcoded: true,
		})
	}
	return out
}
