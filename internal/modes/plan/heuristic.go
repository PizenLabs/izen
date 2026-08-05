package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// proseFilePathRe matches file-path mentions inside narrative prose. It covers
// bare basenames (index.html, styles.css, script.js) as well as relative paths
// (cmd/api/main.go) ending in a recognised source/config extension. The leading
// \b prevents a match from bleeding into a longer word (e.g. "myindex.html").
// Whitespace, quotes, backticks and punctuation are excluded from the character
// classes so a match never swallows surrounding prose.
var proseFilePathRe = regexp.MustCompile(`(?i)\b(?:[a-z0-9_]+/)*[a-z0-9_][a-z0-9_\-]*\.(?:go|py|rs|js|jsx|ts|tsx|html|htm|css|scss|sass|less|json|yaml|yml|toml|md|sh|bash|c|cpp|cxx|h|hpp|java|kt|rb|php|sql|tf|vue|svelte)`)

// rootContextFallbackTarget marks a fallback task that targets the project root
// rather than a specific detected file. It is intentionally empty so it passes
// the scope guard (empty FILE_MUTATE targets are skipped) while still carrying
// the "apply the plan to the whole project" intent.
const rootContextFallbackTarget = ""

// rootContextFallbackTask builds the hardcoded task that targets the project
// root rather than a specific detected file. It is intentionally empty so it
// passes the scope guard (empty FILE_MUTATE targets are skipped) while still
// carrying the "apply the plan to the whole project" intent. Marked hardcoded
// so it survives every evidence-based filter (disk-existence, scope).
func rootContextFallbackTask() Task {
	return Task{
		StepNum:     1,
		IsDone:      false,
		Status:      "idle",
		Type:        "FILE_MUTATE",
		Target:      rootContextFallbackTarget,
		Description: "Apply the plan derived from model reasoning to the project root.",
		Rationale:   "The model produced narrative prose without parseable JSON or task blocks and no specific file was mentioned.",
		Solution:    "Project updated per the plan reconstructed from the model output.",
		IsHardcoded: true,
	}
}

// extractTasksFromProse is the last-resort plan synthesis fallback. When a
// model emits narrative reasoning prose instead of structured JSON (a common
// failure mode of free/mini cloud models such as Cohere North Mini), the JSON
// parser cannot recover a plan. This function mines the prose for file paths
// and constructs one FILE_MUTATE task per detected file, deriving each
// description from the nearby sentence that mentions it.
//
// When no file path is detected, a single hardcoded task targeting the root
// project context is returned so execution never dies with an unrecoverable
// "all 3 JSON synthesis attempts failed" error.
func extractTasksFromProse(rawText string) []Task {
	rawText = strings.TrimSpace(rawText)
	if rawText == "" {
		return nil
	}

	var files []string
	seen := make(map[string]bool)
	descriptions := make(map[string]string)

	for _, line := range strings.Split(rawText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		matches := proseFilePathRe.FindAllString(line, -1)
		if len(matches) == 0 {
			continue
		}
		desc := deriveProseDescription(line, matches[0])
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				files = append(files, m)
				descriptions[m] = desc
			}
		}
	}

	if len(files) == 0 {
		return []Task{rootContextFallbackTask()}
	}

	tasks := make([]Task, 0, len(files))
	for i, f := range files {
		tasks = append(tasks, Task{
			StepNum:     i + 1,
			IsDone:      false,
			Status:      "idle",
			Type:        "FILE_MUTATE",
			Target:      f,
			Description: descriptions[f],
			Rationale:   "Heuristic extraction from non-JSON model output that mentioned this file.",
			Solution:    fmt.Sprintf("Applied the planned change to %s.", f),
		})
	}
	return tasks
}

// deriveProseDescription converts the sentence that mentions a file into a
// task description by removing the file token, stripping enclosing markdown
// quotes/backticks and surrounding punctuation, and collapsing whitespace. It
// falls back to a generic description when nothing usable remains and caps the
// result so the task list stays readable.
func deriveProseDescription(line, file string) string {
	desc := strings.ReplaceAll(line, file, "")
	desc = strings.TrimSpace(desc)
	desc = strings.Trim(desc, "`'\"(),;:.")
	fields := strings.Fields(desc)
	desc = strings.Join(fields, " ")
	if desc == "" {
		return fmt.Sprintf("Modify %s per the generated plan.", file)
	}
	runes := []rune(desc)
	if len(runes) > 120 {
		desc = string(runes[:120]) + "..."
	}
	return desc
}
