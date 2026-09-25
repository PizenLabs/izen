package session

import (
	"strings"
	"testing"
)

func TestSanitizeTitleDeterministicTruncation(t *testing.T) {
	long := "  Refactor   the session picker\nso that titles\tare deterministic and never raw timestamps  "
	got := SanitizeTitle(long)
	if !strings.HasPrefix(got, "Refactor the session picker so that") {
		t.Fatalf("unexpected title: %q", got)
	}
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("title must be single-line: %q", got)
	}
	if len([]rune(got)) > MaxTitleRunes {
		t.Fatalf("title length %d exceeds %d: %q", len([]rune(got)), MaxTitleRunes, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated title should end with an ellipsis: %q", got)
	}
	// Deterministic on repeat.
	if again := SanitizeTitle(long); again != got {
		t.Fatalf("SanitizeTitle not deterministic: %q vs %q", got, again)
	}
}

func TestSanitizeTitleShortAndEmpty(t *testing.T) {
	if got := SanitizeTitle("  fix   the bug  "); got != "fix the bug" {
		t.Fatalf("got %q, want %q", got, "fix the bug")
	}
	if got := SanitizeTitle("   \n\t "); got != "" {
		t.Fatalf("empty prompt should yield empty title, got %q", got)
	}
}

func TestSanitizeTitleStripsControlChars(t *testing.T) {
	got := SanitizeTitle("add\x00 a \x07feature")
	if got != "add a feature" {
		t.Fatalf("got %q, want %q", got, "add a feature")
	}
}

func TestHistoryHelpers(t *testing.T) {
	history := []Message{
		{Role: "system", Content: "boot"},
		{Role: "user", Content: "first question with details"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second question"},
	}
	if got := UserTurns(history); got != 2 {
		t.Fatalf("UserTurns = %d, want 2", got)
	}
	if got := LastUserPrompt(history); got != "second question" {
		t.Fatalf("LastUserPrompt = %q, want second question", got)
	}
	if got := EstimatedHistoryTokens(history); got <= 0 {
		t.Fatalf("EstimatedHistoryTokens = %d, want > 0", got)
	}
	if got := LastUserPrompt([]Message{{Role: "assistant", Content: "hi"}}); got != "" {
		t.Fatalf("LastUserPrompt without user turn = %q, want empty", got)
	}
}
