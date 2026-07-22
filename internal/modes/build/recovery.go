package build

import "strings"

// StrictRecoverySystemPrompt returns a strict system prompt for the auto-recovery
// loop that prohibits conversational intros/outros and requires the LLM response
// to strictly contain executable diffs or structured file blocks. The system
// boilerplate is eliminated so the patch parser never blocks on prose.
func StrictRecoverySystemPrompt() string {
	return "MODE: AUTO-RECOVERY — execute a targeted fix.\n\n" +
		"PURPOSE\n" +
		"- Apply the minimal code change to fix the compilation error.\n" +
		"- Output ONLY compilable code. No analysis, no explanations.\n\n" +
		"FORBIDDEN\n" +
		"- Do NOT output conversational text of any kind.\n" +
		"- Do NOT greet, summarize, or restate the problem.\n" +
		"- The first output token MUST be ```diff or FILE:. ZERO exceptions.\n\n" +
		"OUTPUT FORMAT\n" +
		"- Unified diff (```diff ... ```) for existing files.\n" +
		"- FILE: block for new files or full rewrites.\n" +
		"- No markdown outside code blocks.\n" +
		"- No conversational setup, no sign-off."
}

// stripNonPatchProsePrefixes are literal strings the LLM may emit before the
// actual patch content during auto-recovery. The StripNonPatchProse function
// scans for the first occurrence of any of these markers and drops everything
// before it.
var stripNonPatchProsePrefixes = []string{
	"```diff",
	"```go",
	"```patch",
	"FILE:",
	"--- a/",
	"+++ b/",
}

// StripNonPatchProse removes conversational preamble from an LLM response,
// keeping only content starting from the first valid patch marker (diff block,
// FILE: tag, or unified diff header). This ensures system boilerplate like
// "Understood. I will follow..." never blocks patch application during auto-
// recovery. If no patch marker is found, the original response is returned
// unchanged.
func StripNonPatchProse(response string) string {
	response = strings.TrimSpace(response)
	if response == "" {
		return response
	}

	// Find the earliest occurrence of any prefix marker.
	earliest := -1
	for _, p := range stripNonPatchProsePrefixes {
		idx := strings.Index(response, p)
		if idx >= 0 && (earliest < 0 || idx < earliest) {
			earliest = idx
		}
	}

	if earliest < 0 {
		return response
	}

	return strings.TrimSpace(response[earliest:])
}
