package prompt

import "fmt"

// AskPromptHandoffContract returns the structured intent-extraction template.
// The LLM extracts ONLY high-level raw intent, domain, and scope tags.
// It MUST NOT generate concrete file manipulation steps, line numbers, or code edits.
func AskPromptHandoffContract() string {
	return `MODE: /ask — extract high-level raw intent.

Evaluate the raw idea below. Extract ONLY the following three fields:

raw_intent: <1-2 sentence description of what the user wants, using their own words>
affected_domain: <FRONTEND_UI | BACKEND_API | DATABASE | CONFIG | TEST | OTHER>
scope_tags: <comma-separated list of domain-specific tags, e.g. "layout,navigation,positioning" or "api,rest,handler">

FORBIDDEN:
- Do NOT generate concrete file manipulation steps, line numbers, or code edits.
- Do NOT produce "Steps:", "Goal:", "Targets:" sections.
- Do NOT suggest specific CSS properties, HTML elements, or code changes.
- Do NOT reference specific line numbers, file paths, or code snippets.

AFFIRMATIVE:
- Preserve the user's original wording in raw_intent.
- Classify the domain honestly based on semantic content.
- Tag with lightweight scoping labels.

Now process the following raw user input:`
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
