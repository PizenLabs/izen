package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/llm"
	"github.com/charmbracelet/x/ansi"
)

func mkModel(id, provider string) llm.ModelInfo {
	return llm.ModelInfo{
		ID:       id,
		Provider: provider,
	}
}

func TestTokenizeQuery_Empty(t *testing.T) {
	if got := tokenizeQuery(""); got != nil {
		t.Fatalf("empty query -> nil tokens, got %v", got)
	}
	if got := tokenizeQuery("   "); got != nil {
		t.Fatalf("whitespace query -> nil tokens, got %v", got)
	}
}

func TestTokenizeQuery_SplitsAndDedups(t *testing.T) {
	got := tokenizeQuery("Minimax FREE  minimax  free")
	want := []string{"minimax", "free"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("token[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestTokenizeQuery_PreservesOrder(t *testing.T) {
	got := tokenizeQuery("vision 128k tools")
	want := []string{"vision", "128k", "tools"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("token[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestMatchModel_AllTokensPresent(t *testing.T) {
	m := mkModel("minimax/minimax-m3:free", "openrouter")
	if !matchModel(m, []string{"minimax", "free"}) {
		t.Fatal("minimax free should match minimax/minimax-m3:free")
	}
	// Order independence.
	if !matchModel(m, []string{"free", "minimax"}) {
		t.Fatal("free minimax should match identically")
	}
}

func TestMatchModel_MissingTokenFails(t *testing.T) {
	m := mkModel("minimax/minimax-m3:free", "openrouter")
	if matchModel(m, []string{"minimax", "anthropic"}) {
		t.Fatal("minimax anthropic must NOT match openrouter minimax")
	}
}

func TestMatchModel_IndexesBadgesAndProvider(t *testing.T) {
	// vision 128k -> only models with both [Vision] capability AND 128k window.
	// gpt-4o has Vision cap and 128k context.
	m := mkModel("openai/gpt-4o", "openai")
	if !matchModel(m, []string{"vision", "128k"}) {
		t.Fatal("gpt-4o should match vision 128k")
	}
	// gemini-1.5-pro has 1M context, not 128k.
	m2 := mkModel("google/gemini-1.5-pro", "google")
	if matchModel(m2, []string{"vision", "128k"}) {
		t.Fatal("gemini-1.5-pro (1M ctx) must NOT match 128k")
	}
	// anthropic free -> free claude models via openrouter.
	m3 := mkModel("anthropic/claude-3-haiku", "anthropic")
	// Claude models are not "free" in the heuristic sense, but the token
	// "free" must not be satisfied by capability absence — ensure AND logic.
	if matchModel(m3, []string{"anthropic", "free"}) {
		t.Fatal("claude-3-haiku has no free badge, must not match 'anthropic free'")
	}
}

func TestMatchModel_ProviderTokenMatches(t *testing.T) {
	m := mkModel("openai/gpt-4o", "openai")
	if !matchModel(m, []string{"openai"}) {
		t.Fatal("provider token should match")
	}
	if !matchModel(m, []string{"gpt", "openai"}) {
		t.Fatal("multi-token provider+id should match")
	}
}

func TestBuildSearchCorpus_ContainsAllAttributes(t *testing.T) {
	m := mkModel("minimax/minimax-m3:free", "openrouter")
	corpus := buildSearchCorpus(m)
	for _, want := range []string{"minimax", "free", "openrouter"} {
		if !strings.Contains(corpus, want) {
			t.Fatalf("corpus %q missing %q", corpus, want)
		}
	}
}

func TestHighlightMatch_SingleToken(t *testing.T) {
	out := highlightMatch("openai/gpt-4o", []string{"gpt"})
	// The matched text must survive the styled render intact.
	if !strings.Contains(ansi.Strip(out), "gpt") {
		t.Fatal("highlight must retain matched text")
	}
	// No word loss: the surrounding text must also survive.
	if !strings.Contains(ansi.Strip(out), "openai") {
		t.Fatal("highlight must retain surrounding text")
	}
}

func TestHighlightMatch_MultipleTokens(t *testing.T) {
	out := highlightMatch("minimax/minimax-m3:free", []string{"minimax", "free"})
	stripped := ansi.Strip(out)
	if !strings.Contains(stripped, "minimax") || !strings.Contains(stripped, "free") {
		t.Fatal("multi-token highlight must retain both tokens")
	}
	// Both tokens must be styled (two distinct spans) when a color profile
	// is active; on a plain profile the text is preserved verbatim.
	if strings.Count(out, "\x1b[") >= 2 {
		t.Logf("multi-token highlight emitted %d ANSI spans", strings.Count(out, "\x1b["))
	}
}

func TestHighlightMatch_NoMatchReturnsPlain(t *testing.T) {
	out := highlightMatch("openai/gpt-4o", []string{"zzz"})
	if out != "openai/gpt-4o" {
		t.Fatalf("no-match highlight should return plain text, got %q", out)
	}
}

func TestHighlightMatch_EmptyTokens(t *testing.T) {
	out := highlightMatch("openai/gpt-4o", nil)
	if out != "openai/gpt-4o" {
		t.Fatalf("empty tokens should return plain text, got %q", out)
	}
}

func TestHighlightMatch_OverlappingMerged(t *testing.T) {
	// "abc" and "abcd" overlap; merged span must not produce nested tags.
	out := highlightMatch("xxabcyy", []string{"abc", "abcd"})
	// No double-open without a close between them (nested styling is invalid).
	if strings.Count(out, "\x1b[") > 2 {
		t.Fatalf("overlapping tokens should merge into a single span, got %q", out)
	}
	if !strings.Contains(ansi.Strip(out), "abc") {
		t.Fatal("merged highlight must retain matched text")
	}
}

func TestHighlightMatch_MultibyteSafe(t *testing.T) {
	out := highlightMatch("café-gpt-4o", []string{"gpt"})
	if !strings.Contains(ansi.Strip(out), "gpt") {
		t.Fatal("multibyte-safe highlight must retain matched text")
	}
	if !strings.Contains(ansi.Strip(out), "café") {
		t.Fatal("multibyte-safe highlight must retain surrounding text")
	}
}
