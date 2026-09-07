package stream

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStreamBufferVietnameseSplit(t *testing.T) {
	// tiếng Việt contains multi-byte Vietnamese runes (2-3 bytes each).
	input := "tiếng Việt"
	raw := []byte(input)

	// Cut across 3 partial chunks at byte offsets that split multi-byte runes.
	// We force splits inside runes by cutting at byte indices 2,5 etc.
	// input bytes: t i ế (3 bytes) n g (1) space (1) V i ệ (3) t etc.
	// Choose splits: [0:3], [3:7], [7:]
	chunks := [][]byte{
		raw[0:3],  // may split inside ế
		raw[3:7],  // middle
		raw[7:],   // remainder
	}

	b := &StreamBuffer{}
	var got string
	var last string
	for i, chunk := range chunks {
		b.Append(chunk)
		s, updated := b.ReadValidString()
		if updated {
			// Must be valid UTF-8 and contain no replacement char unless final flush intended.
			if !utf8.ValidString(s) {
				t.Fatalf("chunk %d: returned string is not valid UTF-8: %q", i, s)
			}
			if strings.Contains(s, "\uFFFD") {
				t.Fatalf("chunk %d: returned string contains U+FFFD replacement char: %q", i, s)
			}
			last = s
		} else {
			// updated==false is allowed when chunk only completed partial rune tails without new valid prefix growth
			// but ensure we didn't lose data
		}
		_ = last
		// Also collect incremental valid prefix; final should equal input
		if s != "" {
			got = s // because ReadValidString returns whole valid prefix each time, last wins
		}
	}

	// After all chunks, flushed valid string must equal original with zero dropped bytes and zero FFFD
	if got != input {
		t.Fatalf("final StreamBuffer = %q, want %q", got, input)
	}
	if strings.Contains(got, "\uFFFD") {
		t.Fatalf("final string contains U+FFFD: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("final string is not valid UTF-8")
	}
}

func TestStreamBufferSplitOneByteAtATime(t *testing.T) {
	input := "tiếng Việt — 你好 🌍"
	b := &StreamBuffer{}
	for i := 0; i < len(input); i++ {
		b.Append([]byte{input[i]})
		s, updated := b.ReadValidString()
		// After each single-byte append, s must be valid UTF-8 and contain no FFFD (incomplete tails withheld)
		if updated && strings.Contains(s, "\uFFFD") {
			t.Fatalf("byte %d: unexpected FFFD in %q", i, s)
		}
		if updated && !utf8.ValidString(s) {
			t.Fatalf("byte %d: invalid UTF-8 %q", i, s)
		}
	}
	final, updated := b.ReadValidString()
	if !updated {
		// If last byte completed a rune, we should have gotten updated true; but if already returned, check String()
		final = b.String()
	}
	if final != input {
		t.Fatalf("one-byte-at-a-time final = %q want %q", final, input)
	}
}

func TestStreamBufferNoDroppedBytes(t *testing.T) {
	input := "café naïve résumé"
	b := &StreamBuffer{}
	// Split arbitrarily so every chunk cuts runes
	raw := []byte(input)
	for i := 0; i < len(raw); i += 2 {
		end := i + 2
		if end > len(raw) {
			end = len(raw)
		}
		b.Append(raw[i:end])
	}
	got := b.String()
	if got != input {
		t.Fatalf("no dropped bytes: got %q want %q", got, input)
	}
}

func TestStreamBufferConcurrentAppend(t *testing.T) {
	b := &StreamBuffer{}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			b.Append([]byte("a"))
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_, _ = b.ReadValidString()
	}
	<-done
	if b.Len() == 0 {
		t.Fatal("expected buffered bytes")
	}
}
