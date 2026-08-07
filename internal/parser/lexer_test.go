package parser

import (
	"reflect"
	"testing"
)

func kinds(t *testing.T, toks []Token) []TokenKind {
	t.Helper()
	out := make([]TokenKind, len(toks))
	for i, tok := range toks {
		out[i] = tok.Kind
	}
	return out
}

func TestTokenizeEmptyAndWhitespace(t *testing.T) {
	for _, input := range []string{"", "   ", "\n\t  \r"} {
		toks := Tokenize(input)
		if !reflect.DeepEqual(kinds(t, toks), []TokenKind{TokenEOF}) {
			t.Fatalf("Tokenize(%q) = %v, want [<eof>]", input, toks)
		}
	}
}

func TestTokenizePlainText(t *testing.T) {
	toks := Tokenize("fix login timeout")
	got := make([]string, 0, len(toks))
	for _, tok := range toks {
		got = append(got, tok.String())
	}
	want := []string{"fix", "login", "timeout", "<eof>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

// TestTokenizeQuickChain covers the primary spec example: multi-directive
// quick-chaining with adjacent markers.
func TestTokenizeQuickChain(t *testing.T) {
	toks := Tokenize("/build$hot$test @auth.go fix deadlock")
	got := make([]string, 0, len(toks))
	for _, tok := range toks {
		got = append(got, tok.String())
	}
	want := []string{"/build", "$hot", "$test", "@auth.go", "fix", "deadlock", "<eof>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizeWhitespaceSeparated(t *testing.T) {
	toks := Tokenize("/build $hot fix login timeout @auth.go")
	got := make([]string, 0, len(toks))
	for _, tok := range toks {
		got = append(got, tok.String())
	}
	want := []string{"/build", "$hot", "fix", "login", "timeout", "@auth.go", "<eof>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

// TestTokenizeScopePath verifies that '/' inside an @ target is a path
// separator, not a marker.
func TestTokenizeScopePath(t *testing.T) {
	toks := Tokenize("@internal/auth.go")
	got := []string{toks[0].String()}
	if !reflect.DeepEqual(got, []string{"@internal/auth.go"}) {
		t.Fatalf("Tokenize = %v, want [@internal/auth.go]", toks)
	}
	if toks[0].Name != "internal/auth.go" {
		t.Fatalf("scope target = %q, want %q", toks[0].Name, "internal/auth.go")
	}
}

// TestTokenizeScopeStopsAtDollar verifies that a $ chained directly after a
// scope target still starts a new directive token.
func TestTokenizeScopeStopsAtDollar(t *testing.T) {
	toks := Tokenize("@auth.go$hot")
	got := make([]string, 0, len(toks))
	for _, tok := range toks {
		got = append(got, tok.String())
	}
	want := []string{"@auth.go", "$hot", "<eof>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizeEmptyName(t *testing.T) {
	toks := Tokenize("/build $")
	if toks[0].String() != "/build" {
		t.Fatalf("toks[0] = %v, want /build", toks[0])
	}
	if toks[1].Marker != '$' || toks[1].Name != "" {
		t.Fatalf("toks[1] = %v, want empty-name dollar command", toks[1])
	}
}

// TestTokenizeWordStopsAtMarker verifies a bare word is split at a marker
// boundary (markers always start new tokens).
func TestTokenizeWordStopsAtMarker(t *testing.T) {
	toks := Tokenize("email@domain.com")
	got := make([]string, 0, len(toks))
	for _, tok := range toks {
		got = append(got, tok.String())
	}
	want := []string{"email", "@domain.com", "<eof>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizePositions(t *testing.T) {
	toks := Tokenize("fix\n/build")
	if toks[0].Pos.Line != 1 || toks[0].Pos.Column != 1 {
		t.Fatalf("word 'fix' pos = %+v, want 1:1", toks[0].Pos)
	}
	if toks[1].Pos.Line != 2 || toks[1].Pos.Column != 1 {
		t.Fatalf("command '/build' pos = %+v, want 2:1", toks[1].Pos)
	}
}

func TestTokenizeUnicodeWords(t *testing.T) {
	toks := Tokenize("/build 修复 死锁")
	if toks[0].String() != "/build" {
		t.Fatalf("toks[0] = %v, want /build", toks[0])
	}
	if toks[1].Text != "修复" {
		t.Fatalf("toks[1] = %q, want 修复", toks[1].Text)
	}
	if toks[2].Text != "死锁" {
		t.Fatalf("toks[2] = %q, want 死锁", toks[2].Text)
	}
}
