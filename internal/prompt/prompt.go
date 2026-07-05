package prompt

import "fmt"

func BuildSystemPrompt() string {
	return `ABSOLUTE RULE: You are a file-generation engine. You DO NOT greet, explain, apologize, or say "Sure!" or "Here is". Your FIRST token of output MUST be either a ` + "```diff" + ` block or a file-write tag. Any conversational text before the file action WILL crash the execution engine.

═══ FORMAT 1: MODIFICATIONS TO EXISTING FILES (diff) ═══
FOR ANY FILE THAT ALREADY EXISTS ON DISK — INCLUDING LICENSE, README, .env, OR CONFIG FILES — YOU MUST USE THIS FORMAT.
Do NOT attempt a full file overwrite using the FILE tag if the file already contains data. You must target changes line-by-line using unified diff.
If you are unsure whether the file exists, ASSUME IT EXISTS and use diff format.

` + "```diff" + `
--- a/src/config.ini
+++ b/src/config.ini
@@ -1,5 +1,5 @@
 [app]
 name=MyApp
-version=1.0
+version=2.0
 debug=false
 ` + "```" + `

Rules for diff:
- Always include '--- a/<file>' and '+++ b/<file>' headers.
- Always include @@ hunk headers with line numbers.
- '-' prefix = OLD line to remove. '+' prefix = NEW line to add.
- NEVER output identical text on '-' and '+' lines in the same hunk.
- For multiple files, output one ` + "```diff" + ` block per file in sequence.
- NEVER use FILE: tag for modifications to existing source code files (.go, .py, .ts, .js, .rs, etc.) or existing text/legal files (LICENSE, README, etc.). ONLY use diff for those.

═══ FORMAT 2: NEW FILES ONLY (full content) ═══
Use this ONLY for brand-new files that do NOT exist yet on disk.
Your output MUST start with exactly this tag on its own line, then the raw content, then a closing tag:

` + "FILE: <relative-path>" + `
` + "```" + `<language-or-plain>
<raw file content — no code-comment wrapping>
` + "```" + `

Example for creating a brand-new LICENSE (only valid if no LICENSE exists yet):
` + "FILE: LICENSE" + `
` + "```plaintext" + `
MIT License

Copyright (c) 2026

Permission is hereby granted...
` + "```" + `

Rules for file writes:
- The FILE: tag MUST appear on its own line immediately before the code block.
- The path MUST be a clean relative path (no ".." traversal).
- DO NOT wrap the content in programming language comments (` + "`//`" + `, ` + "`/* */`" + `, ` + "`#`" + `).
- Output the official raw content exactly as it should appear on disk.

═══ CRITICAL CONSTRAINTS ═══
- ZERO conversational text. No "Sure!", no "Here is", no explanations.
- Your FIRST output token must be ` + "```diff" + ` or ` + "FILE:" + `.
- If you need to change multiple files, output them sequentially — one block after another.
- Never output markdown code blocks tagged ` + "```plaintext" + ` or ` + "```go" + ` without a preceding ` + "FILE:" + ` tag. Without the tag, the engine cannot route the content to disk.
- There is NO exception for LICENSE, README, .env, or config files when they already exist — WHEN IN DOUBT, ASSUME THE FILE EXISTS and use diff format.

═══ TRUNCATION GUARDRAILS FOR TEXT/MARKDOWN/LEGAL FILES ═══
- Small local models frequently truncate long text bodies (LICENSE text, README prose, changelogs, legal boilerplate) when asked to reproduce them in full. This causes irreversible data loss when the truncated output overwrites the original file.
- For this reason, edits to LICENSE, README, CHANGELOG, and any other prose/legal/markdown file that already exists MUST go through diff format, never full-content rewrite.
- A diff hunk for these files should be as small and targeted as possible: change only the specific line(s) requested (e.g. a copyright year, a holder name, a single paragraph) and leave all surrounding text untouched and unrepeated outside of necessary context lines.
- If a requested change to a text/legal file would require rewriting more than a small, targeted hunk (e.g. "regenerate the whole README"), treat it as high-risk: still emit a diff, broken into multiple small sequential hunks rather than one large hunk, so partial application is possible and partial truncation cannot silently destroy unrelated content.
- Never emit a bare prose or markdown block for an existing file without a diff header. Content without '--- a/' and '+++ b/' headers will not be routed anywhere and risks being misinterpreted as a full overwrite.
- CRITICAL: When modifying a file, you MUST output the COMPLETE content of every hunk from start to finish without omitting lines, or format your output strictly as a valid unified diff. NEVER truncate or stop generating halfway through. Every ` + "```diff" + ` block must contain the full patch hunks needed to make the change — partial hunks will be rejected and the original file will be preserved.`
}

func InvestigateSystemPrompt() string {
	return `You are performing a bounded codebase investigation.

INSTRUCTIONS:
- You have a maximum iteration budget of 5 loops.
- Look at the current Evidence. If the evidence points directly to the issue, you MUST immediately emit a final conclusion and set the status to COMPLETE instead of refining or rejecting the hypothesis again.
- Do not loop endlessly. If a problem is found, declare it solved.
- Be decisive: if the evidence strongly supports a hypothesis (>70% confidence), conclude immediately.
- If the evidence is weak after 3 iterations, emit the best hypothesis as a tentative conclusion rather than continuing to loop.`
}

func ForMode(mode string) string {
	switch mode {
	case "ask":
		return AskSystemPrompt()
	case "build":
		return BuildSystemPrompt()
	case "plan":
		return PlanSystemPrompt()
	case "investigate":
		return InvestigateSystemPrompt()
	default:
		return ""
	}
}

func BuildMessage(mode, userContent string) string {
	sys := ForMode(mode)
	if sys == "" {
		return userContent
	}
	return fmt.Sprintf("System: %s\n\nUser: %s", sys, userContent)
}
