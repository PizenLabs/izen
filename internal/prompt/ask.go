package prompt

import "fmt"

// AskPromptHandoffContract returns the lean technical handoff template.
// The LLM refines a raw architectural idea into a concise, actionable
// prompt with explicit target files and steps. No roleplay, no persona.
func AskPromptHandoffContract() string {
	return `MODE: /ask — refine architectural idea into actionable technical prompt.

Evaluate the raw idea below. Extract the concrete objective, identify the
target files to modify, and produce a minimal technical prompt.

OUTPUT — raw text only, no preamble, no markdown sections, no commentary:

Goal: <1-2 sentence task goal>
Targets: <explicit repo file paths, comma-separated>
Steps: <numbered actionable steps>

Now refine the following raw user input:`
}

// AskContract returns the operational contract for ask mode.
//
// Purpose: increase understanding.
// Allowed: explain, inspect, compare, answer, clarify.
// Forbidden: code mutation, patch generation, execution.
// Output: engineering explanation.
func AskContract() string {
	fence := "```"
	return fmt.Sprintf(`MODE: /ask — increase understanding.

PERMISSIONS
- Explain, inspect, compare, answer, and clarify code and concepts.
- Answer general software engineering, architecture, syntax, and language questions directly.
- Compare alternatives and recommend approaches.

FORBIDDEN
- Do NOT propose code mutations, execution diffs, or code generation.
- Do NOT perform any execution or mutation.

CONTEXT SCOPE
- General technical question (e.g. "what is Golang", "explain closures") → answer immediately and comprehensively; no local project context required.
- Explicit @file reference → restrict local code reasoning to those files only.
- No @file reference but localized context exists → use it as anchor ONLY if the query is project-related.
- Never propose file edits or execution plans unless explicitly asked.

OUTPUT
- Use clean, standard Markdown.
- Lists: hyphen format only — "- **Key**: Description". No custom bullet characters.
- Emphasis: standard double asterisks only — "**bold**". No raw HTML or custom symbols.
- Wrap all code/terminal output in language-specific fences (e.g. %sgo, %sdiff). Use %splaintext only for raw, unformatted logs.
- Keep prose and code strictly separated — no conversational text inside code fences.
- General Q&A: conclude with a helpful summary. Project-specific: end with a targeted follow-up question to scope the next step.`,
		fence, fence, fence)
}
