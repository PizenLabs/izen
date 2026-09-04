package execution

import (
	"fmt"
	"os"
	"strings"
)

type FileMutationStrategy int

const (
	STRATEGY_NEW_FILE FileMutationStrategy = iota
	STRATEGY_EXISTING_FILE
)

func (s FileMutationStrategy) String() string {
	switch s {
	case STRATEGY_NEW_FILE:
		return "STRATEGY_NEW_FILE"
	case STRATEGY_EXISTING_FILE:
		return "STRATEGY_EXISTING_FILE"
	default:
		return "STRATEGY_UNKNOWN"
	}
}

func StrategyForFile(target string) FileMutationStrategy {
	if data, err := os.ReadFile(target); err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return STRATEGY_NEW_FILE
	}
	return STRATEGY_EXISTING_FILE
}

func StrategyForOriginal(original string) FileMutationStrategy {
	if IsStubFile(original) {
		return STRATEGY_NEW_FILE
	}
	return STRATEGY_EXISTING_FILE
}

func (s FileMutationStrategy) SystemPromptKey() string {
	switch s {
	case STRATEGY_NEW_FILE:
		return "new_file"
	case STRATEGY_EXISTING_FILE:
		return "existing_file"
	default:
		return "existing_file"
	}
}

// SmallFileLineThreshold is the "Explicit Over Implicit" stub boundary. Any
// target file that does not exist, is 0 bytes, or has fewer than this many
// lines is treated as a stub: forcing it through a SEARCH/REPLACE diff
// protocol makes SLMs fail with "ambiguous snippet without SEARCH/REPLACE
// markers" or loop on missing "old content" anchors. Stubs are ALWAYS
// rewritten whole-file.
const SmallFileLineThreshold = 100

// IsSmallFile reports whether the content is a small/stub file (under
// SmallFileLineThreshold lines) that must be whole-file overwritten. Line count
// uses wc -l semantics (count of newlines), so a 99-line file with a trailing
// newline counts as 99 < 100.
func IsSmallFile(original string) bool {
	return LineCount(original) < SmallFileLineThreshold
}

// IsStubFile reports whether the content represents a stub: empty/whitespace
// only (new or 0-byte file) or under SmallFileLineThreshold lines.
func IsStubFile(original string) bool {
	return strings.TrimSpace(original) == "" || IsSmallFile(original)
}

// LineCount returns the number of newline-terminated lines in content (wc -l
// semantics). Empty content is 0 lines.
func LineCount(content string) int {
	return strings.Count(content, "\n")
}

func StrategyWithFallback(original, diffInput string) FileMutationStrategy {
	base := StrategyForOriginal(original)
	if base == STRATEGY_EXISTING_FILE && IsSmallFile(original) {
		if diffInput != "" && !strings.Contains(diffInput, "<<<<<<< SEARCH") && !strings.Contains(diffInput, "@@") {
			return STRATEGY_NEW_FILE
		}
	}
	return base
}

func StrategyOverrideOnFailure(original string, attempt int) FileMutationStrategy {
	if attempt > 0 && IsSmallFile(original) {
		return STRATEGY_NEW_FILE
	}
	return StrategyForOriginal(original)
}

func NewFileGenerationSystemPrompt() string {
	return `MODE: FILE_CREATE — generate a new file from scratch.

Generate ONLY the raw file content for the requested file.
Output the complete file content inside a SINGLE markdown code block.
Use the appropriate language tag on the opening fence (e.g. ` + "```" + `css, ` + "```" + `javascript, ` + "```" + `html, ` + "```" + `python, ` + "```" + `go).

RULES:
- Output the ENTIRE file content. Do not omit any code.
- Do NOT use SEARCH/REPLACE blocks (<<<<<<< SEARCH) — the file does not exist yet.
- Do NOT use unified diff format (--- a/ +++ b/) — the file does not exist yet.
- Do NOT use FILE_CREATE markers.
- Do NOT wrap the content in HTML if the target language is CSS or JavaScript.
- Do NOT add conversational text, explanations, or markdown outside the code block.
- The first token MUST be the opening ` + "```" + ` fence with the language tag.
- The last token MUST be the closing ` + "```" + ` fence.

OUTPUT FORMAT:
` + "```" + `css
body { margin: 0; }
` + "```" + `
`
}

func ExistingFileMutationSystemPrompt() string {
	bt := "```"
	return `MODE: PATCH — modify an existing file with precision.

Output ONLY the minimal change needed using one of these formats:

METHOD C — SEARCH/REPLACE (preferred):
` + bt + `go:main.go
<<<<<<< SEARCH
old code
=======
new code
>>>>>>>
` + bt + `

METHOD D — Unified Diff (alternative):
` + bt + `diff
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 existing
-old
+new
` + bt + `

RULES:
- Existing files -> SEARCH/REPLACE or unified diff only. Never rewrite the entire file.
- Stub files (under 100 lines) -> output the COMPLETE, FULLY IMPLEMENTED file content in one block.
- SEARCH blocks must match EXACTLY (whitespace-sensitive).
- Include at least 2-3 lines of context in SEARCH blocks.
- Do NOT output full file content — only the changed region.
- No conversational text, explanations, or markdown outside the block.
- Output ends immediately after the last block.`
}

// ExecutionConstraint isolates strategy handling from Authorization DecisionSurface.
// It carries the model output budget and strategy shape without coupling to UI.
type ExecutionConstraint struct {
	Strategy        FileMutationStrategy
	MaxOutputTokens int
	Shape           BudgetShape
}

// DegradeStrategy silently degrades FULL_REWRITE -> BOUNDED_PATCH -> STRICT_PATCH
// on model output budget shortfall. Returns the degraded constraint.
func DegradeStrategy(c ExecutionConstraint, requiredOutputTokens int) ExecutionConstraint {
	if c.Shape == ShapeFullRewrite && requiredOutputTokens > c.MaxOutputTokens && c.MaxOutputTokens > 0 {
		c.Shape = ShapeBoundedPatch
		return c
	}
	if c.Shape == ShapeBoundedPatch && requiredOutputTokens > c.MaxOutputTokens && c.MaxOutputTokens > 0 {
		c.Shape = ShapeInspectOnly // strict patch maps to inspect-only shape here
		return c
	}
	return c
}

// PolicyTransitionForLength implements the finish_reason == "length" transition table:
// Attempt 1 Normal -> retry with StrategyStrictPatch (strip commentary),
// Attempt 2 StrictPatch + length -> Strict Halt with Physical Budget Failure.
func PolicyTransitionForLength(attempt int, currentShape BudgetShape, finishReason string) (nextShape BudgetShape, halt bool, err error) {
	if finishReason != "length" {
		return currentShape, false, nil
	}
	switch attempt {
	case 1:
		return ShapeBoundedPatch, false, nil
	case 2:
		return currentShape, true, fmt.Errorf("physical budget failure: output exhausted twice (finish_reason=length)")
	default:
		return currentShape, true, fmt.Errorf("physical budget failure: output exhausted")
	}
}

func ExistingFileSmallFallbackSystemPrompt() string {
	return `MODE: FILE_REWRITE — rewrite a small existing file.

Output the COMPLETE new file content inside a SINGLE markdown code block.
Use the appropriate language tag on the opening fence.

RULES:
- Output the ENTIRE file content. Do not omit any code.
- Do NOT use SEARCH/REPLACE blocks.
- Do NOT use unified diff format.
- Do NOT add conversational text.
- The first token MUST be the opening ` + "```" + ` fence.
- The last token MUST be the closing ` + "```" + ` fence.

OUTPUT FORMAT:
` + "```" + `go
package main

func main() {}
` + "```" + `
`
}
