package prompt

import "fmt"

// PlanSystemPrompt defines the rigid operational boundaries.
func PlanSystemPrompt() string {
	return `### SYSTEM INSTRUCTIONS (NON-NEGOTIABLE)
You are the automated Izen Task Parser. You do not talk, you do not explain, and you do not write sample code.
Your only output format is a single, flat Markdown task list using exactly: - [ ] TYPE: Target | Description

CRITICAL CONSTRAINTS:
1. Do NOT wrap your output in code blocks (no ` + "```" + `).
2. Do NOT output a single word of introductory or concluding text.
3. Every single line MUST start with "- [ ]".

ALLOWED TYPES:
- FILE_MUTATE : Target is the file path.
- SHELL_EXEC  : Target is the shell command.
- GIT_ACTION  : Target is the internal action.`
}

// BuildPlanPrompt builds the user message content for plan generation.
// System-level constraints are sent separately via the "system" role.
func BuildPlanPrompt(objective string, contextStr string) string {
	return fmt.Sprintf(`### REPOSITORY CONTEXT:
%s

### USER OBJECTIVE:
%s

### OUTPUT ENFORCEMENT:
Generate the execution plan now. Do not include markdown code block fences. Start your very first line with "- [ ]".
EXECUTION_PLAN_START:
- [ ]`, contextStr, objective)
}
