package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/prompt"
)

// ProviderFunc defines a structured function signature matching the ai.Request format.
type ProviderFunc func(ctx context.Context, req ai.Request) (*ai.Response, error)

// Engine is the core interface for the plan module, coordinating between data store,
// parser, and AI provider to process plans.
type Engine struct {
	store    *PlanStore
	parser   func(string) []Task
	provider ProviderFunc
}

// NewEngine creates a new Engine instance with the provided components.
// Default parser is ParseJSONPlan — falls back to ParseMarkdownToTasks for legacy plans.
func NewEngine(store *PlanStore) *Engine {
	return &Engine{
		store:    store,
		parser:   parsePlanContent,
		provider: nil,
	}
}

// parsePlanContent enforces strict JSON schema with recovery.
// Phase 3: If JSON parsing fails, it attempts auto-repair via autoCloseJSON
// and retries before giving up. Markdown-only output is rejected.
func parsePlanContent(content string) []Task {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	result := ParseJSONPlan(content)
	if result.Valid {
		if err := ValidateAllTasks(result.Tasks); err != nil {
			return nil
		}
		return result.Tasks
	}

	// Phase 3: Attempt auto-repair of truncated JSON before giving up.
	repaired := autoCloseJSON(content)
	if repaired != content {
		result = ParseJSONPlan(repaired)
		if result.Valid {
			if err := ValidateAllTasks(result.Tasks); err != nil {
				return nil
			}
			return result.Tasks
		}
	}

	return nil
}

// SetProvider configures the AI provider for this engine using the structured signature.
func (e *Engine) SetProvider(provider ProviderFunc) {
	if e != nil {
		e.provider = provider
	}
}

// ProcessFromLedger generates an execution plan directly from investigation
// ledger data using enforced structured output (JSON mode). Returns parsed
// Task structs, bypassing the conversational text-streaming path entirely.
//
// When fastTrack is true (used for local 7B models on a 0-TODO + compile/dep
// blocker), the heavy JSON-schema instruction and full forensic ledger prompt
// are replaced with a minimal shell-resolution prompt so the model can produce
// its first token within a tight local budget instead of choking on context.
func (e *Engine) ProcessFromLedger(ctx context.Context, ledgerContent string, problem string, modelName string) ([]Task, error) {
	return e.processFromLedger(ctx, ledgerContent, problem, modelName, false)
}

// ProcessFromLedgerFastTrack is the lightweight variant used for local SLMs that
// hit a 0-TODO + dependency/compilation blocker. It skips the JSON-schema system
// prompt and the full forensic ledger prompt in favour of a minimal resolution
// prompt, keeping the prompt tiny enough for a 7B model to answer quickly.
func (e *Engine) ProcessFromLedgerFastTrack(ctx context.Context, promptText string, modelName string) ([]Task, error) {
	return e.processFromLedger(ctx, "", "", modelName, true, promptText)
}

func (e *Engine) processFromLedger(ctx context.Context, ledgerContent string, problem string, modelName string, fastTrack bool, fastPrompt ...string) ([]Task, error) {
	if e == nil || e.provider == nil {
		return nil, fmt.Errorf("plan engine: provider not set")
	}

	var req ai.Request
	if fastTrack && len(fastPrompt) > 0 {
		req = ai.Request{
			Model: modelName,
			Messages: []ai.Message{
				{
					Role:    "system",
					Content: prompt.PlanSystemPrompt(),
				},
				{
					Role:    "user",
					Content: fastPrompt[0],
				},
			},
			Stream: false,
		}
	} else {
		// Extract the investigation conclusion so it can be injected as a
		// high-priority override signal. The conclusion carries the resolved
		// diagnosis (e.g. corrected dependency paths) that must take precedence
		// over raw error text when synthesising shell tasks.
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		req = ai.Request{
			Model: modelName,
			Messages: []ai.Message{
				{
					Role:    "system",
					Content: prompt.PlanSystemPrompt() + "\n\n" + SchemaJSONInstruction(),
				},
				{
					Role:    "user",
					Content: prompt.BuildPlanJSONPrompt(problem, ledgerContent, conclusion),
				},
			},
			Stream: false,
			ResponseFormat: &ai.ResponseFormat{
				Type: "json_object",
			},
		}
	}

	resp, err := e.provider(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan engine: provider call failed: %w", err)
	}

	if resp == nil || resp.Content == "" {
		return nil, fmt.Errorf("plan engine: empty response from provider")
	}

	// Persist raw plan output to disk.
	_ = e.store.SaveRawMarkdown("plan", resp.Content)

	if fastTrack && len(fastPrompt) > 0 {
		// Fast-track: the model returns a minimal markdown shell checklist. A
		// local 7B model may still emit the occasional placeholder/non-shell
		// task; rather than hard-aborting the whole plan, we keep only the valid
		// SHELL_EXEC tasks (placeholder FILE_MUTATE lines are dropped). If nothing
		// usable survives, we surface a clear fallback instead of a build abort.
		raw := ParseMarkdownToTasks(resp.Content)
		clean := make([]Task, 0, len(raw))
		for _, t := range raw {
			if t.Type == "SHELL_EXEC" && strings.TrimSpace(t.Target) != "" {
				clean = append(clean, t)
			}
		}
		if len(clean) == 0 {
			return nil, fmt.Errorf("plan engine: fast-track produced no runnable shell tasks (model returned: %s)", truncateForLog(resp.Content))
		}
		return clean, nil
	}

	// Parse structured JSON output into tasks.
	jsonResult := ParseJSONPlan(resp.Content)
	if jsonResult.Valid && len(jsonResult.Tasks) > 0 {
		// Local 7B SLMs frequently emit tasks with empty targets under context
		// pressure. Rather than hard-aborting the whole plan, filter out invalid
		// tasks and keep only the runnable ones — identical to the fast-track
		// resilience pattern below. If nothing valid survives, fall through to
		// the compile/dep fallback or error path.
		if err := ValidateAllTasks(jsonResult.Tasks); err != nil {
			clean := filterValidTasks(jsonResult.Tasks)
			if len(clean) > 0 {
				return ForceShellExecOnCompileError(clean, problem, ledgerContent), nil
			}
		} else {
			return ForceShellExecOnCompileError(jsonResult.Tasks, problem, ledgerContent), nil
		}
	}

	// ── PHASE 3: JSON RECOVERY — SELF-CORRECT, DO NOT LOOP BACK ────
	// Instead of forcing the user back to /investigate (which creates a
	// redundant 2-turn loop), attempt self-correction strategies in order:
	//   1. Auto-repair truncated JSON (autoCloseJSON already applied in schema.go)
	//   2. Retry with truncated context (smaller prompt for 7B models)
	//   3. Fall back to last valid ledger state with minimal shell prompt
	//   4. Only if all recovery fails, return a structured error (no /investigate redirect)

	// Strategy 1: Auto-repair was already attempted in ParseJSONPlan/sanitizeJSONContent.
	// If we reach here it failed — proceed to Strategy 2.

	// Strategy 2: Retry with a minimal prompt, stripping the full ledger context.
	// This works around context drift on 7B models by reducing token pressure.
	compileErr := IsCompilationOrDependencyError(problem) || IsCompilationOrDependencyError(ledgerContent)
	if compileErr {
		coreErr := CoreErrorLine(problem)
		if coreErr == "" {
			coreErr = CoreErrorLine(ledgerContent)
		}
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		retry, retryErr := e.ProcessFromLedgerFastTrack(ctx, FastTrackPrompt(coreErr, conclusion), modelName)
		if retryErr == nil && len(retry) > 0 {
			return retry, nil
		}
	}

	// Strategy 3: Fall back to a SHELL_EXEC recovery task from the conclusion
	// WITHOUT asking the model to generate JSON — extract directly from the ledger.
	if compileErr {
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		if dep := dependencyFromConclusion(conclusion); dep != "" {
			recovery := Task{
				StepNum:     1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      "go get " + dep,
				Description: "Install missing dependency (recovered from ledger conclusion)",
			}
			return []Task{recovery}, nil
		}
		// Fallback to go mod tidy as the safest generic recovery
		recovery := Task{
			StepNum:     1,
			IsDone:      false,
			Status:      "idle",
			Type:        "SHELL_EXEC",
			Target:      "go mod tidy",
			Description: "Resolve dependency blocker via module tooling",
		}
		return []Task{recovery}, nil
	}

	jsonErr := jsonResult.Error
	if jsonErr == "" {
		jsonErr = fmt.Sprintf("empty error field (provider response: %s)", truncateForLog(resp.Content))
	}
	return nil, fmt.Errorf("plan engine: JSON parse failed after recovery attempts (%s). "+
		"Response preview: %s", jsonErr, truncateForLog(resp.Content))
}

// filterValidTasks filters a task slice to only tasks with valid, non-empty
// targets. Invalid tasks are dropped silently — identical resilience pattern
// used by the fast-track path — so a local 7B model with one bad task does
// not abort the entire plan. Returns the original slice if all tasks are valid.
func filterValidTasks(tasks []Task) []Task {
	clean := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		isValid, _ := ValidateTaskTarget(t.Target, t.Type)
		if isValid {
			clean = append(clean, t)
		}
	}
	return clean
}

// ForceShellExecOnCompileError enforces the IZEN /plan anti-escape law for
// compilation or dependency failures: when the root cause is a build/dep error,
// the plan MUST resolve it through go.mod / SHELL_EXEC (e.g. `go get`,
// `go mod tidy`) — NEVER by patching documentation or unrelated source files.
//
// If the synthesized tasks already contain a SHELL_EXEC task, they are returned
// unchanged (the model complied). Otherwise a deterministic SHELL_EXEC recovery
// task is prepended so the build engine always has a runnable shell step to
// clear the blocker instead of stalling or escaping into README.md.
func ForceShellExecOnCompileError(tasks []Task, problem, ledgerContent string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	if !IsCompilationOrDependencyError(problem) && !IsCompilationOrDependencyError(ledgerContent) {
		return tasks
	}
	for _, t := range tasks {
		if t.Type == "SHELL_EXEC" && strings.TrimSpace(t.Target) != "" {
			return tasks
		}
	}

	// No shell task present → prepend a deterministic dependency-resolution
	// SHELL_EXEC. Prefer the corrected dependency path from the investigation
	// conclusion when available; otherwise fall back to `go mod tidy`.
	cmd := "go mod tidy"
	if conclusion := ExtractConclusionFromLedger(ledgerContent); conclusion != "" {
		if dep := dependencyFromConclusion(conclusion); dep != "" {
			cmd = "go get " + dep
		}
	}
	recovery := Task{
		StepNum:     0,
		IsDone:      false,
		Status:      "idle",
		Type:        "SHELL_EXEC",
		Target:      cmd,
		Description: "Resolve compilation/dependency blocker via module tooling (forced by /plan anti-escape law)",
	}
	out := make([]Task, 0, len(tasks)+1)
	out = append(out, recovery)
	out = append(out, tasks...)
	for i := range out {
		out[i].StepNum = i + 1
	}
	return out
}

// dependencyFromConclusion extracts a plausible module path from an
// investigation conclusion string (e.g. "use github.com/moby/moby/client").
// It returns the first token that looks like a Go module path; empty otherwise.
func dependencyFromConclusion(conclusion string) string {
	for _, tok := range strings.Fields(conclusion) {
		t := strings.TrimRight(strings.TrimLeft(tok, "\"'"), "\"'.,")
		if strings.Contains(t, ".") && (strings.Contains(t, "/") || strings.HasPrefix(t, "github.com") || strings.HasPrefix(t, "golang.org")) {
			return t
		}
	}
	return ""
}

// truncateForLog caps a model response excerpt so error messages stay readable.
func truncateForLog(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// ProcessPlan generates an execution plan by dispatching to the AI provider
// with strict JSON output enforcement.
func (e *Engine) ProcessPlan(ctx context.Context, modelName string, objective string, contextStr string) error {
	if e == nil || e.provider == nil {
		return nil
	}

	req := ai.Request{
		Model: modelName,
		Messages: []ai.Message{
			{
				Role:    "system",
				Content: prompt.PlanSystemPrompt(),
			},
			{
				Role:    "user",
				Content: prompt.BuildPlanPrompt(objective, contextStr),
			},
		},
		Stream: false,
	}

	resp, err := e.provider(ctx, req)
	if err != nil {
		return err
	}

	return e.store.SaveRawMarkdown("plan", resp.Content)
}

// Parse parses plan content (JSON or markdown) into tasks.
func (e *Engine) Parse(content string) []Task {
	return e.parser(content)
}

// ParseJSON parses JSON plan content specifically.
func (e *Engine) ParseJSON(content string) (*PlanOutput, error) {
	result := ParseJSONPlan(content)
	if !result.Valid {
		return nil, &PlanSchemaError{Message: result.Error}
	}
	return result.Plan, nil
}

// Store returns the underlying PlanStore for direct access.
func (e *Engine) Store() *PlanStore {
	return e.store
}

// TickTask marks the N-th task as complete in the current plan file.
func (e *Engine) TickTask(stepNum int) error {
	return e.store.TickTaskHoanThanh(stepNum)
}

// PlanSchemaError indicates a plan output schema violation.
type PlanSchemaError struct {
	Message string
}

func (e *PlanSchemaError) Error() string {
	return "plan output schema violation: " + e.Message
}
