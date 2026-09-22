package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/discovery/recon"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/llmstep"
)

// ── BOUNDED REASONING STEP CONTRACT ─────────────────────────────────────────
//
// A bounded reasoning step is a single LLM invocation whose output budget and
// scope are derived from the provider's ACTUAL capability (its max output
// ceiling), not from a global constant. When the provider cuts the response
// off at its ceiling (finish_reason="length" → OUTPUT_EXHAUSTED), the runtime
// does NOT re-run the same full-scope prompt: it commits only validated atomic
// results and schedules a smaller bounded continuation step that ADVANCES the
// durable state (a compact summary of committed tasks), never replaying the
// full transcript or the same mode/scene.
//
//   - StepIncomplete (no validated atomic result) → NO STATE COMMIT → reschedule
//     a smaller bounded step, or fail with a typed OUTPUT_EXHAUSTED error when
//     the shrink floor is reached.
//   - Validated atomic results before exhaustion → atomic state commit; the
//     plan continues with the accumulated state.
//   - Request budget (maxContinuationSteps) bounds the whole sequence; budget
//     recalculation is telemetry-visible between steps.
//
// Every transition is published as a reasoning.step.* / reasoning.state.* /
// reasoning.continuation.* / reasoning.budget.recalculated domain event so
// projections can distinguish OUTPUT_EXHAUSTED from a failed task without
// parsing free-form log strings.

const (
	// defaultMaxTokenRequest is the plan-synthesis output budget REQUESTED for
	// unconstrained models. It is never a hard invariant: llmstep.ResolveMaxTokens
	// clamps it against the provider's real ceiling (constrained/free-tier models
	// are capped at ~980).
	planSynthesisRequestedMaxTokens = 1536

	// initialStepTaskBudget is the maximum number of atomic tasks a constrained
	// model is asked to emit per bounded step. It is the starting point of the
	// adaptive granularity curve (shrinks on STEP_INCOMPLETE).
	initialStepTaskBudget = 4

	// minStepTaskBudget is the shrink floor: at this granularity the model is
	// only trusted with an atomic single-task batch per bounded step.
	minStepTaskBudget = 1

	// stepTaskBudgetShrink is the adaptive granularity decrement applied after
	// a bounded step commits NO state (STEP_INCOMPLETE → smaller scope).
	stepTaskBudgetShrink = 2

	// defaultMaxContinuationSteps is the request budget for a plan synthesis:
	// the maximum number of bounded continuation steps scheduled after the
	// initial OUTPUT_EXHAUSTED step. It lives on the shared llmstep primitive;
	// this alias keeps the plan contract self-documenting.
	defaultMaxContinuationSteps = llmstep.DefaultMaxContinuationSteps
)

// resolveSynthesisMaxTokens derives the maximal safe output budget for plan
// synthesis against a specific model. It delegates to the SHARED bounded-step
// budget resolution (llmstep.ResolveMaxTokens) so the executor, ask and plan
// paths never ship a separate model-output-budget resolver. The second return
// reports whether the model was classified as constrained.
func resolveSynthesisMaxTokens(modelName string, requested int) (maxTokens int, constrained bool) {
	return llmstep.ResolveMaxTokens(modelName, requested)
}

// synthesisStepState is the durable, bounded continuity state of one plan
// synthesis run. It preserves the task identity (the active model), the
// authority (the staged, validated tasks), the adaptive step budget, the
// remaining request budget, and the committed atomic results — so each
// continuation step advances work instead of replaying it. The ordinal /
// request-budget lifecycle is owned by the shared llmstep.StepState; the
// plan-specific adaptive task-budget and the staged task ledger stay here.
type synthesisStepState struct {
	*stepCore
	taskBudget      int
	staged          []Task
	baseUserContent string
}

// stepCore is the shared bounded-step lifecycle core of a plan synthesis.
type stepCore struct{ ss *llmstep.StepState }

func newSynthesisStepState(modelName string, constrained bool, maxTokens int) *synthesisStepState {
	tb := 0
	if constrained {
		tb = initialStepTaskBudget
	}
	return &synthesisStepState{
		stepCore:   &stepCore{ss: llmstep.NewStepState(modelName, constrained, maxTokens, defaultMaxContinuationSteps)},
		taskBudget: tb,
	}
}

// modelName exposes the bound model for telemetry.
func (s *synthesisStepState) modelName() string { return s.ss.Model() }

// stepOrdinal exposes the current 1-based bounded-step ordinal for telemetry.
func (s *synthesisStepState) stepOrdinal() int { return s.ss.Ordinal() }

// canContinue reports whether the request budget admits another continuation.
func (s *synthesisStepState) canContinue() bool { return s.ss.CanContinue() }

// continuationsLeft returns the remaining request-budget steps.
func (s *synthesisStepState) continuationsLeft() int { return s.ss.ContinuationsLeft() }

// recordContinuation advances the step ordinal and request-budget counter.
func (s *synthesisStepState) recordContinuation() { s.ss.Advance() }

// maxOutputTokens exposes the capability-aware per-step output budget for
// telemetry emission.
func (s *synthesisStepState) maxOutputTokens() int { return s.ss.MaxTokens() }

// shrinkBudget applies the adaptive granularity decrement and reports whether
// the floor was not yet reached. A false return means the step cannot be made
// any smaller and the synthesis must fail with a typed OUTPUT_EXHAUSTED error.
func (s *synthesisStepState) shrinkBudget() bool {
	if s.taskBudget <= minStepTaskBudget {
		return false
	}
	s.taskBudget -= stepTaskBudgetShrink
	if s.taskBudget < minStepTaskBudget {
		s.taskBudget = minStepTaskBudget
	}
	return true
}

// boundedOutputInstruction is appended to the system prompt of a constrained
// model so a full plan batch fits inside the provider's output ceiling instead
// of being cut off mid-JSON. Only applied when the model is capability-locked.
func boundedOutputInstruction(taskBudget int) string {
	return fmt.Sprintf(`
[SYSTEM: BOUNDED OUTPUT CONTRACT]
Your provider enforces a strict output-token ceiling. Output at most %d atomic_task entries per response. Keep every string value (description, rationale, solution) under 15 words. Do not pad, do not expand, do not emit prose.`, taskBudget)
}

// stagedSummary builds the compact durable-state summary injected into the
// next bounded step. It carries ONLY the validated committed work — file
// targets + counts — never the full transcript or the whole ledger.
func stagedSummary(staged []Task) string {
	if len(staged) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(staged))
	for _, t := range staged {
		target := strings.TrimSpace(t.Target)
		if target == "" {
			target = strings.TrimSpace(t.Description)
		}
		if n := len(target); n > 40 {
			target = target[:40] + "…"
		}
		parts = append(parts, target)
	}
	return strings.Join(parts, ", ")
}

// boundedContinuationAppend rebuilds the user turn for a continuation step: the
// ORIGINAL bounded prompt plus a compact instruction that the previous output
// was exhausted, that N validated tasks are already committed (never repeat
// them), and that THIS step must emit at most taskBudget remaining tasks. It
// is rebuilt from baseUserContent each time so a long continuation cannot
// accumulate duplicate instruction blocks.
func boundedContinuationAppend(base string, staged []Task, taskBudget int) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n[SYSTEM: OUTPUT BUDGET EXHAUSTED — BOUNDED CONTINUATION]\n")
	b.WriteString("The previous response was cut off at the provider's output ceiling (finish_reason=length); it was NOT a complete plan.\n")
	fmt.Fprintf(&b, "Already staged and committed (%d validated atomic task(s)) — DO NOT repeat them: %s\n", len(staged), stagedSummary(staged))
	fmt.Fprintf(&b, "Generate ONLY the REMAINING atomic_tasks for the SAME plan. Output at most %d atomic_task entries in this response.\n", taskBudget)
	b.WriteString("Keep every string value under 15 words and context_anchor/architectural_strategy minimal so the whole JSON fits the output ceiling.")
	return b.String()
}

// groundCandidateTasks applies the same evidence-based candidate cleanup the
// canonical synthesis path applies (alignment to compiler-error files,
// unsolicited pkg-file filtering, undefined-symbol shell-exec stripping,
// non-existent target rejection, archetype domain isolation), so a bounded step
// commits only tasks that pass every gate the full retry loop enforces.
func (e *Engine) groundCandidateTasks(candidates []Task, ledgerContent string) []Task {
	if e == nil {
		return candidates
	}
	candidates = AlignFileTargetWithErrors(candidates, ledgerContent)
	candidates = FilterUnsolicitedPkgFiles(candidates, ledgerContent)
	candidates = FilterUndefinedSymbolShellExec(candidates, ledgerContent)
	candidates = FilterNonExistentMutationTargets(candidates, e.rootPath)
	if e != nil && e.vanillaWeb {
		candidates = EnforceFrontendDomainIsolation(candidates)
		candidates = SanitizeTasksForArchetype(candidates, recon.VANILLA_WEB)
	}
	return candidates
}

// salvageValidTasks extracts validated atomic tasks from a truncated step
// buffer (finish_reason=length). It is the ONLY commit gate for partial output:
// a response may contain an independently valid atomic result before exhaustion
// (a JSON plan that survived via tolerant parsing/auto-close, or markdown task
// blocks) and that validated result is committed; anything unvalidated is never
// committed. Returns nil when nothing validated exists (STEP_INCOMPLETE).
func (e *Engine) salvageValidTasks(content, problem, ledgerContent string) []Task {
	if e == nil {
		return nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// Tolerant markdown blocks: Mini/free models emit "- [ ] TASK" lines even
	// when the JSON was cut off. Accept them through the same validation gates.
	if md := ParseMarkdownToTasks(content); len(md) > 0 {
		md = filterValidTasks(md)
		md = FilterNonExistentMutationTargets(md, e.rootPath)
		if e.vanillaWeb {
			md = EnforceFrontendDomainIsolation(md)
			md = SanitizeTasksForArchetype(md, recon.VANILLA_WEB)
		}
		if len(md) > 0 && !hasInvalidShellExecCommand(md) {
			return md
		}
	}

	// Full JSON plan: a truncation that closed cleanly at a task boundary
	// (tolerant sanitization/auto-close) yields a valid, complete plan with the
	// tasks emitted before exhaustion — an independently valid atomic result.
	jsonResult := ParseJSONPlan(content)
	if jsonResult.Valid && len(jsonResult.Tasks) > 0 {
		var candidates []Task
		if err := ValidateAllTasks(jsonResult.Tasks); err != nil {
			candidates = filterValidTasks(jsonResult.Tasks)
		} else {
			candidates = jsonResult.Tasks
		}
		candidates = e.groundCandidateTasks(candidates, ledgerContent)
		if len(candidates) > 0 && !hasInvalidShellExecCommand(candidates) {
			return candidates
		}
	}

	return nil
}

// mergeTaskBatches unions committed task batches, deduplicating by
// (type + target-or-description) and renumbering steps sequentially. Staged
// (validated, committed) state always precedes newly accepted tasks.
func mergeTaskBatches(lists ...[]Task) []Task {
	var out []Task
	seen := make(map[string]bool)
	for _, batch := range lists {
		for _, t := range batch {
			key := strings.ToLower(strings.TrimSpace(string(t.Type) + "|" + t.Target))
			if t.Target == "" {
				key = strings.ToLower(strings.TrimSpace(string(t.Type) + "|" + t.Description))
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			t.StepNum = len(out) + 1
			out = append(out, t)
		}
	}
	return out
}

// SynthesisErrorKind classifies why bounded plan synthesis stopped. It
// lets callers distinguish OUTPUT_EXHAUSTED / STEP_INCOMPLETE (recoverable,
// bounded-step semantics) from a permanent task failure.
type SynthesisErrorKind int

const (
	// SynthesisOK is the zero value; never returned as an error.
	SynthesisOK SynthesisErrorKind = iota
	// SynthesisOutputExhausted: the provider repeatedly cut the response at its
	// output ceiling and the shrink floor produced no validated plan state.
	SynthesisOutputExhausted
	// SynthesisStepIncomplete: a bounded step produced no validated atomic
	// result and no state was committed.
	SynthesisStepIncomplete
	// SynthesisNoProgress: a continuation advanced no new validated state.
	SynthesisNoProgress
	// SynthesisContinuationBudgetExhausted: the request budget was consumed
	// before the plan completed.
	SynthesisContinuationBudgetExhausted
)

func (k SynthesisErrorKind) String() string {
	switch k {
	case SynthesisOutputExhausted:
		return "OUTPUT_EXHAUSTED"
	case SynthesisStepIncomplete:
		return "STEP_INCOMPLETE"
	case SynthesisNoProgress:
		return "NO_PROGRESS"
	case SynthesisContinuationBudgetExhausted:
		return "CONTINUATION_REQUIRED"
	default:
		return "OK"
	}
}

// SynthesisError is the typed error for bounded-step plan synthesis outcomes.
type SynthesisError struct {
	Kind SynthesisErrorKind
	Step int
	Hint string
}

func (e *SynthesisError) Error() string {
	msg := fmt.Sprintf("plan synthesis bounded step failed: %s (step %d)", e.Kind, e.Step)
	if e.Hint != "" {
		msg += ": " + e.Hint
	}
	return msg
}

func (e *SynthesisError) Unwrap() error { return nil }

// IsOutputExhausted reports whether err is a bounded-step OUTPUT_EXHAUSTED
// (or continuation-budget) outcome — a recoverable exhaustion, distinct from a
// failed task.
func IsOutputExhausted(err error) bool {
	var se *SynthesisError
	if !errors.As(err, &se) {
		return false
	}
	return se.Kind == SynthesisOutputExhausted ||
		se.Kind == SynthesisContinuationBudgetExhausted ||
		se.Kind == SynthesisNoProgress
}

// commitStepState persists the accumulated validated state as the final plan,
// or returns a typed OUTPUT_EXHAUSTED/continuation error when nothing validated
// was staged. This is the "atomic state commit" half of the bounded-step
// contract: the staged set is composed exclusively of individually validated
// atomic results.
func (e *Engine) commitStepState(step *synthesisStepState, problem, ledgerContent string) ([]Task, error) {
	if len(step.staged) == 0 {
		return nil, &SynthesisError{
			Kind: SynthesisOutputExhausted,
			Step: step.stepOrdinal(),
			Hint: "provider output ceiling exhausted and the minimal bounded step still produced no validated plan tasks; model cannot fit the plan in its output budget",
		}
	}
	e.emit(events.NewStateCommitted(step.stepOrdinal(), len(step.staged), "final.plan"))
	return e.finalizeTasks(step.staged, problem, ledgerContent), nil
}

// synthesizeBoundedContinuation completes a plan whose FIRST provider response
// was cut off at the output ceiling (finish_reason="length"). It is the bounded
// continuity driver:
//
//   - salvageValidTasks commits ONLY validated atomic results (atomic commit).
//   - No salvage → NO STATE COMMIT → the next step is rescheduled with a
//     smaller task budget (adaptive granularity), never the same full scope.
//   - Each continuation rebuilds the prompt from baseUserContent plus a compact
//     summary of committed state — it advances work instead of replaying the
//     transcript, and the whole sequence is bounded by maxSteps (request budget).
//   - A natural "stop" response is merged with the committed state (dedupe) and
//     returned as the final plan.
//   - Exhausting the request budget returns the committed validated tasks, or a
//     typed OUTPUT_EXHAUSTED error when nothing validated was staged.
func (e *Engine) synthesizeBoundedContinuation(ctx context.Context, baseReq ai.Request, exhausted *ai.Response, problem, ledgerContent string, step *synthesisStepState) ([]Task, error) {
	req := baseReq

	// Step 1: atomic commit from the exhausted first buffer.
	salvaged := e.salvageValidTasks(exhausted.Content, problem, ledgerContent)
	if len(salvaged) > 0 {
		step.staged = mergeTaskBatches(step.staged, salvaged)
		e.emit(events.NewStateCommitted(step.stepOrdinal(), len(step.staged), "step.batch"))
		_ = e.store.SaveRawMarkdown("plan", exhausted.Content) //nolint:contextcheck // substrate wrapper manages its own context
	} else {
		e.emit(events.NewStateRejected(step.stepOrdinal(), "exhausted buffer contained no validated atomic result"))
	}

	for step.canContinue() {
		// Schedule the next bounded step.
		step.recordContinuation()
		if len(salvaged) == 0 && len(step.staged) == 0 {
			// STEP_INCOMPLETE: no state was committed. Reschedule a SMALLER
			// bounded step (adaptive granularity), or fail at the shrink floor.
			prevMax := req.MaxTokens
			if !step.shrinkBudget() {
				return e.commitStepState(step, problem, ledgerContent) // fails with typed OUTPUT_EXHAUSTED
			}
			e.emit(events.NewBudgetRecalculated(step.stepOrdinal(), prevMax, req.MaxTokens,
				fmt.Sprintf("task.batch.adjusted to %d", step.taskBudget)))
		}
		e.emit(events.NewContinuationScheduled(step.stepOrdinal(), len(step.staged), step.continuationsLeft()))
		e.emit(events.NewStepStarted(step.modelName(), step.stepOrdinal(), req.MaxTokens))
		e.emit(events.NewContinuationStarted(step.stepOrdinal(), req.MaxTokens))

		req.Messages[len(req.Messages)-1].Content = boundedContinuationAppend(step.baseUserContent, step.staged, step.taskBudget)

		nextResp, err := e.complete(ctx, req)
		if err != nil || nextResp == nil || strings.TrimSpace(nextResp.Content) == "" {
			return e.commitStepState(step, problem, ledgerContent)
		}

		if nextResp.FinishReason == "length" {
			// Next bounded step also exhausted: commit validated results, keep
			// advancing, or reschedule smaller when nothing validated.
			e.emit(events.NewStepExhausted(step.stepOrdinal(), nextResp.TokenOutput, 0))
			more := e.salvageValidTasks(nextResp.Content, problem, ledgerContent)
			if len(more) == 0 {
				e.emit(events.NewStateRejected(step.stepOrdinal(), "bounded step committed no validated atomic result"))
				salvaged = nil
				continue
			}
			step.staged = mergeTaskBatches(step.staged, more)
			e.emit(events.NewStateCommitted(step.stepOrdinal(), len(step.staged), "step.batch"))
			_ = e.store.SaveRawMarkdown("plan", nextResp.Content) //nolint:contextcheck // substrate wrapper manages its own context
			salvaged = more
			continue
		}

		// Natural stop: the model had room to complete — accept its plan,
		// merged with the committed staged state.
		e.emit(events.NewStepCompleted(step.stepOrdinal(), len(step.staged), nextResp.FinishReason))
		parsed := ParseJSONPlan(cleanLLMResponse(nextResp.Content))
		if parsed.Valid && len(parsed.Tasks) > 0 {
			var cands []Task
			if err := ValidateAllTasks(parsed.Tasks); err != nil {
				cands = filterValidTasks(parsed.Tasks)
			} else {
				cands = parsed.Tasks
			}
			cands = e.groundCandidateTasks(cands, ledgerContent)
			if len(cands) > 0 && !hasInvalidShellExecCommand(cands) {
				merged := mergeTaskBatches(step.staged, cands)
				return e.finalizeTasks(merged, problem, ledgerContent), nil
			}
		}
		if md := e.tolerantMarkdownTasks(nextResp.Content, problem, ledgerContent); len(md) > 0 {
			merged := mergeTaskBatches(step.staged, md)
			return e.finalizeTasks(merged, problem, ledgerContent), nil
		}
		return e.commitStepState(step, problem, ledgerContent)
	}

	return e.commitStepState(step, problem, ledgerContent)
}
