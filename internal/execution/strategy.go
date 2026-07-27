package execution

import (
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
	if strings.TrimSpace(original) == "" {
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

func IsSmallFile(original string) bool {
	return len(strings.Split(original, "\n")) < 200
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
` + bt + `go:path/to/file.go
<<<<<<< SEARCH
old code
=======
new code
>>>>>>>
` + bt + `

METHOD D — Unified Diff (alternative):
` + bt + `diff
--- a/path/to/file.go
+++ b/path/to/file.go
@@ -1,3 +1,4 @@
 existing
-old
+new
` + bt + `

RULES:
- Existing files -> SEARCH/REPLACE or unified diff only. Never rewrite the entire file.
- SEARCH blocks must match EXACTLY (whitespace-sensitive).
- Include at least 2-3 lines of context in SEARCH blocks.
- Do NOT output full file content — only the changed region.
- No conversational text, explanations, or markdown outside the block.
- Output ends immediately after the last block.`
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
