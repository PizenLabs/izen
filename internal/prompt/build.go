package prompt

// BuildContract returns the operational contract for build mode.
//
// Purpose: execute an approved implementation with zero prose.
// Output: SEARCH/REPLACE blocks for existing files; FILE_CREATE blocks for new files;
// unified diff (--- a/... +++ b/...) when explicitly requested.
// NO conversational text, NO markdown outside code blocks, NO explanations.
func BuildContract() string {
	code := "```"
	return `MODE: /build — execute approved implementation. Zero prose. Zero explanation. Zero commentary.

FORBIDDEN
- Any conversational prose, introductions, summaries, or explanations.
- Full-file repeats outside SEARCH/REPLACE blocks or diff format.
- Raw code snippets without SEARCH/REPLACE markers or diff headers for existing files.
- The first output token MUST be a SEARCH/REPLACE block, a unified diff (--- a/), or a FILE_CREATE block. No exceptions.
- Do NOT wrap output in markdown code fences (fences with diff/go/etc. language tags) unless the user explicitly requests it. If you MUST use a fence, use a diff-tagged fence.
- Do NOT add any text before or after the structural block. No "Here is the patch:", no "Let me know if you need anything", no greetings.
- Return ONLY the raw structural block. No markdown backticks outside the block. No trailing whitespace. No closing remarks.

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

METHOD D — UNIFIED DIFF (alternative for existing files, especially for cloud models)
` + code + `diff --git a/path/to/file.go b/path/to/file.go
` + code + ` ` + code + `
--- a/path/to/file.go
+++ b/path/to/file.go
@@ -10,6 +10,8 @@ func main() {
 	existing line
+	added line
 	another existing line
` + code + `

RULES
- Existing files → METHOD C (SEARCH/REPLACE) or METHOD D (unified diff). New files → METHOD B (FILE_CREATE). Never mix.
- SEARCH blocks are whitespace-sensitive. Copy lines EXACTLY from the original file.
- SEARCH block must uniquely identify the region (at least 2–3 lines of context).
- When using METHOD D (unified diff), include the --- a/ and +++ b/ headers and @@ hunk markers.
- No prose, explanations, or markdown outside the blocks.
- ON ERROR: retry with whitespace-trimmed matching before switching to METHOD D unified diff.
- SHELL_EXEC tasks contain only executable commands — never code diffs.
- Output ends immediately after the last block. No trailing text.`
}
