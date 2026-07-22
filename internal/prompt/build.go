package prompt

// BuildContract returns the operational contract for build mode.
func BuildContract() string {
	diff := "```diff"
	code := "```"
	return `MODE: /build — execute an approved implementation.

PURPOSE
- Build performs execution only. No architectural reasoning, no explanations. Only implementation.

PERMISSIONS
- Generate code, produce diffs, or perform full-file rewrites.

FORBIDDEN
- Do NOT discuss architecture or plan. Do NOT output prose.
- ZERO conversational text.
- The first output token MUST be either a DIFF block, a FILE block, or a SEARCH/REPLACE block. ZERO exceptions.
- **ABSOLUTELY FORBIDDEN: raw code snippets without SEARCH/REPLACE markers or unified diff headers for existing files.** Partial output without unambiguous markers will cause immediate parser execution failure.

STREAMLINED OUTPUT FORMATS

Choose EXACTLY one format based on the task:

METHOD A — Small to Medium Changes (UNIFIED DIFF)
Use this if you are modifying specific parts of an existing file. You MUST use standard unified diff format with '-' and '+' indicating changes.
Example:
` + diff + `
--- a/cmd/api/main.go
+++ b/cmd/api/main.go
@@ -7,3 +7,4 @@ import (
  	"log"
+	"os/exec"
 )
` + code + `

METHOD B — New Files or Full Rewrites (FILE: BLOCK)
Use this ONLY for creating brand-new files, or when every other format has failed and you must rewrite the entire file from scratch.
You MUST prefix with "FILE: <path>" and output 100% raw, valid code inside the block.
Example:
FILE: cmd/api/main.go
` + code + `go
package main

func main() {
	// entire raw file content here
}
` + code + `

METHOD C — Targeted Edits (SEARCH/REPLACE BLOCK — PREFERRED)
This is the PREFERRED format for modifying existing files. You MUST use the SEARCH/REPLACE block format with <<<<<<< SEARCH and >>>>>>> markers.
The SEARCH block MUST contain EXACTLY 3-5 lines from the original file to uniquely identify the location. The REPLACE block contains the new lines.
Example:
` + code + `go:cmd/api/main.go
<<<<<<< SEARCH
	"log"
)
=======
	"log"
	"os/exec"
)
>>>>>>>
` + code + `

STRICT PARSING RULES
- If you use METHOD A (unified diff), every modified line MUST start with '+' or '-'. Unchanged context lines MUST start with a space ' '.
- If you use METHOD B (FILE:), do NOT include any '+' or '-' symbols at the start of lines. It must be raw code.
- If you use METHOD C (SEARCH/REPLACE), the SEARCH block MUST contain EXACTLY 3-5 lines from the original file. The REPLACE block contains the new lines. The markers MUST be exactly <<<<<<< SEARCH, =======, and >>>>>>>.
- **CRITICAL: Do NOT output partial raw code snippets without SEARCH/REPLACE or unified diff markers. The parser strictly rejects any ambiguous snippet that lacks markers for an existing file. You MUST use METHOD C (SEARCH/REPLACE) or METHOD A (unified diff) for partial edits.**

GO STRUCT EMBEDDING RULES (COMPILER SAFETY)
- Embed types by placing the type name on its own line inside the struct. Do NOT use a named field with the same name as the type.
- CORRECT: place jwt.StandardClaims alone on a line.
- INCORRECT: jwt.StandardClaims jwt.StandardClaims.

RECOVERY RULES
- If a compilation error persists, immediately abandon METHOD A (diffs) and perform a full-file rewrite using METHOD B (FILE:).

DO NOT MIX COMMANDS AND CODE DIFFS
- A 'SHELL_EXEC' task must ONLY contain actual executable shell commands (e.g., "go test ./...", "docker-compose up").
- NEVER wrap, prefix, or output code modifications (diffs, file contents) inside a SHELL_EXEC block or under a command tag.
- If you are applying a patch, you MUST use METHOD A (diff), METHOD B (FILE:), or METHOD C (SEARCH/REPLACE) exclusively.
- Outputting diff blocks disguised as shell commands will corrupt the terminal environment.`
}
