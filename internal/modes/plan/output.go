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

// ValidatePlanOutput parses and validates LLM output against the rigid schema.
// Returns structured result with valid blocks and any schema violations.
func ValidatePlanOutput(content string) *ValidationResult {
	lines := strings.Split(content, "\n")
	result := &ValidationResult{Valid: true}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
			continue
		}

		matches := planBlockRegex.FindStringSubmatch(trimmed)
		if matches == nil {
			if strings.HasPrefix(trimmed, "- [") {
				result.Invalid = append(result.Invalid, InvalidLine{
					LineNumber: i + 1,
					Content:    trimmed,
					Reason:     "malformed task block — must match: - [ ] TYPE: Target | Rationale",
				})
			}
			continue
		}

		block := OutputBlock{
			Checked:    matches[1] == "x",
			Type:       matches[2],
			Target:     matches[3],
			Rationale:  matches[4],
			LineNumber: i + 1,
			RawLine:    trimmed,
		}
		result.Blocks = append(result.Blocks, block)
	}

	if len(result.Invalid) > 0 {
		result.Valid = false
	}
	return result
}

// FormatValidationError produces a human-readable error for TUI display.
func FormatValidationError(v *ValidationResult) string {
	if v.Valid {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("plan output schema violation: %d invalid line(s)\n", len(v.Invalid)))
	for _, inv := range v.Invalid {
		b.WriteString(fmt.Sprintf("  L%d: %s\n  └─ %s\n", inv.LineNumber, inv.Content, inv.Reason))
	}
	b.WriteString("regeneration required — output must conform to the rigid schema")
	return b.String()
}

// CollapsePlanSections strips headers and prose, keeping only task blocks.
func CollapsePlanSections(content string) string {
	var b strings.Builder
	for _, line := range strings.Split(content, "\n") {
		if planBlockRegex.MatchString(line) {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// IsValidTaskLine checks if a single line matches the task block schema.
func IsValidTaskLine(line string) bool {
	return planBlockRegex.MatchString(strings.TrimSpace(line))
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
