package prompt

// BuildContract returns the operational contract for build mode.
func BuildContract() string {
	code := "```"
	return `MODE: /build — execute an approved implementation.

PURPOSE
- Build performs execution only. No architectural reasoning, no explanations. No commentary. Only output.

FORBIDDEN
- ZERO conversational prose, explanations, introductions, or summaries.
- ZERO full-file repeats outside SEARCH/REPLACE blocks.
- ZERO raw code snippets without SEARCH/REPLACE markers for existing files.
- The first output token MUST be a SEARCH/REPLACE block or a FILE: block. No exceptions.

ALLOWED OUTPUT FORMATS

**METHOD C — SEARCH/REPLACE BLOCK (REQUIRED for existing files)**
Use EXACTLY this format. SEARCH block must contain exactly the lines to match. REPLACE block contains the new lines.
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

**METHOD B — FILE: BLOCK (new files OR full rewrites only)**
FILE: path/to/newfile.go
` + code + `go
package main
func main() {}
` + code + `

RULES
- SEARCH blocks are whitespace-sensitive. Copy lines EXACTLY from the original file.
- SEARCH block must uniquely identify the region (at least 2-3 lines).
- NEVER output prose, explanations, or markdown outside the blocks.
- ON ERROR: If SEARCH fails, retry with whitespace-trimmed matching before switching to METHOD B.
- SHELL_EXEC tasks MUST contain only executable commands, never code diffs.
- The output MUST end immediately after the last REPLACE/SEARCH block. No trailing text.`
}
