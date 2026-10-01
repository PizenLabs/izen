package plan

import (
	"fmt"
	"path/filepath"
	"strings"

	stdctx "context"

	"github.com/PizenLabs/izen/internal/engine/adapter"
	"github.com/PizenLabs/izen/internal/engine/inference"
	"github.com/PizenLabs/izen/internal/engine/lowerer"
	"github.com/PizenLabs/izen/internal/engine/planner"
	"github.com/PizenLabs/izen/internal/engine/strategy"
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
		// SINGLE-CANDIDATE RESOLUTION GUARD: a Static HTML/CSS/JS (Vanilla
		// Web archetype) project whose only credible hypothesis is confident
		// and has NO competing runner-up (nil or 0.00 confidence) is a false
		// positive of the delta policy — there is nothing to escalate to. The
		// engine selects the VANILLA_WEB adapter unilaterally instead of
		// aborting with "cannot choose a framework unilaterally".
		if fw == adapter.FrameworkStaticWeb &&
			verdict.Top.Confidence() > 0.50 &&
			runnerUpConfidenceForPlan(verdict.RunnerUp) == 0.00 {
			resolved = true
			break
		}
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

	// 6. Gate the lowered artifacts against the DISCOVERED workspace before any
	// of them is staged.
	//
	// PHASE 12 (discovery before concrete decomposition): the inspection above
	// used to feed framework inference only, while the artifact SET itself came
	// from prompt substrings — so the very same three files were staged in a
	// completely empty workspace, marked `IsHardcoded`, with no recorded reason
	// for why any of them was in scope. An artifact is now staged only when the
	// workspace or the prompt provides evidence for it, and every staged task
	// carries that evidence so a human can audit the scope decision.
	supported, dropped, evidence := filterArtifactsByEvidence(artifacts, facts, prompt)
	if len(supported) == 0 {
		// Nothing is evidence-backed: decline ownership instead of staging a
		// blind template. The caller falls through to the real plan pipeline,
		// which asks the model to propose a plan against actual repository
		// evidence. This is the truthful outcome, not a silent degradation.
		reason := "intent compiler: no artifact is supported by the discovered workspace or the prompt"
		if len(dropped) > 0 {
			reason += " (pruned: " + strings.Join(dropped, ", ") + ")"
		}
		diagnosticf("[intent-compiler] %s", reason)
		return nil, false, nil
	}
	tasks := artifactsToTasks(supported, evidence)
	return tasks, true, nil
}

// diagnosticf routes intent-compiler evidence onto the shared bus when one is
// wired. A nil sink disables emission so headless harnesses stay silent.
func diagnosticf(format string, args ...interface{}) {
	if intentCompilerDiagnostic != nil {
		intentCompilerDiagnostic(format, args...)
	}
}

// intentCompilerDiagnostic is the optional evidence sink installed by the
// composition root. It is diagnostics only: it never stages, mutates, or
// authorizes.
var intentCompilerDiagnostic func(format string, args ...interface{})

// SetIntentCompilerDiagnostic installs the evidence sink.
func SetIntentCompilerDiagnostic(fn func(format string, args ...interface{})) {
	intentCompilerDiagnostic = fn
}

// runnerUpConfidenceForPlan returns the runner-up confidence, or 0.00 when no
// competing hypothesis exists (nil runner-up = zero competing frameworks).
func runnerUpConfidenceForPlan(h *inference.Hypothesis) float64 {
	if h == nil {
		return 0.00
	}
	return h.Confidence()
}

// artifactsToTasks converts evidence-backed FileArtifacts into FILE_MUTATE
// tasks.
//
// `IsHardcoded` keeps its original meaning — "the deterministic plan author
// chose this, not the model" — which is exactly true here and is what the
// plan engine's governance filters rely on. What PHASE 12 adds is the
// EVIDENCE: `Rationale` now names why the artifact is in scope, so the staged
// plan is auditable instead of opaque. The scope decision is a proposal; it
// still requires /build plus the executor's admission and authorization.
func artifactsToTasks(artifacts []adapter.FileArtifact, evidence map[string]string) []Task {
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
			Rationale:   "Intent compiler: " + artifactScopeEvidence(path, evidence) + ".",
			Solution:    "File generated by the resolved framework adapter.",
			IsHardcoded: true,
		})
	}
	return out
}

// artifactScopeEvidence renders the recorded reason an artifact is in scope.
func artifactScopeEvidence(path string, evidence map[string]string) string {
	if ev, ok := evidence[path]; ok {
		return ev
	}
	return "scope proposed by the IR-driven intent compiler"
}

// filterArtifactsByEvidence partitions lowered artifacts into the ones the
// discovered workspace or the explicit prompt supports, and the ones it does
// not.
//
// Support is deliberately structural and project-agnostic:
//   - the workspace ALREADY CONTAINS a file of the same technology class
//     (same extension, or the same directory family for extensionless entry
//     points), so the target is a real part of the project shape; or
//   - the PROMPT explicitly names that technology, so the human asked for it.
//
// Nothing else is staged. A target that is neither an existing project file
// nor an explicitly requested technology is exactly the "blindly synthesized
// artifact" this gate exists to remove.
// evidence maps each kept artifact path to the concrete reason it survived the
// gate. It is returned to the caller rather than stored in package state so the
// pipeline stays free of shared mutable data.
func filterArtifactsByEvidence(artifacts []adapter.FileArtifact, facts inference.WorkspaceFacts, prompt string) (kept []adapter.FileArtifact, dropped []string, evidence map[string]string) {
	lower := strings.ToLower(prompt)
	existing := make(map[string]bool, len(facts.Files))
	extClasses := make(map[string]bool, len(facts.Files))
	for _, f := range facts.Files {
		base := filepath.Base(f)
		existing[strings.ToLower(base)] = true
		existing[strings.ToLower(f)] = true
		if ext := strings.ToLower(filepath.Ext(f)); ext != "" {
			extClasses[ext] = true
		}
	}
	kept = make([]adapter.FileArtifact, 0, len(artifacts))
	dropped = make([]string, 0, len(artifacts))
	nextEvidence := make(map[string]string, len(artifacts))
	for _, a := range artifacts {
		path := a.Path
		if path == "" {
			continue
		}
		base := strings.ToLower(filepath.Base(path))
		ext := strings.ToLower(filepath.Ext(path))
		switch {
		case existing[base] || existing[strings.ToLower(path)]:
			// The workspace already contains this file: the target is part of
			// the discovered project shape, so a rewrite/creation of it is in
			// scope without further justification.
			nextEvidence[path] = "workspace already contains " + path
			kept = append(kept, a)
		case promptNamesTechnology(lower, base, ext):
			nextEvidence[path] = "prompt explicitly requests " + technologyLabel(base, ext)
			kept = append(kept, a)
		case ext != "" && extClasses[ext]:
			// The workspace speaks this technology, so the artifact belongs to
			// the discovered project shape.
			nextEvidence[path] = "workspace contains " + strings.TrimPrefix(ext, ".") + " sources"
			kept = append(kept, a)
		default:
			dropped = append(dropped, path)
		}
	}
	return kept, dropped, nextEvidence
}

// promptNamesTechnology reports whether the prompt explicitly asked for the
// artifact's technology. It matches the file's own basename and the generic
// name of its extension class, so "using HTML, CSS, and JS" supports
// index.html / styles.css / script.js while an unmentioned class does not.
func promptNamesTechnology(lowerPrompt, base, ext string) bool {
	if base != "" && strings.Contains(lowerPrompt, base) {
		return true
	}
	switch ext {
	case ".html", ".htm":
		return containsAnyFold(lowerPrompt, "html", "webpage", "web page", "page", "site", "website")
	case ".css", ".scss", ".sass", ".less":
		return containsAnyFold(lowerPrompt, "css", "stylesheet", "style", "scss", "sass")
	case ".js", ".jsx", ".ts", ".tsx":
		return containsAnyFold(lowerPrompt, "js", "javascript", "typescript", "script", "react", "component")
	case ".go":
		return containsAnyFold(lowerPrompt, "go", "golang")
	case ".py":
		return containsAnyFold(lowerPrompt, "python", "py")
	case ".rs":
		return containsAnyFold(lowerPrompt, "rust")
	case ".java":
		return containsAnyFold(lowerPrompt, "java")
	case ".rb":
		return containsAnyFold(lowerPrompt, "ruby")
	case ".php":
		return containsAnyFold(lowerPrompt, "php")
	case ".md":
		return containsAnyFold(lowerPrompt, "markdown", "readme", "docs", "documentation")
	}
	return false
}

func containsAnyFold(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// technologyLabel renders a human-readable technology name for the evidence
// record.
func technologyLabel(base, ext string) string {
	if ext == "" {
		return base
	}
	return strings.TrimPrefix(ext, ".")
}
