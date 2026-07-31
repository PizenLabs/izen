package stream

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRuneBufferSplitsMultiByteRunesAcrossReads is the core UTF-8 boundary
// guarantee: a multi-byte rune delivered across two reads must be reassembled
// verbatim, never emitted as a broken slice.
func TestRuneBufferSplitsMultiByteRunesAcrossReads(t *testing.T) {
	input := "héllo wörld 你好 🌍"
	b := NewRuneBuffer()

	var emitted string
	for i := 0; i < len(input); i++ {
		// Feed ONE byte at a time — the worst possible chunk alignment.
		emitted += b.Write([]byte{input[i]})
	}

	// Nothing may be held back for a fully-delivered valid string.
	if got := b.Flush(); got != "" {
		t.Errorf("Flush after complete input = %q, want empty", got)
	}
	if emitted != input {
		t.Errorf("emitted = %q, want %q", emitted, input)
	}
	if !utf8.ValidString(emitted) {
		t.Errorf("emitted text is not valid UTF-8: %q", emitted)
	}
}

func TestRuneBufferHoldsIncompleteRuneTail(t *testing.T) {
	b := NewRuneBuffer()

	// "é" is 0xC3 0xA9. Deliver only the lead byte.
	out := b.Write([]byte{0xC3})
	if out != "" {
		t.Fatalf("incomplete lead byte emitted prematurely: %q", out)
	}
	if b.Len() != 1 {
		t.Errorf("pending = %d, want 1", b.Len())
	}

	// Now the continuation byte completes it.
	out = b.Write([]byte{0xA9})
	if out != "é" {
		t.Errorf("completed rune = %q, want é", out)
	}
	if b.Len() != 0 {
		t.Errorf("pending = %d, want 0 after completion", b.Len())
	}
}

func TestRuneBufferFlushReplacesTruncatedRune(t *testing.T) {
	b := NewRuneBuffer()

	// 4-byte rune (emoji) truncated to 3 bytes at stream end.
	for _, bt := range []byte{0xF0, 0x9F, 0x8C} {
		if got := b.Write([]byte{bt}); got != "" {
			t.Fatalf("unexpected early emit: %q", got)
		}
	}
	got := b.Flush()
	if !utf8.ValidString(got) {
		t.Fatalf("Flush output is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "\uFFFD") {
		t.Errorf("Flush output %q missing U+FFFD replacement for truncated rune", got)
	}
	if b.Len() != 0 {
		t.Errorf("pending = %d, want 0 after Flush", b.Len())
	}
}

func TestRuneBufferPassesASCIIVerbatim(t *testing.T) {
	b := NewRuneBuffer()
	got := b.Write([]byte("hello world\n"))
	if got != "hello world\n" {
		t.Errorf("got %q", got)
	}
}

func TestRuneBufferMixedChunking(t *testing.T) {
	b := NewRuneBuffer()
	// Split a CJK rune across reads while interleaving ASCII.
	parts := make([]string, 0, 4)
	parts = append(parts, b.Write([]byte("a")))     // "a"
	parts = append(parts, b.Write([]byte("中"[:1]))) // lead byte only
	parts = append(parts, b.Write([]byte("中"[1:]))) // remaining 2 bytes
	parts = append(parts, b.Write([]byte("b")))     // "b"
	got := strings.Join(parts, "")
	if got != "a中b" {
		t.Errorf("got %q, want a中b", got)
	}
}

func TestRuneBufferEmptyAndNil(t *testing.T) {
	b := NewRuneBuffer()
	if got := b.Write(nil); got != "" {
		t.Errorf("nil write = %q, want empty", got)
	}
	if got := b.Write([]byte{}); got != "" {
		t.Errorf("empty write = %q, want empty", got)
	}
	if got := b.Flush(); got != "" {
		t.Errorf("empty flush = %q, want empty", got)
	}
}
