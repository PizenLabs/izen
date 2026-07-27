package prompt

// BuildContract returns the combined operational contract for build mode.
//
// Deprecated: Use NewFileContract or ExistingFileContract for strategy-specific
// prompts. This function is retained for backward compatibility.
func BuildContract() string {
	return ExistingFileContract()
}

// NewFileContract returns the system prompt for generating a brand-new file.
// The LLM is instructed to output RAW FULL FILE CONTENT inside a single
// markdown code block, NOT SEARCH/REPLACE blocks or unified diffs.
// TokenThriftyConstraint is appended to all build/plan system prompts to enforce
// completion token efficiency and eliminate conversational filler.
const TokenThriftyConstraint = `
STRICT RULE: Output ONLY valid code within markdown codeblocks. ZERO conversational filler, ZERO intros/outros, ZERO explanations.
Write minimal, clean, modern code to optimize completion token efficiency.`

func NewFileContract() string {
	cb := "```"
	return `MODE: FILE_CREATE — generate a new file from scratch.

Generate ONLY the raw file content for the requested file.
Output the complete file content inside a SINGLE markdown code block.
Use the appropriate language tag on the opening fence (e.g. ` + "```" + `css, ` + "```" + `javascript, ` + "```" + `html, ` + "```" + `python, ` + "```" + `go).

RULES
- Output the ENTIRE file content. Do not omit any code.
- Do NOT use SEARCH/REPLACE blocks (<<<<<<< SEARCH) — the file does not exist yet.
- Do NOT use unified diff format (--- a/ +++ b/) — the file does not exist yet.
- Do NOT use FILE_CREATE markers.
- Do NOT wrap the content in HTML if the target language is CSS or JavaScript.
- Do NOT add conversational text, explanations, or markdown outside the code block.
- The first token MUST be the opening ` + "```" + ` fence with the language tag.
- The last token MUST be the closing ` + "```" + ` fence.

OUTPUT FORMAT:
` + cb + `css
body { margin: 0; }
` + cb + TokenThriftyConstraint
}

// ExistingFileContract returns the system prompt for modifying an existing file.
// The LLM is instructed to output SEARCH/REPLACE blocks or unified diffs.
func ExistingFileContract() string {
	code := "```"
	return `MODE: PATCH — modify an existing file with precision.

Output ONLY the minimal change needed using one of these formats:

METHOD C — SEARCH/REPLACE (preferred):
` + code + `go:path/to/file.go
<<<<<<< SEARCH
old code
=======
new code
>>>>>>>
` + code + `

METHOD D — Unified Diff (alternative):
` + code + `diff
--- a/path/to/file.go
+++ b/path/to/file.go
@@ -1,3 +1,4 @@
 existing
-old
+new
` + code + `

RULES
- Existing files -> SEARCH/REPLACE or unified diff only. Never rewrite the entire file.
- SEARCH blocks must match EXACTLY (whitespace-sensitive).
- Include at least 2-3 lines of context in SEARCH blocks.
- Do NOT output full file content — only the changed region.
- No conversational text, explanations, or markdown outside the block.
- Output ends immediately after the last block.` + TokenThriftyConstraint
}

// ExistingFileSmallFallbackContract returns the system prompt for rewriting a
// small existing file entirely. Used as a fallback when SEARCH/REPLACE or
// unified diff application fails for files under 200 lines.
func ExistingFileSmallFallbackContract() string {
	cb := "```"
	return `MODE: FILE_REWRITE — rewrite a small existing file.

Output the COMPLETE new file content inside a SINGLE markdown code block.
Use the appropriate language tag on the opening fence.

RULES
- Output the ENTIRE file content. Do not omit any code.
- Do NOT use SEARCH/REPLACE blocks.
- Do NOT use unified diff format.
- Do NOT add conversational text.
- The first token MUST be the opening ` + "```" + ` fence.
- The last token MUST be the closing ` + "```" + ` fence.

OUTPUT FORMAT:
` + cb + `go
package main

func main() {}
` + cb + TokenThriftyConstraint
}

// StrategyContract returns the appropriate system prompt for the given
// strategy. This is the canonical dispatch point for strategy-aware prompts.
func StrategyContract(strategy string) string {
	switch strategy {
	case "new_file":
		return NewFileContract()
	case "existing_file":
		return ExistingFileContract()
	case "small_fallback":
		return ExistingFileSmallFallbackContract()
	default:
		return ExistingFileContract()
	}
}
