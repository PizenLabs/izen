package commit

import (
	"regexp"
	"strings"
	"testing"
)

var subjectRegexp = regexp.MustCompile(`^[a-z]+\([a-z][a-z0-9._/-]*\): .+`)

func TestParseGeneratedMessage_LicenseDiff(t *testing.T) {
	raw := `feat(license): add MIT license file

- include full MIT license text
- set copyright holder placeholder`

	msg := ParseGeneratedMessage(raw)

	if !subjectRegexp.MatchString(msg.Subject) {
		t.Errorf("subject %q does not match conventional commit format", msg.Subject)
	}
	if !strings.Contains(msg.Subject, "license") {
		t.Errorf("subject %q should contain 'license' scope", msg.Subject)
	}
	if msg.Body == "" {
		t.Fatal("body must not be empty")
	}
	if !strings.HasPrefix(msg.Body, "- ") {
		t.Errorf("body should start with bullet, got %q", msg.Body)
	}
}

func TestParseGeneratedMessage_EmptyDiff(t *testing.T) {
	raw := ``
	msg := ParseGeneratedMessage(raw)

	if msg.Subject == "" {
		t.Fatal("subject must not be empty even for empty diff")
	}
	if msg.Body == "" {
		t.Fatal("body must not be empty even for empty diff")
	}
}

func TestParseGeneratedMessage_StripsMarkdownFences(t *testing.T) {
	raw := "```\nchore(deps): bump lodash version\n\n- update from 4.17.20 to 4.17.21\n```"
	msg := ParseGeneratedMessage(raw)

	if !subjectRegexp.MatchString(msg.Subject) {
		t.Errorf("subject %q does not match conventional commit format", msg.Subject)
	}
	if !strings.Contains(msg.Body, "update") {
		t.Errorf("body should contain bullet content, got %q", msg.Body)
	}
}

func TestParseGeneratedMessage_MultiLineBody(t *testing.T) {
	raw := `fix(api): normalize error response status codes

- return 400 for validation failures
- map internal errors to 500
- log unexpected errors before responding`

	msg := ParseGeneratedMessage(raw)

	if msg.Subject == "" {
		t.Fatal("subject must not be empty")
	}
	if !strings.Contains(msg.Subject, "api") {
		t.Errorf("subject %q should contain 'api' scope", msg.Subject)
	}
	bullets := strings.Split(msg.Body, "\n")
	if len(bullets) < 2 {
		t.Errorf("expected at least 2 bullets, got %d: %q", len(bullets), msg.Body)
	}
	for _, b := range bullets {
		if !strings.HasPrefix(b, "- ") {
			t.Errorf("bullet %q should start with '- '", b)
		}
	}
}

func TestParseGeneratedMessage_RefactorDiff(t *testing.T) {
	raw := `refactor(engine): replace processing pipeline

- switch from legacy to new implementation
- maintain backward compatibility`

	msg := ParseGeneratedMessage(raw)

	if !subjectRegexp.MatchString(msg.Subject) {
		t.Errorf("subject %q does not match conventional commit format", msg.Subject)
	}
	if !strings.Contains(msg.Subject, "engine") {
		t.Errorf("subject %q should contain 'engine' scope", msg.Subject)
	}
	if msg.Body == "" {
		t.Fatal("body must not be empty")
	}
}

func TestSanitizeSubject_StripsPeriod(t *testing.T) {
	result := SanitizeSubject("feat(ui): add dark mode toggle.")
	if strings.HasSuffix(result, ".") {
		t.Errorf("subject should not end with period: %q", result)
	}
	if !strings.Contains(result, "dark mode toggle") {
		t.Errorf("subject should contain summary text, got %q", result)
	}
}

func TestSanitizeSubject_LowercasesSummary(t *testing.T) {
	result := SanitizeSubject("feat(ui): Add dark mode")
	if !strings.Contains(result, "add dark mode") {
		t.Errorf("subject summary should be lowercase, got %q", result)
	}
}

func TestSanitizeSubject_TruncatesLongLines(t *testing.T) {
	long := "feat(ui): add a very long feature description that exceeds the maximum character limit enforced by the system"
	result := SanitizeSubject(long)
	if len(result) > MaxSubject {
		t.Errorf("subject length %d exceeds max %d: %q", len(result), MaxSubject, result)
	}
}

func TestSanitizeSubject_FallbackWhenNoColon(t *testing.T) {
	result := SanitizeSubject("no colon here")
	if !strings.HasPrefix(result, "chore(repo):") {
		t.Errorf("fallback subject should start with 'chore(repo):', got %q", result)
	}
}

func TestSanitizeBody_FiltersImplementationLeaks(t *testing.T) {
	lines := []string{
		"- use `internal/engine` resolver for edge cases",
		"- remove duplicated validation logic",
		"- add new function to process data",
	}
	result := SanitizeBody(lines)
	if strings.Contains(result, "resolver") {
		t.Errorf("body should filter 'resolver': %q", result)
	}
	if !strings.Contains(result, "remove duplicated") {
		t.Errorf("body should contain valid bullet: %q", result)
	}
}

func TestSanitizeBody_BulletFormat(t *testing.T) {
	lines := []string{"- add validation for empty inputs", "- handle timeout errors gracefully"}
	result := SanitizeBody(lines)
	bullets := strings.Split(result, "\n")
	for _, b := range bullets {
		if !strings.HasPrefix(b, "- ") {
			t.Errorf("bullet %q should start with '- '", b)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	diff := "diff --git a/LICENSE b/LICENSE"
	prompt := BuildPrompt(diff)
	if !strings.Contains(prompt, diff) {
		t.Errorf("prompt should contain diff, got %q", prompt)
	}
	if !strings.HasPrefix(prompt, "Generate") {
		t.Errorf("prompt should start with 'Generate', got %q", prompt)
	}
}

func TestCleanRawLLMOutput_NoFence(t *testing.T) {
	raw := "feat(license): add license\n\n- include mit text"
	lines := CleanRawLLMOutput(raw)
	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	if lines[0] != "feat(license): add license" {
		t.Errorf("first line should be subject, got %q", lines[0])
	}
}

func TestCleanRawLLMOutput_WithFence(t *testing.T) {
	raw := "```\nfeat(license): add license\n\n- include mit text\n```"
	lines := CleanRawLLMOutput(raw)
	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	if lines[0] != "feat(license): add license" {
		t.Errorf("first line should be stripped subject, got %q", lines[0])
	}
}
