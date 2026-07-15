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
	content = stripCodeFences(content)
	content = strings.TrimSpace(content)

	var plan PlanOutput
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return &JSONPlanValidationResult{
			Valid: false,
			Error: fmt.Sprintf("JSON parse error: %v", err),
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

func stripCodeFences(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		firstNewline := strings.Index(content, "\n")
		if firstNewline != -1 {
			content = content[firstNewline+1:]
		}
	}
	if strings.HasSuffix(content, "```") {
		lastBackticks := strings.LastIndex(content, "```")
		if lastBackticks != -1 {
			content = content[:lastBackticks]
		}
	}
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		firstNewline := strings.Index(content, "\n")
		if firstNewline != -1 {
			content = content[firstNewline+1:]
		}
	}
	if strings.HasSuffix(content, "```") {
		lastBackticks := strings.LastIndex(content, "```")
		if lastBackticks != -1 {
			content = content[:lastBackticks]
		}
	}
	return strings.TrimSpace(content)
}

func SchemaJSONInstruction() string {
	return `You MUST output ONLY a single JSON object with this EXACT schema:

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
1. Output ONLY the JSON object. No introductory text, no markdown, no code fences.
2. context_anchor.source must identify where this plan originated.
3. context_anchor.target_packages lists all packages affected.
4. architectural_strategy is a single concise sentence.
5. atomic_tasks must have at least one entry. Each entry must have all four fields.
6. strategy must be one of: ATOMIC_REPLACE, DIFF_PATCH, SHELL_EXEC, GIT_ACTION.
7. file paths must be relative to project root.
8. task_id values must be sequential integers starting at 1.
9. NO shell execution commands in the plan itself. Only file mutations and git actions.
10. If a file has severe syntax/AST errors, strategy MUST be "ATOMIC_REPLACE" (complete file override).`
}
