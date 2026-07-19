package plan

import (
	"encoding/json"
	"fmt"
	"strings"
)

type PlanOutput struct {
	ContextAnchor         ContextAnchor `json:"context_anchor"`
	ArchitecturalStrategy string        `json:"architectural_strategy"`
	AtomicTasks           []AtomicTask  `json:"atomic_tasks"`
}

type ContextAnchor struct {
	Source         string   `json:"source"`
	TargetPackages []string `json:"target_packages"`
}

type AtomicTask struct {
	TaskID      int    `json:"task_id"`
	File        string `json:"file"`
	Strategy    string `json:"strategy"`
	Description string `json:"description"`
}

type JSONPlanValidationResult struct {
	Plan  *PlanOutput `json:"plan,omitempty"`
	Tasks []Task      `json:"tasks,omitempty"`
	Valid bool        `json:"valid"`
	Error string      `json:"error,omitempty"`
}

func ParseJSONPlan(content string) *JSONPlanValidationResult {
	content = sanitizeJSONContent(content)
	content = strings.TrimSpace(content)

	var plan PlanOutput
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		rawPreview := content
		if len(rawPreview) > 120 {
			rawPreview = rawPreview[:120]
		}
		return &JSONPlanValidationResult{
			Valid: false,
			Error: fmt.Sprintf("JSON parse error: %v (content preview: %q)", err, rawPreview),
		}
	}

	if len(plan.AtomicTasks) == 0 {
		return &JSONPlanValidationResult{
			Valid: false,
			Error: "plan must contain at least one atomic_task",
		}
	}

	if plan.ArchitecturalStrategy == "" {
		return &JSONPlanValidationResult{
			Valid: false,
			Error: "plan must contain architectural_strategy",
		}
	}

	for _, task := range plan.AtomicTasks {
		if IsDocumentationTarget(task.File, "FILE_MUTATE") {
			return &JSONPlanValidationResult{
				Valid: false,
				Error: "documentation targets (README.md, docs, etc.) are prohibited; use SHELL_EXEC or go.mod mutation for dependency fixes instead",
			}
		}
	}

	tasks := convertAtomicTasks(plan.AtomicTasks)
	return &JSONPlanValidationResult{
		Plan:  &plan,
		Tasks: tasks,
		Valid: true,
	}
}

func convertAtomicTasks(atomic []AtomicTask) []Task {
	tasks := make([]Task, 0, len(atomic))
	for i, a := range atomic {
		strategy := a.Strategy
		if strategy == "" {
			strategy = "FILE_MUTATE"
		}
		taskType := mapStrategyToType(strategy)
		target := a.File
		desc := a.Description
		if desc == "" {
			desc = fmt.Sprintf("%s: %s", strategy, a.File)
		}
		tasks = append(tasks, Task{
			StepNum:     i + 1,
			IsDone:      false,
			Status:      "idle",
			Type:        taskType,
			Target:      target,
			Description: desc,
		})
	}
	return tasks
}

func mapStrategyToType(strategy string) string {
	switch strings.ToUpper(strategy) {
	case "ATOMIC_REPLACE", "DIFF_PATCH", "FILE_MUTATE":
		return "FILE_MUTATE"
	case "SHELL_EXEC":
		return "SHELL_EXEC"
	case "GIT_ACTION":
		return "GIT_ACTION"
	default:
		return "FILE_MUTATE"
	}
}

// sanitizeJSONContent strips everything that is not valid JSON from an LLM
// response before passing it to json.Unmarshal. It handles:
//   - Markdown code fences (```json, ```)
//   - // line comments before, after, or within the JSON structure
//   - /* */ block comments
//   - Leading/trailing non-JSON text before the first { or after the last }
//   - Trailing // comments on JSON lines
//
// This is the critical sanitization gate that prevents LLM-generated
// structural noise from crashing the /plan parser.
func sanitizeJSONContent(content string) string {
	content = strings.TrimSpace(content)

	// 1. Strip markdown code fences (handle nested fences too).
	for strings.HasPrefix(content, "```") {
		firstNewline := strings.Index(content, "\n")
		if firstNewline != -1 {
			content = content[firstNewline+1:]
		} else {
			break
		}
		content = strings.TrimSpace(content)
	}
	for strings.HasSuffix(content, "```") {
		lastBackticks := strings.LastIndex(content, "```")
		if lastBackticks != -1 {
			content = strings.TrimSpace(content[:lastBackticks])
		} else {
			break
		}
	}
	// Repeat once more for nested fences (e.g., ```json ``` ```).
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		firstNewline := strings.Index(content, "\n")
		if firstNewline != -1 {
			content = content[firstNewline+1:]
		}
	}
	content = strings.TrimSpace(content)
	if strings.HasSuffix(content, "```") {
		lastBackticks := strings.LastIndex(content, "```")
		if lastBackticks != -1 {
			content = strings.TrimSpace(content[:lastBackticks])
		}
	}
	content = strings.TrimSpace(content)

	// 2. Strip leading // line comments (each line starting with // before {).
	lines := strings.Split(content, "\n")
	cleaned := make([]string, 0, len(lines))
	inBlockComment := false
	foundJSON := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track /* */ block comments.
		if idxOpen := strings.Index(trimmed, "/*"); idxOpen >= 0 {
			inBlockComment = true
			// If the block comment ends on the same line, strip just the comment.
			if idxClose := strings.LastIndex(trimmed, "*/"); idxClose >= idxOpen+2 {
				before := strings.TrimSpace(trimmed[:idxOpen])
				after := strings.TrimSpace(trimmed[idxClose+2:])
				trimmed = strings.TrimSpace(before + " " + after)
				inBlockComment = false
			} else {
				// Block comment started — skip the /* portion.
				trimmed = strings.TrimSpace(trimmed[:idxOpen])
			}
		}
		if inBlockComment {
			if idxClose := strings.LastIndex(trimmed, "*/"); idxClose >= 0 {
				trimmed = strings.TrimSpace(trimmed[idxClose+2:])
				inBlockComment = false
			} else {
				continue
			}
		}

		// Once we've seen a JSON structural character, keep everything (except
		// inline // comments within JSON string values).
		if !foundJSON {
			if trimmed == "" || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				foundJSON = true
			}
		}

		// Strip trailing // comments on JSON lines (but be careful not to
		// strip // inside string values).
		if foundJSON && strings.Contains(trimmed, "//") {
			trimmed = stripTrailingComment(trimmed)
		}

		cleaned = append(cleaned, trimmed)
	}
	content = strings.Join(cleaned, "\n")
	content = strings.TrimSpace(content)

	// 3. If content still has no JSON prefix, do a last-resort scan for the
	// first { or [ and grab everything through the matching closing bracket.
	if !strings.HasPrefix(content, "{") && !strings.HasPrefix(content, "[") {
		content = extractJSONObject(content)
	}

	return content
}

// stripTrailingComment removes a trailing // comment from a JSON line while
// preserving // that appears inside a quoted JSON string value.
func stripTrailingComment(line string) string {
	inString := false
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if !inString && ch == '/' && i+1 < len(line) && line[i+1] == '/' {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

// extractJSONObject scans content for the first { or [ and returns the
// balanced JSON substring through the matching closing bracket.
func extractJSONObject(content string) string {
	content = strings.TrimSpace(content)
	start := strings.Index(content, "{")
	if start == -1 {
		start = strings.Index(content, "[")
	}
	if start == -1 {
		return content
	}
	content = content[start:]

	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(content); i++ {
		ch := content[i]
		if esc {
			esc = false
			continue
		}
		if ch == '\\' {
			esc = true
			continue
		}
		if ch == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if ch == '{' || ch == '[' {
			depth++
			continue
		}
		if ch == '}' || ch == ']' {
			depth--
			if depth == 0 {
				return content[:i+1]
			}
		}
	}
	return content
}

func SchemaJSONInstruction() string {
	return `You MUST output ONLY a single raw JSON object — NO markdown fences, NO // comments, NO extra text — with this EXACT schema:

{
  "context_anchor": {
    "source": "origin of this plan (e.g. user-request, diagnose-ledger)",
    "target_packages": ["package1", "package2"]
  },
  "architectural_strategy": "One-sentence summary of the architectural approach",
  "atomic_tasks": [
    {
      "task_id": 1,
      "file": "relative/path/to/file.go",
      "strategy": "ATOMIC_REPLACE",
      "description": "What to do and why"
    }
  ]
}

RULES:
1. Output ONLY the JSON object. No introductory text, no markdown, no code fences, no // comments.
2. context_anchor.source must identify where this plan originated.
3. context_anchor.target_packages lists all packages affected.
4. architectural_strategy is a single concise sentence.
5. atomic_tasks must have at least one entry. Each entry must have all four fields.
6. strategy must be one of: ATOMIC_REPLACE, DIFF_PATCH, SHELL_EXEC, GIT_ACTION.
7. file paths must be relative to project root.
8. task_id values must be sequential integers starting at 1.
 9. SHELL_EXEC is REQUIRED (not forbidden) when the investigation root cause is a
    compilation or dependency error: emit an exact command such as
    "go get <package>" or "go mod tidy". NEVER patch documentation files
    (README.md, docs/, CHANGELOG, etc.) to work around build failures — resolve
    the dependency via SHELL_EXEC or mutate go.mod instead.
 10. If a file has severe syntax/AST errors, strategy MUST be "ATOMIC_REPLACE" (complete file override).
 11. Documentation files (README.md, *.md docs, LICENSE, CONTRIBUTING.md, SECURITY.md,
     CODE_OF_CONDUCT.md) are PROHIBITED targets under every strategy.`
}
