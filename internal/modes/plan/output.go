package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// BlockPattern defines the non-negotiable schema for LLM plan output.
// Format: - [ ] TYPE: Target | Rationale
// Where TYPE is one of: FILE_MUTATE, SHELL_EXEC, GIT_ACTION
var planBlockRegex = regexp.MustCompile(`^- \[([ x])\] (FILE_MUTATE|SHELL_EXEC|GIT_ACTION): (.+?) \| (.+)$`)

// validTypes is the set of allowed task types.
var validTypes = map[string]bool{
	"FILE_MUTATE": true,
	"SHELL_EXEC":  true,
	"GIT_ACTION":  true,
}

// OutputBlock represents a single validated task block from the LLM output.
type OutputBlock struct {
	Checked    bool
	Type       string
	Target     string
	Rationale  string
	LineNumber int
	RawLine    string
}

// ValidationResult holds the parsed and validated blocks plus any invalid lines.
type ValidationResult struct {
	Blocks  []OutputBlock
	Invalid []InvalidLine
	Valid   bool
}

// InvalidLine represents a line that failed schema validation.
type InvalidLine struct {
	LineNumber int
	Content    string
	Reason     string
}

// mergeFragmentedLines pre-processes raw lines to stitch multi-line task
// fragments that smaller SLMs produce (e.g. verbose TYPE on L1, rationale
// continuation on L2).
func mergeFragmentedLines(lines []string) []string {
	merged := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		body := stripAnyListPrefix(line)
		if i+1 < len(lines) && isVerboseTaskHeader(body) && !strings.Contains(body, "| Rationale:") {
			next := strings.TrimSpace(lines[i+1])
			if cont := continuationText(next); cont != "" {
				line = line + " | Rationale: " + cont
				i++
			}
		}
		merged = append(merged, line)
	}
	return merged
}

// isVerboseTaskHeader returns true if body uses verbose key-value format
// "TYPE: X | Target: Y".
func isVerboseTaskHeader(body string) bool {
	return strings.HasPrefix(body, "TYPE:") && strings.Contains(body, "| Target:")
}

// continuationText extracts continuation prose from a candidate line.
// Returns empty string if the line is not a continuation.
func continuationText(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	body := stripAnyListPrefix(trimmed)
	for _, kw := range []string{"TYPE:", "FILE_MUTATE:", "SHELL_EXEC:", "GIT_ACTION:"} {
		if strings.Contains(body, kw) {
			return ""
		}
	}
	return body
}

// stripAnyListPrefix removes a leading markdown list prefix from s.
func stripAnyListPrefix(s string) string {
	for _, p := range []string{"- [x] ", "- [ ] ", "* ", "- "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

// convertVerboseToCompact converts verbose key-value format to compact format.
//
//	Input:  "TYPE: FILE_MUTATE | Target: path | Rationale: desc"
//	Output: "FILE_MUTATE: path | desc"
func convertVerboseToCompact(body string) string {
	if !strings.HasPrefix(body, "TYPE:") {
		return body
	}

	rest := strings.TrimSpace(strings.TrimPrefix(body, "TYPE:"))

	pipeIdx := strings.Index(rest, " | ")
	if pipeIdx == -1 {
		return body
	}
	taskType := strings.TrimSpace(rest[:pipeIdx])
	rest = rest[pipeIdx+3:]

	const tgtPrefix = "Target:"
	tgtIdx := strings.Index(rest, tgtPrefix)
	if tgtIdx == -1 {
		return body
	}
	afterTarget := strings.TrimSpace(rest[tgtIdx+len(tgtPrefix):])

	var target, rationale string
	const ratPrefix = "| Rationale:"
	ratIdx := strings.Index(afterTarget, ratPrefix)
	if ratIdx != -1 {
		target = strings.TrimSpace(afterTarget[:ratIdx])
		rationale = strings.TrimSpace(afterTarget[ratIdx+len(ratPrefix):])
	} else {
		target = strings.TrimSpace(afterTarget)
	}

	if rationale != "" {
		return taskType + ": " + target + " | " + rationale
	}
	return taskType + ": " + target
}

// ValidatePlanOutput parses and validates LLM output against the rigid schema.
// Returns structured result with valid blocks and any schema violations.
func ValidatePlanOutput(content string) *ValidationResult {
	lines := mergeFragmentedLines(strings.Split(content, "\n"))
	result := &ValidationResult{Valid: true}

	for i, line := range lines {
		normalized, skip, reason := normalizeLine(line)
		if skip {
			continue
		}

		matches := planBlockRegex.FindStringSubmatch(normalized)
		if matches == nil {
			if reason == "" {
				reason = "malformed task block — must match: - [ ] TYPE: Target | Rationale"
			}
			result.Invalid = append(result.Invalid, InvalidLine{
				LineNumber: i + 1,
				Content:    strings.TrimSpace(line),
				Reason:     reason,
			})
			continue
		}

		block := OutputBlock{
			Checked:    matches[1] == "x",
			Type:       matches[2],
			Target:     matches[3],
			Rationale:  matches[4],
			LineNumber: i + 1,
			RawLine:    strings.TrimSpace(line),
		}
		result.Blocks = append(result.Blocks, block)
	}

	if len(result.Invalid) > 0 {
		result.Valid = false
	}
	return result
}

// normalizeLine applies resilient normalization to LLM output lines.
// Steps: strip markdown list artifacts, convert verbose key-value format,
// strip quotes/backticks from targets, apply graceful fallback for
// FILE_MUTATE lines missing Rationale.
// Returns (normalized line, skip bool, error reason string).
func normalizeLine(line string) (string, bool, string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", true, ""
	}
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
		return "", true, ""
	}

	var checkboxPrefix string
	body := trimmed

	switch {
	case strings.HasPrefix(body, "- [x] "):
		checkboxPrefix = "- [x] "
		body = strings.TrimPrefix(body, "- [x] ")
	case strings.HasPrefix(body, "- [ ] "):
		checkboxPrefix = "- [ ] "
		body = strings.TrimPrefix(body, "- [ ] ")
	case strings.HasPrefix(body, "* "):
		checkboxPrefix = "- [ ] "
		body = strings.TrimPrefix(body, "* ")
	case strings.HasPrefix(body, "- "):
		checkboxPrefix = "- [ ] "
		body = strings.TrimPrefix(body, "- ")
	default:
		hasType := false
		for typ := range validTypes {
			if strings.Contains(trimmed, typ+":") || strings.Contains(trimmed, "TYPE:") {
				hasType = true
				break
			}
		}
		if !hasType {
			return "", true, ""
		}
		checkboxPrefix = "- [ ] "
	}

	body = convertVerboseToCompact(body)

	colonIdx := strings.Index(body, ":")
	if colonIdx == -1 {
		return trimmed, false, "missing colon separator"
	}

	taskType := strings.TrimSpace(body[:colonIdx])
	rest := strings.TrimSpace(body[colonIdx+1:])

	if !validTypes[taskType] {
		return trimmed, false, "invalid task type: " + taskType
	}

	var target, rationale string
	pipeIdx := strings.Index(rest, " | ")
	if pipeIdx != -1 {
		target = strings.TrimSpace(rest[:pipeIdx])
		rationale = strings.TrimSpace(rest[pipeIdx+3:])
	} else {
		target = strings.TrimSpace(rest)
		if taskType == "FILE_MUTATE" {
			rationale = "Code mutation requested by system plan"
		}
	}

	for _, q := range []string{"'", "\"", "`"} {
		if strings.HasPrefix(target, q) && strings.HasSuffix(target, q) {
			target = target[len(q) : len(target)-len(q)]
			break
		}
	}

	normalized := fmt.Sprintf("%s%s: %s | %s", checkboxPrefix, taskType, target, rationale)
	return normalized, false, ""
}

// FormatValidationError produces a human-readable error for TUI display.
func FormatValidationError(v *ValidationResult) string {
	if v.Valid {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plan output schema violation: %d invalid line(s)\n", len(v.Invalid))
	for _, inv := range v.Invalid {
		fmt.Fprintf(&b, "  L%d: %s\n  └─ %s\n", inv.LineNumber, inv.Content, inv.Reason)
	}
	b.WriteString("regeneration required — output must conform to the rigid schema")
	return b.String()
}

// CollapsePlanSections strips headers and prose, keeping only task blocks.
func CollapsePlanSections(content string) string {
	var b strings.Builder
	for _, line := range mergeFragmentedLines(strings.Split(content, "\n")) {
		if normalized, skip, _ := normalizeLine(line); !skip {
			if planBlockRegex.MatchString(normalized) {
				b.WriteString(normalized)
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// IsValidTaskLine checks if a single line matches the task block schema.
func IsValidTaskLine(line string) bool {
	normalized, skip, _ := normalizeLine(line)
	if skip {
		return false
	}
	return planBlockRegex.MatchString(normalized)
}

// PlaceholderPathPatterns contains literal strings that indicate placeholder paths.
var PlaceholderPathPatterns = []string{
	"relative/path/",
	"file.go",
	"file_test.go",
	"path/to/",
	"<path>",
	"<file>",
	"your-file.go",
	"example.go",
	"test.go",
	"somefile.go",
}

// DocumentationFilePatterns lists file-name substrings that identify
// documentation/non-source artifacts. The /plan engine MUST NEVER emit a task
// that mutates these — compilation/dependency failures are resolved via go.mod
// edits or SHELL_EXEC dependency commands, never by editing docs.
var DocumentationFilePatterns = []string{
	"readme.md",
	"readme",
	"docs/",
	"doc/",
	"changelog.md",
	"changelog",
	"contributing.md",
	"license",
	"code_of_conduct.md",
	"security.md",
	".md",
}

// IsDocumentationTarget reports whether a task target (file path or shell
// command) resolves to a documentation/non-source artifact. For shell commands
// (SHELL_EXEC) it inspects whether the command writes to such a file; for file
// targets it matches against DocumentationFilePatterns. This is the primary
// anti-escape gate that prevents the local model from "fixing" compile/dep
// failures by silently patching README.md or other docs.
func IsDocumentationTarget(target string, taskType string) bool {
	if taskType == "SHELL_EXEC" {
		// Only block shell commands that explicitly write to doc files
		// (e.g. redirection into README.md). Plain build/dep commands are fine.
		lower := strings.ToLower(target)
		for _, p := range DocumentationFilePatterns {
			if p == ".md" || p == "docs/" || p == "doc/" {
				continue // too broad for command inspection
			}
			if strings.Contains(lower, "> "+p) || strings.Contains(lower, ">> "+p) {
				return true
			}
		}
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(target))
	for _, p := range DocumentationFilePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// ValidateTaskTarget checks if a task target is valid or a placeholder.
// Returns (isValid, isPlaceholder) where:
// - isValid: true if the target is a valid, non-placeholder path
// - isPlaceholder: true if the target contains placeholder patterns
func ValidateTaskTarget(target string, taskType string) (isValid bool, isPlaceholder bool) {
	if target == "" {
		return false, false
	}

	// Shell commands are valid if they're not empty
	if taskType == "SHELL_EXEC" {
		return len(strings.TrimSpace(target)) > 0, false
	}

	// Check for placeholder patterns
	lowerTarget := strings.ToLower(target)
	for _, pattern := range PlaceholderPathPatterns {
		if strings.Contains(lowerTarget, strings.ToLower(pattern)) {
			return false, true
		}
	}

	return true, false
}

// ValidateTask validates a task for placeholder paths and other issues.
// Returns an error if the task contains invalid placeholder paths.
func ValidateTask(t Task) error {
	isValid, isPlaceholder := ValidateTaskTarget(t.Target, t.Type)

	if isPlaceholder {
		return fmt.Errorf("[BUILD ABORTED] Detected invalid placeholder paths in the execution plan. Task %d target '%s' contains placeholder pattern. Please re-run /plan", t.StepNum, t.Target)
	}

	if !isValid {
		return fmt.Errorf("[BUILD ABORTED] Task %d has invalid or empty target: '%s'", t.StepNum, t.Target)
	}

	return nil
}

// ValidateAllTasks validates all tasks in a slice and returns the first error found.
// Returns nil if all tasks are valid.
func ValidateAllTasks(tasks []Task) error {
	for _, t := range tasks {
		if err := ValidateTask(t); err != nil {
			return err
		}
	}
	return nil
}

const SchemaTemplate = `- [ ] FILE_MUTATE: path/to/file.go | describe the change
- [ ] SHELL_EXEC: go build ./... | reason for running this command
- [ ] GIT_ACTION: commit -m "message" | why this commit is needed`

// SchemaInstruction returns the schema definition block for system prompts.
func SchemaInstruction() string {
	return fmt.Sprintf(`You MUST output ONLY task blocks. Each line MUST follow this EXACT syntax:

  - [ ] <TYPE>: <Target> | <Rationale>

ALLOWED TYPES (case-sensitive):
  FILE_MUTATE — Target is the exact file path (relative to project root)
  SHELL_EXEC  — Target is the exact shell command to execute
  GIT_ACTION  — Target is the git operation

RULES:
  1. Every line MUST start with "- [ ]" or "- [x]".
  2. No introductory text, no concluding text, no markdown code fences.
  3. Use "|" (pipe) to separate Target from Rationale.
  4. Target paths MUST be relative to the project root.
  5. No speculative paths — only reference files in the directory tree above.

Example:
%s`,
		SchemaTemplate)
}
