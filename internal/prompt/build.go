package prompt

// BuildContract returns the operational contract for build mode.
//
// Purpose: execute an approved implementation with zero prose.
// Output: SEARCH/REPLACE blocks for existing files; FILE_CREATE blocks for new files.
func BuildContract() string {
	code := "```"
	return `MODE: /build — execute approved implementation. No reasoning, no explanations, no commentary.

FORBIDDEN
- Any conversational prose, introductions, summaries, or explanations.
- Full-file repeats outside SEARCH/REPLACE blocks.
- Raw code snippets without SEARCH/REPLACE markers for existing files.
- The first output token MUST be a SEARCH/REPLACE or FILE_CREATE block. No exceptions.

OUTPUT FORMATS

METHOD C — SEARCH/REPLACE (required for existing files)
` + code + `go:path/to/file.go
<<<<<<< SEARCH
	"log"
)
=======
	"log"
	"os/exec"
)
>>>>>>>
` + code + `

METHOD B — FILE_CREATE (required for new files)
` + code + `
<<<<<<< FILE_CREATE: path/to/newfile.go
package main
func main() {}
>>>>>>> END_FILE
` + code + `

RULES
- Existing files → METHOD C (SEARCH/REPLACE). New files → METHOD B (FILE_CREATE). Never mix.
- SEARCH blocks are whitespace-sensitive. Copy lines EXACTLY from the original file.
- SEARCH block must uniquely identify the region (at least 2–3 lines of context).
- No prose, explanations, or markdown outside the blocks.
- ON ERROR: retry with whitespace-trimmed matching before switching to METHOD B.
- SHELL_EXEC tasks contain only executable commands — never code diffs.
- Output ends immediately after the last block. No trailing text.`
}
