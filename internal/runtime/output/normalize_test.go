package output

import (
	"strings"
	"testing"
)

func TestNormalizeStripsANSI(t *testing.T) {
	raw := "\x1b[31mred\x1b[0m \x1b[1;32mgreen\x1b[m\n"
	got := Normalize([]byte(raw))
	if got != "red green\n" {
		t.Errorf("Normalize = %q, want %q", got, "red green\n")
	}
}

func TestNormalizeUnifiesCarriageReturns(t *testing.T) {
	got := Normalize([]byte("one\r\ntwo\rthree\nfour\r\n"))
	if got != "one\ntwo\nthree\nfour\n" {
		t.Errorf("Normalize = %q", got)
	}
}

func TestNormalizeHandlesOSCSequences(t *testing.T) {
	raw := "\x1b]0;title here\x07visible"
	got := Normalize([]byte(raw))
	if got != "visible" {
		t.Errorf("Normalize = %q, want %q", got, "visible")
	}
}

func TestNormalizeFixesInvalidUTF8(t *testing.T) {
	raw := []byte{'a', 0xff, 'b', 0xfe, '\n'}
	got := Normalize(raw)
	if !strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("Normalize = %q, want replacement char", got)
	}
	// Valid UTF-8 must survive untouched.
	if got := Normalize([]byte("héllo wörld\n")); got != "héllo wörld\n" {
		t.Errorf("Normalize mangled valid UTF-8: %q", got)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	raw := "\x1b[31mfoo\r\nbar\r\x1b[2K"
	once := Normalize([]byte(raw))
	twice := Normalize([]byte(once))
	if once != twice {
		t.Errorf("Normalize not idempotent: %q vs %q", once, twice)
	}
}
