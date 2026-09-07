package stream

import (
	"sync"
	"unicode/utf8"
)

// StreamBuffer is an append-only UTF-8 boundary safe buffer.
// It accumulates raw network chunks (which may split multi-byte runes) and
// exposes only valid UTF-8 prefixes via ReadValidString. Dangling incomplete
// bytes at the tail are retained for the next Append call and never emitted as
// replacement characters (U+FFFD) mid-stream. Thread-safe via sync.RWMutex.
//
// CONTRACT (Option A — Cumulative Overwrite): ReadValidString returns the FULL
// accumulated valid string from the start of the stream on every call (with
// updated=false when nothing new arrived). It NEVER drains. Callers must
// overwrite their view from the full text and emit only the unread suffix
// beyond what they already rendered — NEVER `view += fullText`. Reset() fully
// flushes the buffer to empty at the start of a new prompt submission.
type StreamBuffer struct {
	mu       sync.RWMutex
	buf      []byte
	lastLen  int // length of last valid prefix returned (for updated detection)
}

// Append appends a raw network chunk thread-safely.
func (b *StreamBuffer) Append(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	b.mu.Lock()
	b.buf = append(b.buf, chunk...)
	b.mu.Unlock()
}

// ReadValidString returns the valid UTF-8 prefix of the buffer.
// If an incomplete multi-byte rune is detected at the slice tail, those
// dangling bytes are retained for the next chunk and not included in the
// returned string. It returns updated=false when no new valid runes are
// available since the last successful read.
func (b *StreamBuffer) ReadValidString() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buf) == 0 {
		return "", false
	}

	// Find incomplete tail length.
	tail := incompleteTailLen(b.buf)
	validLen := len(b.buf) - tail

	// No valid prefix yet (e.g. only a lead byte of a 3-byte rune).
	if validLen == 0 {
		return "", false
	}

	// Validate that prefix is indeed valid UTF-8 (defensive).
	// If validLen prefix is valid, we can return it.
	if !utf8.Valid(b.buf[:validLen]) {
		// Fallback: find last valid rune boundary via DecodeLastRune.
		// Trim back until prefix is valid.
		for validLen > 0 && !utf8.Valid(b.buf[:validLen]) {
			validLen--
		}
		if validLen == 0 {
			return "", false
		}
		// Also adjust tail accordingly (but buf retains tail)
	}

	if validLen == b.lastLen {
		return "", false
	}

	s := string(b.buf[:validLen])
	b.lastLen = validLen
	return s, true
}

// Len returns current buffered byte length (including dangling tail).
func (b *StreamBuffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.buf)
}

// Reset clears the buffer.
func (b *StreamBuffer) Reset() {
	b.mu.Lock()
	b.buf = b.buf[:0]
	b.lastLen = 0
	b.mu.Unlock()
}

// String returns the current valid UTF-8 string without updating state.
// Used for final flush where caller needs full content regardless of updated flag.
func (b *StreamBuffer) String() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.buf) == 0 {
		return ""
	}
	tail := incompleteTailLen(b.buf)
	validLen := len(b.buf) - tail
	if validLen <= 0 {
		// If stream ended with incomplete tail, flush with replacement.
		// For spec compliance, we expose valid prefix; caller may choose to flush.
		return ""
	}
	return string(b.buf[:validLen])
}

// Flush returns the entire content with any dangling tail replaced by U+FFFD.
// It clears the buffer and is used at stream termination.
func (b *StreamBuffer) Flush() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) == 0 {
		return ""
	}
	// Validate and replace incomplete tail with U+FFFD via RuneBuffer logic.
	var out []byte
	// Emit valid prefix directly.
	tail := incompleteTailLen(b.buf)
	validLen := len(b.buf) - tail
	out = append(out, b.buf[:validLen]...)
	if tail > 0 {
		// Incomplete rune at end of stream -> replacement char.
		out = append(out, []byte("\uFFFD")...)
	}
	b.buf = b.buf[:0]
	b.lastLen = 0
	return string(out)
}

// incompleteTailLen returns the number of trailing bytes that form an
// incomplete UTF-8 encoding. Zero means the buffer ends on a valid rune boundary.
func incompleteTailLen(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	// Fast path: last byte is ASCII
	if p[len(p)-1] < 0x80 {
		return 0
	}
	// Find the start of the last rune by scanning backwards over continuation bytes.
	i := len(p) - 1
	for i >= 0 && p[i]&0xC0 == 0x80 {
		i--
	}
	if i < 0 {
		// All bytes are continuation bytes -> incomplete.
		return len(p)
	}
	lead := p[i]
	var want int
	switch {
	case lead&0xE0 == 0xC0:
		want = 2
	case lead&0xF0 == 0xE0:
		want = 3
	case lead&0xF8 == 0xF0:
		want = 4
	default:
		// Invalid lead byte (e.g. 0xFF) -> treat as single invalid byte, not incomplete.
		// Let utf8 validation handle it; incomplete = 0 so replacement will be emitted via Flush path only.
		return 0
	}
	have := len(p) - i
	if have < want {
		// Verify that all bytes after lead are indeed continuation bytes (they are, by scan)
		return have
	}
	// have >= want: check if last rune is valid. If invalid (e.g. overlong), not incomplete.
	_, size := utf8.DecodeLastRune(p)
	if size == 1 {
		// RuneError with size 1 means invalid encoding at tail, not incomplete for our purpose.
		// But we already handled have<want case; here have==want yet invalid, so not incomplete.
		return 0
	}
	return 0
}
