package commit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	MaxSubject     = 48
	MaxBodyBullets = 4
)

// ForbiddenEndings matches weak hanging trailing particles.
var ForbiddenEndings = map[string]bool{
	"with": true, "for": true, "to": true, "into": true, "using": true,
	"include": true, "includes": true, "including": true, "and": true,
	"or": true, "via": true, "through": true, "by": true,
}

// CommitMessage holds the structured subject and sanitized body.
type CommitMessage struct {
	Subject string
	Body    string
}

// GetStagedDiff extracts the staged git changes up to 180 lines, matching the reference script.
func GetStagedDiff() (string, string, error) {
	// Check status porcelain
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	if err != nil {
		return "", "", err
	}
	statusStr := strings.TrimSpace(string(statusOut))
	if statusStr == "" {
		return "", "", nil
	}

	// Fetch unified cached diff context
	diffCmd := exec.Command("sh", "-c", "git diff --cached -w -U3 | head -n 180")
	diffOut, _ := diffCmd.Output()
	diffStr := strings.TrimSpace(string(diffOut))

	if diffStr == "" {
		diffStr = statusStr
	}

	return diffStr, statusStr, nil
}

// IsIncompleteSubject evaluates if the trailing token terminates weakly.
func IsIncompleteSubject(summary string) bool {
	summary = strings.ToLower(strings.TrimSpace(summary))
	words := strings.Fields(summary)
	if len(words) == 0 {
		return false
	}
	return ForbiddenEndings[words[len(words)-1]]
}

// SanitizeSubject enforces lowercase semantics, enforces max lengths, and truncates incomplete parts.
func SanitizeSubject(line string) string {
	line = strings.TrimSpace(line)
	// Strip enclosing quotes and trailing periods
	line = strings.Trim(line, "\"`'")
	line = strings.TrimSuffix(line, ".")

	if !strings.Contains(line, ":") {
		return "chore(repo): update repository state"
	}

	parts := strings.SplitN(line, ":", 2)
	prefix := strings.TrimSpace(parts[0])
	summary := strings.TrimSpace(parts[1])
	summary = strings.TrimSuffix(summary, ".")

	if summary != "" {
		runes := []rune(summary)
		runes[0] = unicode.ToLower(runes[0])
		summary = string(runes)
	}

	// Purge forbidden hanging particles loop
	words := strings.Fields(summary)
	for len(words) > 0 && ForbiddenEndings[strings.ToLower(words[len(words)-1])] {
		words = words[:len(words)-1]
	}
	summary = strings.Join(words, " ")

	for summary != "" && IsIncompleteSubject(summary) {
		w := strings.Fields(summary)
		if len(w) == 0 {
			break
		}
		summary = strings.Join(w[:len(w)-1], " ")
	}

	if summary == "" {
		summary = "update repository state"
	}

	subject := fmt.Sprintf("%s: %s", prefix, summary)

	// Smart truncation at word boundary if over maximum limit
	if len(subject) > MaxSubject {
		cut := strings.LastIndex(subject[:MaxSubject], " ")
		if cut < 20 {
			cut = MaxSubject
		}
		subject = strings.TrimSpace(subject[:cut])

		truncWords := strings.Fields(subject)
		for len(truncWords) > 0 && ForbiddenEndings[strings.ToLower(truncWords[len(truncWords)-1])] {
			truncWords = truncWords[:len(truncWords)-1]
		}
		subject = strings.Join(truncWords, " ")

		pParts := strings.SplitN(subject, ":", 2)
		pPrefix := strings.TrimSpace(pParts[0])
		pSummary := ""
		if len(pParts) > 1 {
			pSummary = strings.TrimSpace(pParts[1])
		}

		for pSummary != "" && IsIncompleteSubject(pSummary) {
			w := strings.Fields(pSummary)
			pSummary = strings.Join(w[:len(w)-1], " ")
		}
		subject = fmt.Sprintf("%s: %s", pPrefix, pSummary)
	}

	return subject
}

// IsImplementationLeak filters out low-level code details via strict regex rules.
func IsImplementationLeak(line string) bool {
	patterns := []*regexp.Regexp{
		regexp.MustCompile("`.*?`"),
		regexp.MustCompile(`(?i)\bpass\s+\d+\b`),
		regexp.MustCompile(`(?i)\bfunction\b`),
		regexp.MustCompile(`(?i)\bresolver\b`),
		regexp.MustCompile(`(?i)\bedge\b`),
		regexp.MustCompile(`(?i)\bconstant\b`),
		regexp.MustCompile(`(?i)\benum\b`),
		regexp.MustCompile(`(?i)\bphase\b`),
		regexp.MustCompile(`(?i)\binternal\b`),
		regexp.MustCompile(`(?i)\bstruct\b`),
	}
	for _, p := range patterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

// SanitizeBody normalizes bullet lines, dropping semantic duplicates and leaked syntax details.
func SanitizeBody(lines []string) string {
	var cleaned []string
	conventionalScopeRegex := regexp.MustCompile(`(?i)^[a-z]+\(.+\):`)

	for _, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}

		// Strip list symbols
		text = strings.TrimLeft(text, "-*• ")
		text = strings.TrimSuffix(text, ".")
		text = strings.TrimSpace(text)

		if text == "" || IsImplementationLeak(text) || conventionalScopeRegex.MatchString(text) {
			continue
		}

		runes := []rune(text)
		runes[0] = unicode.ToLower(runes[0])
		text = string(runes)

		cleaned = append(cleaned, fmt.Sprintf("- %s", text))
		if len(cleaned) >= MaxBodyBullets {
			break
		}
	}
	return strings.Join(cleaned, "\n")
}

// BuildFallback safely counts affected items when LLM inference aborts.
func BuildFallback(status string) CommitMessage {
	count := len(strings.Split(status, "\n"))
	return CommitMessage{
		Subject: fmt.Sprintf("chore(repo): update %d files", count),
		Body:    "- apply repository changes",
	}
}

// CleanRawLLMOutput un-wraps Markdown blocks before pipeline processing.
func CleanRawLLMOutput(raw string) []string {
	cleaned := strings.TrimSpace(raw)
	// Remove outer codeblocks if appended by unstable model outputs
	reStart := regexp.MustCompile("(?i)^m*g*```[a-z]*\n?")
	cleaned = reStart.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	return strings.Split(cleaned, "\n")
}

// ExecuteCommit flushes the final structured text using a temporary manifest file descriptor.
func ExecuteCommit(msg CommitMessage) error {
	finalMessage := fmt.Sprintf("%s\n\n%s\n", msg.Subject, msg.Body)
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("izen-commit-%d.txt", time.Now().UnixNano()))

	if err := os.WriteFile(tmpFile, []byte(finalMessage), 0644); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	cmd := exec.Command("git", "commit", "-F", tmpFile)
	return cmd.Run()
}
