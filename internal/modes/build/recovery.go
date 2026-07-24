package build

import (
	"strings"

	"regexp"
)

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

// conversationalPatterns are regex patterns indicating the LLM output is
// conversational prose rather than executable code patches.
var conversationalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(understood|okay|ok|got it|i will|let me|here's|here is|the goal|the task|i'll|i will now|i understand|of course|certainly|absolutely|sure thing|yes,|no,)`),
	regexp.MustCompile(`(?i)(what is the goal|what should i|what would you|how can i|tell me what|please provide|i need you to|your goal|the objective is|you asked me to)`),
	regexp.MustCompile(`(?i)^(hello|hi|hey|greetings|welcome)`),
	regexp.MustCompile(`(?i)(let's start|let me begin|first,|firstly,|the first step)`),
	regexp.MustCompile(`(?i)(to implement|to fix|to resolve|to address|in order to|the purpose of)`),
}

// IsConversationalOutput reports whether the LLM response is purely
// conversational prose with no valid code patch markers. A conversational
// output lacks SEARCH/REPLACE, FILE_CREATE, and diff markers, and its
// content matches conversational greeting/rhetoric patterns.
func IsConversationalOutput(response string) bool {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return false
	}

	// Check for valid patch markers first — if any exist, the output is
	// at least partially actionable regardless of conversational preamble.
	patchMarkers := []string{
		"<<<<<<< SEARCH",
		"<<<<<<< FILE_CREATE:",
		"FILE_CREATE:",
		"SEARCH",
		"=======",
		">>>>>>>",
		"--- a/",
		"+++ b/",
		"```diff",
	}
	for _, m := range patchMarkers {
		if strings.Contains(trimmed, m) {
			return false
		}
	}
	// Check if the output has any markdown code fence (```), which indicates
	// code content even without the above markers.
	if strings.Contains(trimmed, "```") {
		return false
	}
	// Score conversational patterns: if ≥2 patterns match, it's conversational.
	score := 0
	for _, pat := range conversationalPatterns {
		if pat.MatchString(trimmed) {
			score++
			if score >= 2 {
				return true
			}
		}
	}
	return false
}
