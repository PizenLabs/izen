package prompt

// BuildContract returns the combined operational contract for build mode. It
// is file-state aware: the mode-level system prompt must never force a patch
// format onto a file that does not exist yet. Existing files get SEARCH/REPLACE
// or unified-diff instructions; NEW or 0-byte files get full-content
// instructions. The strategy-specific contracts (NewFileContract /
// ExistingFileContract) are the canonical per-strategy prompts; this function
// stays the neutral mode-level superset so a single /build turn that touches
// both new and existing files never produces a format conflict.
func BuildContract() string {
	cb := "```"
	return `MODE: BUILD — modify existing files or create new files.

The workspace you are editing contains a mix of EXISTING files and NEW files
that do not exist yet. Choose the output format based on the file's state:

EXISTING FILE (content already on disk)
- Output ONLY the minimal change using SEARCH/REPLACE blocks or unified diffs.
- ` + cb + `go:main.go
<<<<<<< SEARCH
old code
=======
new code
>>>>>>>
` + cb + `
- Never rewrite the entire file.

NEW FILE, 0-BYTE FILE, OR STUB (no content or under 100 lines on disk)
- Output the COMPLETE file content inside a single markdown code block with the
  appropriate language tag (e.g. ` + cb + `css, ` + cb + `javascript, ` + cb + `html, ` + cb + `python, ` + cb + `go), OR a ` + "`FILE: <path>`" + ` block followed by the full content.
- Do NOT force a SEARCH/REPLACE patch or unified diff on a file that does not
  exist — there is no "old content" to search for.
- Stub files are incomplete skeletons: output the COMPLETE, FULLY IMPLEMENTED
  content and expand every function, style, and markup. Do NOT repeat stubs.
- Do NOT use FILE_CREATE markers.

RULES
- Output exact target relative file paths from the workspace root. Do NOT invent subdirectories like path/to/file/.
- No conversational text, explanations, or markdown outside the block.
- Output ends immediately after the last block.` + TokenThriftyConstraint
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
Output the complete file content either inside a SINGLE markdown code block
or as a FILE: <path> block:
` + "`FILE: " + `index.html
<complete file content>
` + "`" + `
Use the appropriate language tag on the opening fence (e.g. ` + "```" + `css, ` + "```" + `javascript, ` + "```" + `html, ` + "```" + `python, ` + "```" + `go).

RULES
- Output the ENTIRE file content. Do not omit any code.
- Do NOT use SEARCH/REPLACE blocks (<<<<<<< SEARCH) — the file does not exist yet.
- Do NOT use unified diff format (--- a/ +++ b/) — the file does not exist yet.
- Do NOT use FILE_CREATE markers.
- Do NOT wrap the content in HTML if the target language is CSS or JavaScript.
- Do NOT add conversational text, explanations, or markdown outside the code block.
- The first token MUST be the opening ` + "```" + ` fence with the language tag, or the FILE: header.
- The last token MUST be the closing ` + "```" + ` fence (or the end of the FILE block).

OUTPUT FORMAT:
` + cb + `css
body { margin: 0; }
` + cb + TokenThriftyConstraint
}

// ExistingFileContract returns the system prompt for modifying an existing
// file with content. It MUST NOT be used for NEW or 0-BYTE files — a
// SEARCH/REPLACE patch against a file with no content forces models into a
// reasoning loop (there is no "old content" to match) and times out. Those
// files use NewFileContract instead.
func ExistingFileContract() string {
	code := "```"
	return `MODE: PATCH — modify an existing file with precision.

Output ONLY the minimal change needed using one of these formats:

METHOD C — SEARCH/REPLACE (preferred):
` + code + `go:main.go
<<<<<<< SEARCH
old code
=======
new code
>>>>>>>
` + code + `

METHOD D — Unified Diff (alternative):
` + code + `diff
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 existing
-old
+new
` + code + `

RULES
- Existing files -> SEARCH/REPLACE or unified diff only. Never rewrite the entire file.
- Stub files (under 100 lines) -> output the COMPLETE, FULLY IMPLEMENTED file content in one block. Never use SEARCH/REPLACE or unified diff.
- The SEARCH blocks you emit MUST match text that actually exists in the file.
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
