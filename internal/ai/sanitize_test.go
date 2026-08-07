package ai

import (
	"strings"
	"testing"
)

// TestVisibleCompletion_StripsThinkingKeepsCode proves reasoning tokens never
// leak into the parseable completion: a response carrying real code alongside
// a <thought> block returns only the code.
func TestVisibleCompletion_StripsThinkingKeepsCode(t *testing.T) {
	raw := "<thought>consider the design carefully</thought>\n```html:index.html\n<h1>Portfolio</h1>\n```\n"
	got := VisibleCompletion(raw)
	if strings.Contains(got, "consider the design") {
		t.Fatalf("reasoning leaked into visible content: %q", got)
	}
	if !strings.Contains(got, "<h1>Portfolio</h1>") {
		t.Fatalf("code lost from visible content: %q", got)
	}
}

// TestVisibleCompletion_AllThinkingFallsBack proves a completion that is
// ENTIRELY a thinking block (some models emit their whole answer there) still
// surfaces the text so the answer survives.
func TestVisibleCompletion_AllThinkingFallsBack(t *testing.T) {
	got := VisibleCompletion("<think>the whole answer</think>")
	if got != "the whole answer" {
		t.Fatalf("got %q, want %q", got, "the whole answer")
	}
}

// TestVisibleCompletion_StripsSentinels proves the reasoning sentinel is
// consumed, never emitted.
func TestVisibleCompletion_StripsSentinels(t *testing.T) {
	got := VisibleCompletion("\x00RSNG\x00plan\x00RSNG\x00")
	if got != "plan" {
		t.Fatalf("got %q, want %q", got, "plan")
	}
}

// TestSanitizeResponse_StripsSentinelsAndThought proves both marker families
// are removed from an assembled response.
func TestSanitizeResponse_StripsSentinelsAndThought(t *testing.T) {
	got := SanitizeResponse("\x00RSNG\x00x\x00RSNG\x00<thought>y</thought>code")
	if got != "code" {
		t.Fatalf("got %q, want %q", got, "code")
	}
}

// TestCompletionStatsOf proves the content/reasoning split is measurable so
// token-loss audits (raw tokens vs visible code volume) can be surfaced.
func TestCompletionStatsOf(t *testing.T) {
	s := CompletionStatsOf("<thought>reasoning</thought>content")
	if s.ContentLen != len("content") {
		t.Errorf("content len = %d, want %d", s.ContentLen, len("content"))
	}
	if s.ReasoningLen != len("reasoning") {
		t.Errorf("reasoning len = %d, want %d", s.ReasoningLen, len("reasoning"))
	}
}
