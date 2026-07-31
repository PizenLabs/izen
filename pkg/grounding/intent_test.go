package grounding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/recon"
)

func TestLevenshtein_Equal(t *testing.T) {
	if d := Levenshtein("hello", "hello"); d != 0 {
		t.Fatalf("expected 0, got %d", d)
	}
}

func TestLevenshtein_Empty(t *testing.T) {
	if d := Levenshtein("", "abc"); d != 3 {
		t.Fatalf("expected 3, got %d", d)
	}
	if d := Levenshtein("abc", ""); d != 3 {
		t.Fatalf("expected 3, got %d", d)
	}
}

func TestLevenshtein_Insert(t *testing.T) {
	if d := Levenshtein("cat", "cats"); d != 1 {
		t.Fatalf("expected 1, got %d", d)
	}
}

func TestLevenshtein_Substitute(t *testing.T) {
	if d := Levenshtein("cat", "cut"); d != 1 {
		t.Fatalf("expected 1, got %d", d)
	}
}

func TestLevenshtein_Delete(t *testing.T) {
	if d := Levenshtein("cats", "cat"); d != 1 {
		t.Fatalf("expected 1, got %d", d)
	}
}

func TestFuzzyMatcher_ExactMatch(t *testing.T) {
	fm := NewFuzzyMatcher(0.6)
	m := fm.Match("navigation")
	if m == nil || m.Keyword != "navigation" || m.Score != 1.0 {
		t.Fatalf("expected exact match, got %+v", m)
	}
}

func TestFuzzyMatcher_VariantMatch(t *testing.T) {
	fm := NewFuzzyMatcher(0.6)
	m := fm.Match("navi")
	if m == nil || m.Keyword != "navigation" {
		t.Fatalf("expected navigation, got %+v", m)
	}
}

func TestFuzzyMatcher_TypoMatch(t *testing.T) {
	fm := NewFuzzyMatcher(0.6)
	m := fm.Match("buton")
	if m == nil || m.Keyword != "button" {
		t.Fatalf("expected button, got %+v", m)
	}
}

func TestFuzzyMatcher_NoMatch(t *testing.T) {
	fm := NewFuzzyMatcher(0.9)
	m := fm.Match("xyzzy")
	if m != nil {
		t.Fatalf("expected nil match for low-similarity word, got %+v", m)
	}
}

func TestFuzzyMatcher_ShortWord(t *testing.T) {
	fm := NewFuzzyMatcher(0.6)
	m := fm.Match("x")
	if m != nil {
		t.Fatalf("expected nil for single char, got %+v", m)
	}
}

func TestTokenize_Basic(t *testing.T) {
	tokens := Tokenize("hello world")
	if len(tokens) != 2 || tokens[0] != "hello" || tokens[1] != "world" {
		t.Fatalf("expected [hello world], got %v", tokens)
	}
}

func TestTokenize_Punctuation(t *testing.T) {
	tokens := Tokenize("nav, btn, hdr")
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %v", tokens)
	}
}

func TestTokenize_Empty(t *testing.T) {
	tokens := Tokenize("")
	if tokens != nil {
		t.Fatalf("expected nil, got %v", tokens)
	}
}

func TestWords_Basic(t *testing.T) {
	words := Words("navigation button header")
	if len(words) == 0 {
		t.Fatal("expected non-empty words")
	}
}

func TestSanitizer_EmptyPrompt(t *testing.T) {
	s := NewSanitizer()
	_, err := s.Sanitize("")
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestSanitizer_TypoNavigation(t *testing.T) {
	s := NewSanitizer()
	intent, err := s.Sanitize("navi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.RawPrompt != "navi" {
		t.Fatalf("expected raw prompt preserved, got %q", intent.RawPrompt)
	}
	if intent.CleanIntent == "" {
		t.Fatal("expected non-empty clean intent")
	}
	if len(intent.TargetScopes) == 0 {
		t.Fatal("expected at least one target scope")
	}
	hasNav := false
	for _, scope := range intent.TargetScopes {
		if scope == "navigation" {
			hasNav = true
			break
		}
	}
	if !hasNav {
		t.Fatalf("expected 'navigation' in target scopes, got %v", intent.TargetScopes)
	}
}

func TestSanitizer_ButtonTypo(t *testing.T) {
	s := NewSanitizer()
	intent, err := s.Sanitize("btn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hasButton := false
	for _, scope := range intent.TargetScopes {
		if scope == "button" {
			hasButton = true
			break
		}
	}
	if !hasButton {
		t.Fatalf("expected 'button' in target scopes, got %v", intent.TargetScopes)
	}
}

func TestSanitizer_ColorRequest(t *testing.T) {
	s := NewSanitizer()
	intent, err := s.Sanitize("change background color to blue")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hasColor := false
	for _, scope := range intent.TargetScopes {
		if scope == "color" || scope == "background" {
			hasColor = true
			break
		}
	}
	if !hasColor {
		t.Fatalf("expected color/background in scopes, got %v", intent.TargetScopes)
	}
}

func TestSanitizer_LayoutRequest(t *testing.T) {
	s := NewSanitizer()
	intent, err := s.Sanitize("fix layout alignment and padding")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	scopeSet := make(map[string]bool)
	for _, sc := range intent.TargetScopes {
		scopeSet[sc] = true
	}
	if !scopeSet["layout"] {
		t.Fatalf("expected layout in scopes, got %v", intent.TargetScopes)
	}
	if !scopeSet["alignment"] {
		t.Fatalf("expected alignment in scopes, got %v", intent.TargetScopes)
	}
}

func TestSanitizer_Confidence(t *testing.T) {
	s := NewSanitizer()
	intent, err := s.Sanitize("add navigation button header")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.Confidence < 0.5 {
		t.Fatalf("expected confidence >= 0.5, got %f", intent.Confidence)
	}
}

func TestSanitizer_LowConfidence(t *testing.T) {
	s := NewSanitizer()
	intent, err := s.Sanitize("zzzzz yyyyy xxxxx wwwww")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.Confidence >= 0.5 {
		t.Fatalf("expected low confidence for non-matching words, got %f", intent.Confidence)
	}
}

func TestSanitizer_NeedsClarification(t *testing.T) {
	s := NewSanitizer()
	intent, _ := s.Sanitize("xyzzy")
	if !s.NeedsClarification(intent) {
		t.Fatal("expected NeedsClarification=true for low-confidence intent")
	}
}

func TestSanitizer_NoClarificationNeeded(t *testing.T) {
	s := NewSanitizer()
	intent, _ := s.Sanitize("navigation button header style layout")
	if s.NeedsClarification(intent) {
		t.Fatal("expected NeedsClarification=false for high-confidence intent")
	}
}

func TestEstimateTokens_Empty(t *testing.T) {
	if n := EstimateTokens(""); n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestEstimateTokens_Short(t *testing.T) {
	n := EstimateTokens("test")
	if n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}
}

func TestEstimateTokens_Long(t *testing.T) {
	n := EstimateTokens(strings.Repeat("a", 100))
	if n != 25 {
		t.Fatalf("expected 25, got %d", n)
	}
}

func TestSliceContext_NilArchetype(t *testing.T) {
	_, err := SliceContext(nil, &CanonicalIntent{RawPrompt: "test"}, "/tmp")
	if err == nil {
		t.Fatal("expected error for nil archetype")
	}
}

func TestSliceContext_NilIntent(t *testing.T) {
	_, err := SliceContext(&recon.ArchetypeContext{Type: recon.VANILLA_WEB}, nil, "/tmp")
	if err == nil {
		t.Fatal("expected error for nil intent")
	}
}

func TestSliceContext_VanillaWeb(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body>Hello</body></html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body { color: red; }"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('hello');"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte(`{"key": "value"}`), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := &recon.ArchetypeContext{
		Type: recon.VANILLA_WEB,
	}
	intent := &CanonicalIntent{
		RawPrompt:    "fix style",
		CleanIntent:  "fix style",
		TargetScopes: []string{"style"},
		Confidence:   0.9,
	}

	gc, err := SliceContext(archetype, intent, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gc == nil {
		t.Fatal("expected non-nil GroundedContext")
	}
	if len(gc.AllowedFileTree) == 0 {
		t.Fatal("expected non-empty allowed file tree")
	}
	for _, f := range gc.AllowedFileTree {
		ext := filepath.Ext(f)
		if ext != ".html" && ext != ".css" && ext != ".js" {
			t.Fatalf("unexpected file in allowed tree: %s (ext=%s)", f, ext)
		}
	}
	if !strings.Contains(gc.Payload, "VANILLA_WEB") {
		t.Fatal("payload should contain VANILLA_WEB archetype")
	}
	if !strings.Contains(gc.Payload, "ALLOWED_FILE_TREE") {
		t.Fatal("payload should contain ALLOWED_FILE_TREE")
	}
}

func TestSliceContext_GoBackend(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package main\n\nfunc Handle() {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Project"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := &recon.ArchetypeContext{
		Type: recon.GO_BACKEND,
	}
	intent := &CanonicalIntent{
		RawPrompt:    "add handler",
		CleanIntent:  "add handler",
		TargetScopes: []string{"handler"},
		Confidence:   0.8,
	}

	gc, err := SliceContext(archetype, intent, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gc == nil {
		t.Fatal("expected non-nil GroundedContext")
	}
	for _, f := range gc.AllowedFileTree {
		ext := filepath.Ext(f)
		if ext != ".go" && ext != ".mod" && ext != ".sum" {
			t.Fatalf("unexpected file for Go backend: %s (ext=%s)", f, ext)
		}
	}
}

func TestSliceContext_TokenCeiling(t *testing.T) {
	dir := t.TempDir()
	var hugeContent strings.Builder
	for i := 0; i < 1000; i++ {
		hugeContent.WriteString("line of content that is quite long and will consume tokens ")
	}
	if err := os.WriteFile(filepath.Join(dir, "big.html"), []byte(hugeContent.String()), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.css"), []byte(hugeContent.String()), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := &recon.ArchetypeContext{
		Type: recon.VANILLA_WEB,
	}
	intent := &CanonicalIntent{
		RawPrompt:    "fix style",
		CleanIntent:  "fix style",
		TargetScopes: []string{"style"},
		Confidence:   0.9,
	}

	gc, err := SliceContext(archetype, intent, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gc.TokenEstimate > totalTokenCeiling {
		t.Fatalf("token estimate %d exceeds ceiling %d", gc.TokenEstimate, totalTokenCeiling)
	}
}

func TestSliceContext_UnknownGeneric(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := &recon.ArchetypeContext{
		Type: recon.UNKNOWN_GENERIC,
	}
	intent := &CanonicalIntent{
		RawPrompt:    "do something",
		CleanIntent:  "do something",
		TargetScopes: []string{},
		Confidence:   0.3,
	}

	gc, err := SliceContext(archetype, intent, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gc.AllowedFileTree) > 0 {
		t.Fatalf("expected empty allowed file tree for UNKNOWN_GENERIC, got %v", gc.AllowedFileTree)
	}
}

func TestSanitizer_WithThreshold(t *testing.T) {
	s := NewSanitizer().WithThreshold(0.9)
	intent, _ := s.Sanitize("navigation")
	if s.NeedsClarification(intent) {
		t.Fatal("expected no clarification for exact match even at high threshold")
	}
}

func TestFuzzyMatcher_BestMatch(t *testing.T) {
	fm := NewFuzzyMatcher(0.6)
	m := fm.BestMatch([]string{"navi", "btn", "hedr"})
	if m == nil {
		t.Fatal("expected best match")
	}
	if m.Keyword != "navigation" && m.Keyword != "button" && m.Keyword != "header" {
		t.Fatalf("expected one of navigation/button/header, got %s", m.Keyword)
	}
}

func TestBuildPayload_ContainsSystemInstruction(t *testing.T) {
	gc := &GroundedContext{
		Archetype: &recon.ArchetypeContext{
			Type: recon.VANILLA_WEB,
		},
		Intent: &CanonicalIntent{
			RawPrompt:   "fix style",
			CleanIntent: "fix style",
		},
		AllowedFileTree: []string{"style.css"},
		Snippets: []Snippet{
			{FilePath: "style.css", StartLine: 1, EndLine: 1, Content: "body {}"},
		},
	}
	payload := buildPayload(gc)
	if !strings.Contains(payload, "VANILLA_WEB") {
		t.Fatal("payload should contain VANILLA_WEB")
	}
	if !strings.Contains(payload, "STRICT RULE") {
		t.Fatal("payload should contain STRICT RULE")
	}
	if !strings.Contains(payload, "style.css") {
		t.Fatal("payload should reference style.css")
	}
}
