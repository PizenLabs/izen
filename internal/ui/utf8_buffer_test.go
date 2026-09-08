package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PizenLabs/izen/internal/core/stream"
)

func TestFrameTickBuffer_VietnameseSplit(t *testing.T) {
	input := "tiếng Việt"
	raw := []byte(input)
	// Cut across 3 partial chunks at byte offsets that split multi-byte runes
	chunks := [][]byte{
		raw[0:2],
		raw[2:5],
		raw[5:],
	}
	buf := &stream.StreamBuffer{}
	var last string
	for i, chunk := range chunks {
		buf.Append(chunk)
		s, updated := buf.ReadValidString()
		// updated may be false when chunk only completed partial tail without new rune
		if updated {
			if !utf8.ValidString(s) {
				t.Fatalf("chunk %d: invalid UTF-8 %q", i, s)
			}
			if strings.Contains(s, "\uFFFD") {
				t.Fatalf("chunk %d: contains FFFD %q", i, s)
			}
			last = s
		}
	}
	// Final valid string must equal original
	final := last
	if final == "" {
		final = buf.String()
	}
	if final != input {
		t.Fatalf("got %q want %q", final, input)
	}
	if strings.Contains(final, "\uFFFD") {
		t.Fatalf("final contains FFFD")
	}
}

func TestFrameTickBuffer_NoFFFDOnSplit(t *testing.T) {
	input := "tiếng Việt — hello 🌍"
	b := &stream.StreamBuffer{}
	for i := 0; i < len(input); i++ {
		b.Append([]byte{input[i]})
		s, updated := b.ReadValidString()
		if updated && strings.Contains(s, "\uFFFD") {
			t.Fatalf("byte %d: FFFD in %q", i, s)
		}
		if updated && !utf8.ValidString(s) {
			t.Fatalf("byte %d: invalid UTF-8", i)
		}
	}
	got := b.String()
	if got != input {
		t.Fatalf("got %q want %q", got, input)
	}
}
