package commit

import (
	"fmt"
	"regexp"
	"strings"
)

// SubjectRE validates conventional commit header format.
var SubjectRE = regexp.MustCompile(`^[a-z]+\([a-z][a-z0-9._/-]*\): .+`)

// BuildPrompt constructs the user-facing prompt payload from the git diff.
func BuildPrompt(diff string) string {
	return fmt.Sprintf("Generate a conventional commit message for these changes:\n\n%s", diff)
}

// ParseGeneratedMessage parses raw LLM output into a CommitMessage.
// It strips markdown codeblocks, extracts the subject from the first
// non-empty line, and sanitizes the body bullets.
func ParseGeneratedMessage(raw string) CommitMessage {
	lines := CleanRawLLMOutput(raw)

	var subject string
	var bodyLines []string
	foundSubject := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !foundSubject {
			subject = SanitizeSubject(trimmed)
			foundSubject = true
		} else {
			bodyLines = append(bodyLines, trimmed)
		}
	}

	if subject == "" {
		subject = "chore(repo): update repository state"
	}

	body := SanitizeBody(bodyLines)
	if body == "" {
		body = "- apply repository changes"
	}

	return CommitMessage{Subject: subject, Body: body}
}
