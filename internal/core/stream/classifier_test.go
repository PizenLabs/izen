package stream

import (
	"strings"
	"testing"
)

// collectTokens runs the classifier over chunks and flushes, returning tokens.
func collectTokens(chunks ...string) []Token {
	c := NewClassifier()
	var tokens []Token
	emit := func(t Token) { tokens = append(tokens, t) }
	for _, ch := range chunks {
		c.Write(ch, emit)
	}
	c.Flush(emit)
	return tokens
}

func tokensText(tokens []Token, kind TokenKind) string {
	var sb strings.Builder
	for _, t := range tokens {
		if t.Kind == kind {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

func TestClassifierSeparatesContentFromThinking(t *testing.T) {
	tokens := collectTokens("Answer. <thought>reason one</thought> More.")
	if got := tokensText(tokens, TokenKindContent); got != "Answer.  More." {
		t.Errorf("content = %q", got)
	}
	if got := tokensText(tokens, TokenKindThinking); got != "reason one" {
		t.Errorf("thinking = %q", got)
	}
}

func TestClassifierIsThinkingState(t *testing.T) {
	c := NewClassifier()
	if c.IsThinking() {
		t.Error("IsThinking = true before any reasoning marker")
	}
	c.Write("<thought>", func(Token) {})
	if !c.IsThinking() {
		t.Error("IsThinking = false inside an open reasoning block")
	}
	c.Write("still thinking", func(Token) {})
	if !c.IsThinking() {
		t.Error("IsThinking = false while reasoning text streams")
	}
	c.Write("</thought>", func(Token) {})
	if c.IsThinking() {
		t.Error("IsThinking = true after the reasoning block closed")
	}
	c.Flush(func(Token) {})
	if c.IsThinking() {
		t.Error("IsThinking = true after Flush (must return to content state)")
	}
}

func TestClassifierMarkerSplitAcrossChunks(t *testing.T) {
	tokens := collectTokens("Hello <thou", "ght>internal chain</", "thought> world")
	if got := tokensText(tokens, TokenKindContent); got != "Hello  world" {
		t.Errorf("content = %q", got)
	}
	if got := tokensText(tokens, TokenKindThinking); got != "internal chain" {
		t.Errorf("thinking = %q", got)
	}
}

func TestClassifierReasoningSentinel(t *testing.T) {
	tokens := collectTokens("Lead \x00RSNG\x00deep think\x00RSNG\x00 Tail")
	if got := tokensText(tokens, TokenKindThinking); got != "deep think" {
		t.Errorf("thinking = %q", got)
	}
	if got := tokensText(tokens, TokenKindContent); got != "Lead  Tail" {
		t.Errorf("content = %q", got)
	}
}

func TestClassifierEmitsThinkingKindMetadata(t *testing.T) {
	c := NewClassifier()
	var kinds []TokenKind
	emit := func(t Token) { kinds = append(kinds, t.Kind) }
	c.Write("plain", emit)
	c.Write("<thought>reason</thought>", emit)
	c.Flush(emit)
	if len(kinds) != 2 {
		t.Fatalf("got %d tokens, want 2", len(kinds))
	}
	if kinds[0] != TokenKindContent || kinds[1] != TokenKindThinking {
		t.Errorf("kinds = %v, want [Content Thinking]", kinds)
	}
}

func TestClassifierEmptyTokensSkipped(t *testing.T) {
	c := NewClassifier()
	emitted := false
	c.Write("", func(Token) { emitted = true })
	c.Flush(func(Token) { emitted = true })
	if emitted {
		t.Error("empty writes produced tokens")
	}
}

func TestClassifierUnclosedThoughtFlushedAsThinking(t *testing.T) {
	// A stream ending inside an unclosed reasoning block keeps the dangling
	// tail in the thinking pipeline — it never leaks into content.
	tokens := collectTokens("<thought>never closed")
	if got := tokensText(tokens, TokenKindThinking); got != "never closed" {
		t.Errorf("thinking = %q, want the dangling reasoning tail", got)
	}
}
