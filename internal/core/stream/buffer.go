// Package stream provides rune-safe buffering and thought/content separation
// for LLM response streams.
//
// LLM providers deliver their output as arbitrary byte chunks that are NOT
// aligned to UTF-8 rune boundaries. Slicing those raw byte chunks into strings
// directly (e.g. string(buf[:n])) can split a multi-byte rune across two reads,
// corrupting the final text with replacement characters and dropping visible
// words from markdown answers. RuneBuffer fixes that: it holds incomplete
// multi-byte runes back until the next read completes them and only ever emits
// whole runes.
//
// Splitter additionally separates the stream into two distinct pipelines —
// reasoning/thinking content (<thought>…</thought> or reasoning_content
// frames) and the final response — so thinking never bleeds into the visible
// answer. Both layers are verbatim passthrough: markdown escapes, backticks,
// and JSON escape codes are preserved exactly as delivered.
package stream

import (
	"strings"
	"unicode/utf8"
)

// RuneBuffer accumulates raw bytes until complete UTF-8 runes are available
// and only then releases them as strings. An incomplete multi-byte rune at the
// end of a chunk is held back (never sliced) until the next Write completes it.
//
// The zero value is not usable; construct with NewRuneBuffer.
type RuneBuffer struct {
	pending []byte
}

// NewRuneBuffer constructs an empty rune-safe buffer.
func NewRuneBuffer() *RuneBuffer {
	return &RuneBuffer{}
}

// Write ingests raw bytes and returns the completed runes as a single string.
// Any trailing incomplete multi-byte sequence is retained internally until the
// next Write (or Flush). The returned string is always valid UTF-8.
func (b *RuneBuffer) Write(p []byte) string {
	if len(p) == 0 {
		return ""
	}
	b.pending = append(b.pending, p...)
	return b.drain()
}

// Flush releases any remaining buffered bytes. Bytes forming an incomplete
// multi-byte sequence at the end of the stream are replaced with U+FFFD so the
// emitted text stays valid UTF-8 (dropping the invalid tail is preferable to
// corrupting the visible answer with raw bytes). The buffer is emptied.
func (b *RuneBuffer) Flush() string {
	if len(b.pending) == 0 {
		return ""
	}
	var sb strings.Builder
	for len(b.pending) > 0 {
		r, size := utf8.DecodeRune(b.pending)
		if r == utf8.RuneError && size == 1 {
			if isIncompletePrefix(b.pending) {
				// Incomplete rune at stream end — replace rather than emit raw bytes.
				sb.WriteRune(utf8.RuneError)
				b.pending = b.pending[1:]
				continue
			}
		}
		sb.WriteRune(r)
		b.pending = b.pending[size:]
	}
	b.pending = b.pending[:0]
	return sb.String()
}

// Len returns the number of pending bytes currently held back.
func (b *RuneBuffer) Len() int {
	return len(b.pending)
}

// drain emits every complete rune from pending and returns them as a string.
func (b *RuneBuffer) drain() string {
	var sb strings.Builder
	for len(b.pending) > 0 {
		r, size := utf8.DecodeRune(b.pending)
		if r == utf8.RuneError && size == 1 {
			// A single-byte RuneError is either a genuinely invalid byte
			// (emit U+FFFD) or the head of an incomplete multi-byte rune
			// (hold back until more bytes arrive).
			if isIncompletePrefix(b.pending) {
				break
			}
			sb.WriteRune(utf8.RuneError)
			b.pending = b.pending[1:]
			continue
		}
		sb.WriteRune(r)
		b.pending = b.pending[size:]
	}
	return sb.String()
}

// isIncompletePrefix reports whether p is a proper prefix of a valid UTF-8
// encoding, i.e. it begins with a multi-byte lead byte and ends before all of
// its continuation bytes have arrived. Such a tail must be buffered, not
// sliced, because more bytes may complete it on the next read.
func isIncompletePrefix(p []byte) bool {
	if len(p) == 0 {
		return false
	}
	b := p[0]
	var want int
	switch {
	case b < 0x80:
		return false
	case b&0xE0 == 0xC0:
		want = 2
	case b&0xF0 == 0xE0:
		want = 3
	case b&0xF8 == 0xF0:
		want = 4
	default:
		return false
	}
	if len(p) >= want {
		return false
	}
	// Every byte after the lead must be a valid continuation byte.
	for i := 1; i < len(p); i++ {
		if p[i]&0xC0 != 0x80 {
			return false
		}
	}
	return true
}
