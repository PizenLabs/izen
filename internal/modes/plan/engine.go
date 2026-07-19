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
		return ValidateShellExecCommands(clean, ledgerContent), nil
	}

	// ── JSON PARSING — ELEVATED SILENT RETRY LOOP ──────────────────
	// The loop covers the provider call, JSON code-fence stripping, structural
	// json.Unmarshal parsing, AND semantic SHELL_EXEC validation in a single
	// retry envelope. Both structural failures (truncated/malformed JSON) and
	// semantic failures (hallucinated file paths as SHELL_EXEC targets) trigger
	// an automated retry with an augmented prompt. This eliminates the manual
	// friction of /mode investigate ↔ /mode plan toggling by handling the
	// correction transparently.
	maxSilentRetries := 2
	for attempt := 0; attempt <= maxSilentRetries; attempt++ {
		// On retry (attempt > 0), re-invoke the provider with an augmented
		// prompt that includes the strict enforcement instruction from the
		// previous rejection.
		if attempt > 0 {
			fmt.Printf("[plan-engine] JSON syntax or command schema broken. Refining prompt and retrying internally (Attempt %d/%d)...\n", attempt, maxSilentRetries)
			req.Messages[len(req.Messages)-1].Content += shellExecReinforcement(attempt, maxSilentRetries)
			var retryErr error
			resp, retryErr = e.provider(ctx, req)
			if retryErr != nil || resp == nil || resp.Content == "" {
				continue
			}
			_ = e.store.SaveRawMarkdown("plan", resp.Content)
		}

		jsonResult := ParseJSONPlan(resp.Content)

		if jsonResult.Valid && len(jsonResult.Tasks) > 0 {
			var candidates []Task
			if err := ValidateAllTasks(jsonResult.Tasks); err != nil {
				candidates = filterValidTasks(jsonResult.Tasks)
			} else {
				candidates = jsonResult.Tasks
			}

			if len(candidates) > 0 {
				if !hasInvalidShellExecCommand(candidates) {
					// All checks passed — return with compile-error enforcement.
					return ForceShellExecOnCompileError(candidates, problem, ledgerContent), nil
				}

				// Semantic failure: invalid SHELL_EXEC commands detected.
				if attempt < maxSilentRetries {
					continue
				}

				// Max retries exceeded for semantic failures — deterministic fallback.
				return ValidateShellExecCommands(
					ForceShellExecOnCompileError(candidates, problem, ledgerContent),
					ledgerContent,
				), nil
			}
		}

		// Structural parse failure or all candidates filtered out.
		if attempt < maxSilentRetries {
			continue
		}
	}

	// ── EMERGENCY DETERMINISTIC FALLBACK ──────────────────────────
	// All 3 LLM synthesis attempts (initial + 2 retries) failed to produce a
	// valid JSON plan. Build a deterministic go get / go mod tidy task from
	// the forensic ledger conclusion instead of returning a hard error or
	// forcing a manual /investigate loop.
	if IsCompilationOrDependencyError(problem) || IsCompilationOrDependencyError(ledgerContent) {
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		if dep := dependencyFromConclusion(conclusion); dep != "" {
			return []Task{
				{
					StepNum:     1,
					IsDone:      false,
					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      "go get " + dep,
					Description: "Emergency fallback: all LLM synthesis attempts exhausted",
				},
			}, nil
		}
		return []Task{
			{
				StepNum:     1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      "go mod tidy",
				Description: "Emergency fallback: all LLM synthesis attempts exhausted",
			},
		}, nil
	}

	// ── ABSOLUTE FALLBACK ENFORCER (No Turning Back) ─────────────
	// If we exhausted all silent retries yet the ledger still carries a raw
	// *.go file path with a parsing/import error indicator, we MUST NOT bubble
	// a hard failure up to the UI that forces the user to re-run /investigate
	// manually. A raw compile error on a source file is, by definition, a
	// module/environment discrepancy — assume it and stage `go mod tidy`
	// deterministically, with a warning logged for traceability.
	if hasGoFileParseError(ledgerContent) || hasGoFileParseError(problem) {
		fmt.Printf("[plan-fallback] Truncated dependency match hit. Injecting deterministic go mod tidy task.\n")
		return []Task{
			{
				StepNum:     1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      "go mod tidy",
				Description: "Emergency fallback: all LLM synthesis attempts exhausted; raw *.go parse/import error detected",
			},
		}, nil
	}

	return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts failed and no dependency error detected", maxSilentRetries+1)
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

// knownShellBinaries is the set of recognised executable binaries that a
// SHELL_EXEC target may legitimately start with. Any first token outside this
// set — especially bare file paths like "go.mod" or "relative/path/to/go.mod" —
// is treated as a hallucinated command and triggers the deterministic fallback
// in ValidateShellExecCommands.
var knownShellBinaries = map[string]bool{
	"go":             true,
	"git":            true,
	"make":           true,
	"npm":            true,
	"npx":            true,
	"yarn":           true,
	"pip":            true,
	"pip3":           true,
	"cargo":          true,
	"brew":           true,
	"docker":         true,
	"docker-compose": true,
	"cd":             true,
	"mkdir":          true,
	"cp":             true,
	"mv":             true,
	"rm":             true,
	"touch":          true,
	"echo":           true,
	"cat":            true,
	"curl":           true,
	"wget":           true,
	"chmod":          true,
	"chown":          true,
	"python":         true,
	"python3":        true,
	"node":           true,
	"deno":           true,
	"bun":            true,
	"ls":             true,
	"grep":           true,
	"rg":             true,
	"sed":            true,
	"awk":            true,
	"find":           true,
	"sort":           true,
	"tee":            true,
	"ln":             true,
	"source":         true,
	"export":         true,
	"sudo":           true,
	"bash":           true,
	"sh":             true,
	"zsh":            true,
	"terraform":      true,
	"tofu":           true,
	"kubectl":        true,
	"helm":           true,
	"go.mod":         false, // explicitly NOT a valid binary
	"go.sum":         false, // explicitly NOT a valid binary
}

// isValidShellCommand checks whether a SHELL_EXEC target is a valid runnable
// command rather than a hallucinated file path or placeholder text. A command
// is valid when its first token is a known binary and it is not a bare file
// path (e.g. "relative/path/to/go.mod", "./go.mod", or "go.mod" as a bare
// command).
func isValidShellCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Forbid bare file paths ending in .mod or .sum.
	if strings.HasSuffix(cmd, ".mod") || strings.HasSuffix(cmd, ".sum") {
		return false
	}
	first := strings.Fields(cmd)[0]
	// Forbid relative/absolute paths as the command token.
	if strings.Contains(first, "/") {
		return false
	}
	// Forbid bare go.mod/go.sum invoked as a command.
	if first == "go.mod" || first == "go.sum" || first == "go.work" {
		return false
	}
	return knownShellBinaries[first]
}

// ValidateShellExecCommands checks all SHELL_EXEC tasks for valid command
// format per isValidShellCommand. If any SHELL_EXEC target is invalid — a
// bare file path, ends in .mod/.sum, or does not start with a known binary —
// the entire LLM output is rejected and replaced with a deterministic fallback
// derived from the forensic ledger conclusion. This prevents local 7B models
// from hallucinating execution commands like "relative/path/to/go.mod" as
// SHELL_EXEC targets.
func ValidateShellExecCommands(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	for _, t := range tasks {
		if t.Type != "SHELL_EXEC" {
			continue
		}
		if !isValidShellCommand(t.Target) {
			conclusion := ExtractConclusionFromLedger(ledgerContent)
			if dep := dependencyFromConclusion(conclusion); dep != "" {
				return []Task{
					{
						StepNum:     1,
						IsDone:      false,
						Status:      "idle",
						Type:        "SHELL_EXEC",
						Target:      "go get " + dep,
						Description: "Install missing dependency (sanitized: LLM produced invalid command)",
					},
				}
			}
			return []Task{
				{
					StepNum:     1,
					IsDone:      false,
					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      "go mod tidy",
					Description: "Resolve dependency blocker (sanitized: LLM produced invalid command)",
				},
			}
		}
	}
	return tasks
}

// hasInvalidShellExecCommand returns true if any SHELL_EXEC task in the slice
// has a target that fails isValidShellCommand. Unlike ValidateShellExecCommands,
// this is a pure check with no side effects — used by the silent retry loop to
// detect LLM command hallucination without triggering deterministic substitution.
func hasInvalidShellExecCommand(tasks []Task) bool {
	for _, t := range tasks {
		if t.Type == "SHELL_EXEC" && !isValidShellCommand(t.Target) {
			return true
		}
	}
	return false
}

// shellExecReinforcement returns the strict enforcement instruction appended to
// the prompt on each silent retry attempt. It reminds the model what format
// SHELL_EXEC targets must follow after a previous hallucination failure.
func shellExecReinforcement(attempt, maxRetries int) string {
	return fmt.Sprintf("\n\n[SYSTEM: CRITICAL FAILURE PREVENTED] (Retry %d/%d) The SHELL_EXEC target you just generated was rejected because it is not a valid runnable command. You MUST output a real executable command — e.g. 'go get <package>', 'go mod tidy', 'git clone <url>' — NOT a file path. FORBIDDEN targets include: 'go.mod', 'go.sum', './relative/path', 'relative/path/to/go.mod', or any bare file name. The target must start with a known binary name like go, git, make, npm, docker, etc.",
		attempt, maxRetries)
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
