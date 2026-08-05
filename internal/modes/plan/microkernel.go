package plan

import (
	"errors"
	"fmt"
	"strings"

	stdctx "context"

	ectx "github.com/PizenLabs/izen/pkg/engine/context"
	eintent "github.com/PizenLabs/izen/pkg/engine/intent"
	eplan "github.com/PizenLabs/izen/pkg/engine/plan"
	estrate "github.com/PizenLabs/izen/pkg/engine/strategy"
)

// MicrokernelPlanner is the bridge between the immutable microkernel engine
// (pkg/engine/{intent,context,strategy,plan}) and the TUI Task view-model.
//
// It runs the full microkernel pipeline against a raw prompt:
//
//	intent.Classify → context.Collect → strategy.DetermineGoal →
//	planner.Derive → Normalize → Validate → PolicyEngine.Evaluate → Lower
//	→ ExecutionPreconditions
//
// and converts the resulting ExecutablePlan into []Task so the TUI staged
// plan rendering (TODO checklist, build ledger) works unchanged. Greenfield
// generation prompts therefore render explicit CREATE/WRITE file targets
// (index.html, styles.css, script.js) instead of the legacy heuristic
// fallback ("Apply the plan derived from model reasoning").
type MicrokernelPlanner struct {
	rootPath string
	strategy estrate.PlanningStrategy
}

// NewMicrokernelPlanner returns a planner rooted at workspace. It uses the
// deterministic greenfield strategy, so goal derivation never depends on an
// LLM round-trip.
func NewMicrokernelPlanner(rootPath string) *MicrokernelPlanner {
	return &MicrokernelPlanner{
		rootPath: rootPath,
		strategy: estrate.NewGreenfieldWebStrategy(),
	}
}

// RootPath returns the workspace the planner is bound to.
func (p *MicrokernelPlanner) RootPath() string { return p.rootPath }

// TryPlan classifies each candidate prompt in order and runs the full
// microkernel pipeline on the first one the strategy can handle. The bool
// reports whether the microkernel took ownership of the request:
//
//   - false, nil → the request is not a microkernel concern; the caller must
//     fall back to the legacy pipeline.
//   - true, tasks → the ExecutablePlan was converted into executable tasks.
//   - true, error → the pipeline produced a Goal but a stage rejected it
//     (PolicyEngine or ExecutionPreconditions). The error message carries the
//     explicit rejection reason and is safe to surface in the TUI status bar.
func (p *MicrokernelPlanner) TryPlan(ctx stdctx.Context, candidates ...string) ([]Task, bool, error) {
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

// tryOne runs the pipeline for a single candidate. It returns
// (nil, false, nil) when the strategy is not applicable so TryPlan can move
// to the next candidate.
func (p *MicrokernelPlanner) tryOne(ctx stdctx.Context, prompt string) ([]Task, bool, error) {
	in := eintent.Classify(prompt)

	collector := ectx.NewCollector()
	collector.Register(ectx.ProviderPrompt, ectx.NewPromptProvider(prompt))
	collector.Register(ectx.ProviderFilesystem, ectx.NewFilesystemProvider(p.rootPath, 0, true))
	collector.Register(ectx.ProviderEnvironment, ectx.NewEnvironmentProvider())
	collector.Register(ectx.ProviderRepository, ectx.NewRepositoryProvider(p.rootPath))
	pc, err := collector.Collect(ctx)
	if err != nil {
		// Context cancelled or zero providers — let the legacy path own it.
		return nil, false, nil //nolint:nilerr
	}

	goal, err := p.strategy.DetermineGoal(in, pc)
	if errors.Is(err, estrate.ErrStrategyNotApplicable) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("microkernel strategy %q failed: %w", p.strategy.Name(), err)
	}

	// ── Immutable plan pipeline ─────────────────────────────────────────
	lp, err := eplan.NewPlanner().Derive(goal)
	if err != nil {
		return nil, true, fmt.Errorf("microkernel plan derivation failed: %w", err)
	}
	np, err := eplan.NewPlanNormalizer().Normalize(lp)
	if err != nil {
		return nil, true, fmt.Errorf("microkernel plan normalization failed: %w", err)
	}
	vp, err := eplan.NewPlanValidator().Validate(np)
	if err != nil {
		return nil, true, fmt.Errorf("microkernel plan validation failed: %w", err)
	}
	if !vp.Valid() {
		return nil, true, fmt.Errorf("microkernel plan rejected by validation: %s", joinFailedValidation(vp))
	}

	decision := eplan.NewPolicyEngine(eplan.NewDefaultPolicy(p.rootPath)).Evaluate(vp)
	if !decision.Approved() {
		return nil, true, fmt.Errorf("microkernel plan rejected by policy: %s", summarizeDecision(decision))
	}

	ep, err := eplan.NewPlanLowerer(p.rootPath).Lower(vp)
	if err != nil {
		return nil, true, fmt.Errorf("microkernel plan lowering failed: %w", err)
	}

	report, err := eplan.NewExecutionPreconditions(p.rootPath).Check(ep)
	if err != nil {
		return nil, true, fmt.Errorf("microkernel preconditions check failed: %w", err)
	}
	if !report.Ready() {
		return nil, true, fmt.Errorf("microkernel plan blocked by execution preconditions: %s", summarizeReport(report))
	}

	tasks := convertExecutableToTasks(ep)
	if len(tasks) == 0 {
		return nil, true, fmt.Errorf("microkernel plan produced no executable tasks")
	}
	return tasks, true, nil
}

// convertExecutableToTasks maps an ExecutablePlan onto the TUI Task
// view-model. CREATE/WRITE steps become FILE_MUTATE tasks whose target and
// description name the concrete file; RUN/VERIFY steps become SHELL_EXEC
// tasks. Tasks are marked hardcoded so deterministic microkernel targets
// survive evidence-based filters (a greenfield CREATE target does not yet
// exist on disk). Read steps are not execution tasks and are dropped.
func convertExecutableToTasks(ep *eplan.ExecutablePlan) []Task {
	var out []Task
	for _, es := range ep.Steps() {
		s := es.Step()
		var verb string
		switch s.Kind() {
		case eplan.StepCreate:
			verb = "CREATE"
		case eplan.StepModify:
			verb = "WRITE"
		case eplan.StepDelete:
			verb = "DELETE"
		case eplan.StepRead:
			continue
		case eplan.StepRun:
			out = append(out, Task{
				StepNum:     len(out) + 1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      s.Target(),
				Description: "RUN " + s.Target(),
				Rationale:   s.Reason(),
				IsHardcoded: true,
			})
			continue
		case eplan.StepVerify:
			out = append(out, Task{
				StepNum:     len(out) + 1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      s.Target(),
				Description: "VERIFY " + strings.TrimSpace(s.Target()),
				Rationale:   s.Reason(),
				IsHardcoded: true,
			})
			continue
		default:
			continue
		}
		out = append(out, Task{
			StepNum:     len(out) + 1,
			IsDone:      false,
			Status:      "idle",
			Type:        "FILE_MUTATE",
			Target:      s.Target(),
			Description: verb + " " + s.Target(),
			Rationale:   s.Reason(),
			IsHardcoded: true,
		})
	}
	return out
}

// joinFailedValidation renders the failing validation rules.
func joinFailedValidation(vp *eplan.ValidatedPlan) string {
	results := vp.FailedResults()
	if len(results) == 0 {
		return "plan failed validation"
	}
	var parts []string
	for _, r := range results {
		parts = append(parts, r.Rule+": "+r.Detail)
	}
	return strings.Join(parts, "; ")
}

// summarizeDecision renders the policy decision's explicit rejection reason.
func summarizeDecision(d eplan.PolicyDecision) string {
	if d.Approved() {
		return "approved"
	}
	var parts []string
	for _, v := range d.Violations() {
		if v.Target != "" {
			parts = append(parts, fmt.Sprintf("%s on %q — %s", v.Rule, v.Target, v.Reason))
		} else {
			parts = append(parts, fmt.Sprintf("%s — %s", v.Rule, v.Reason))
		}
	}
	if len(parts) == 0 {
		return "not approved"
	}
	return strings.Join(parts, "; ")
}

// summarizeReport renders the preconditions report's fatal failures.
func summarizeReport(r *eplan.PreconditionReport) string {
	failed := r.Failed()
	if len(failed) == 0 {
		return "ready"
	}
	var parts []string
	for _, c := range failed {
		parts = append(parts, fmt.Sprintf("%s on %s — %s", c.Name, c.StepID, c.Detail))
	}
	return strings.Join(parts, "; ")
}
