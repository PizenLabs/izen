package prompt

import "fmt"

// PlanContract defines the behavioral contract for /plan mode.
func PlanContract() string {
	return `MODE: /plan — Transform investigation evidence into an ordered execution plan.

ROLE
- Act as a deterministic transformer inside the IZEN runtime.
- Convert the /investigate JSON ledger into isolated, actionable, verifiable tasks.
- Produce no conversational filler, greetings, or explanations outside the requested output.

RULES
- Tasks MUST be atomic, independently verifiable, and ordered by dependency.
- If a missing dependency is the root cause, Task 1 MUST be SHELL_EXEC with the exact module installation command.
- Source-code defects MUST target the exact relative file path and, when known, the relevant symbol or line range.
- Plans MUST end with an appropriate verification task when verification is supported by the evidence.`
}

// BuildPlanJSONPrompt builds the strict JSON prompt consumed by the TUI parser.
func BuildPlanJSONPrompt(problem, ledgerContent, conclusion string) string {
	conclusionBlock := ""
	if conclusion != "" {
		conclusionBlock = fmt.Sprintf(`
AUTHORITATIVE CONCLUSION
Prefer the following investigation conclusion over stale or contradictory raw-log evidence:

%s

If a dependency conflict exists, use the corrected module path explicitly.`, conclusion)
	}

	return fmt.Sprintf(`You are the IZEN Plan Transformer. Convert the investigation evidence below into a valid JSON object matching the schema defined by the IZEN runtime.

INPUT
PROBLEM:
%s

INVESTIGATION LEDGER:
%s
%s

TASK RULES
- Every “atomic_tasks” item MUST have non-empty “task_id”, “strategy”, “target”, and “rationale”.
- SHELL_EXEC: “target” MUST contain the complete exact shell command to execute.
- ATOMIC_REPLACE or DIFF_PATCH: “target” MUST contain the relative file path from the project root.
- If a missing dependency is the root cause, Task 1 MUST be SHELL_EXEC with the correct “go get <package>” command.
- Order tasks by dependency: prerequisites, mutations, then verification.
- Include a verification task when supported by the evidence.

Output ONLY the raw JSON object. No Markdown, code fences, or additional text.`,
		problem,
		ledgerContent,
		conclusionBlock,
	)
}

// BuildPlanPrompt builds the compact Markdown prompt for user-facing terminal output.
func BuildPlanPrompt(objective, contextStr string) string {
	return fmt.Sprintf(`%s

USER OBJECTIVE
%s

OUTPUT FORMAT
Output exactly this Markdown structure and stop after the final checklist item. Do not wrap in markdown code blocks:

# ⏭  EXECUTION PLAN

### ⛑ Architectural Strategy
[2–3 sentences describing the implementation strategy.]

---

### ✱ Atomic TODO Tasks
- [ ] SHELL_EXEC: <exact_command> | <Short clear rationale>
- [ ] FILE_MUTATE: <relative_path> | <Actionable description of changes>
- [ ] SHELL_EXEC: <verification_command> | Verify the complete system patch`,
		contextStr,
		objective,
	)
}
