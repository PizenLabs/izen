package prompt

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/modes/plan"
)

func BuildSystemPrompt() string {
	return `CRITICAL: You are NOT allowed to print the entire file content using markdown block code blocks like ` + "```plaintext or ```go" + `.

You MUST only generate structural changes using standard unified diff format wrapped inside a ` + "```diff" + ` codeblock containing '--- filename' and '+++ filename' along with @@ hunks. If you fail to use this format, the execution engine will crash.

Example of the ONLY format you may use:
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

CRITICAL RULES FOR '-' AND '+' USAGE:
  - The '-' prefix MUST be applied to the EXACT OLD line you want to replace. Example:
    ` + "`-version=1.0`" + `  (the line as it exists now)
  - The '+' prefix MUST be applied to the NEW line with your modification. Example:
    ` + "`+version=2.0`" + `  (the line after your change)
  - NEVER output identical text on '-' and '+' lines in the same hunk. If the text is the same, it MUST be a context line (no prefix at all), not a pair of '-' and '+' lines.
  - A no-op patch (subtracting and re-adding the same line) is a critical bug — the execution engine will waste resources and leave the file unchanged.

Rules:
- Every file change MUST be a unified diff inside a ` + "```diff" + ` block.
- Always include the '--- a/<file>' and '+++ b/<file>' headers.
- Always include @@ hunk headers with line numbers.
- Never output full file contents — only the minimal changes.
- If you need to change multiple files, output one ` + "```diff" + ` block per file in sequence.
- Use + for added lines, - for removed lines, and no prefix for context lines.

CRITICAL INSTRUCTION FOR TEXT/DOCUMENTATION FILES:
When creating or modifying standalone text, documentation, or legal files (such as LICENSE, README.md, .gitignore, .env), you MUST output the raw text directly. DO NOT wrap the content inside code comments of any programming language (DO NOT use ` + "`/* ... */`" + `, ` + "`//`" + `, or ` + "`#`" + ` unless specifically requested by the user or required by the file spec like .env/.gitignore). Provide the official raw legal text or documentation text exactly as it is.`
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

// CompilePlanContext serializes an ExecutionPlan into a strict constraint block
// for injection into the build mode system prompt.
func CompilePlanContext(p *plan.ExecutionPlan) string {
	if p == nil || len(p.Steps) == 0 {
		return ""
	}

	var targetFiles []string
	var allSymbols []string
	var explanations []string
	seenSymbols := make(map[string]bool)

	for _, step := range p.Steps {
		targetFiles = append(targetFiles, strings.ToUpper(step.Action)+": "+step.TargetFile)
		for _, sym := range step.Symbols {
			if !seenSymbols[sym] {
				seenSymbols[sym] = true
				allSymbols = append(allSymbols, sym)
			}
		}
		if step.Explanation != "" {
			explanations = append(explanations, step.Explanation)
		}
	}

	var b strings.Builder
	b.WriteString("[STRICT EXECUTION CONSTRAINT]\n")
	b.WriteString("You must execute code mutations adhering strictly to the following approved plan:\n")
	b.WriteString("- Target Files & Actions: " + strings.Join(targetFiles, ", ") + "\n")
	b.WriteString("- Scope of Symbols: " + strings.Join(allSymbols, ", ") + "\n")
	b.WriteString("- Blueprint Architecture: " + strings.Join(explanations, " | ") + "\n")
	b.WriteString("Do not modify files or symbols outside this approved scope.\n")
	return b.String()
}
