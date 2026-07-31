package stream

import "strings"

// ChunkKind classifies a unit of stream output into one of two pipelines.
type ChunkKind int

const (
	// ChunkContent is final markdown answer / tool call text destined for the
	// response pipeline.
	ChunkContent ChunkKind = iota
	// ChunkReasoning is thinking/reasoning process text destined for the
	// reasoning pipeline. It is never mixed into the response.
	ChunkReasoning
)

// Frame is one verbatim unit of classified stream output. Text is passed
// through byte-for-byte (markdown escapes, backticks, and JSON escape codes
// are preserved exactly); only the classification changes.
type Frame struct {
	Kind ChunkKind
	Text string
}

// ThoughtOpen and ThoughtClose are the thinking markers recognized between
// chunks, including when they straddle a chunk boundary.
const (
	ThoughtOpen  = "<thought>"
	ThoughtClose = "</thought>"
)

// ReasoningSentinel is the zero-width marker used by providers that surface
// native reasoning_content via SSE (e.g. the OpenRouter provider). The
// Splitter recognizes it as a synonym for <thought>.
const ReasoningSentinel = "\x00RSNG\x00"

// Splitter is a cross-chunk state machine that separates an LLM stream into
// reasoning and content pipelines. It is verbatim: every byte of text is
// routed to exactly one pipeline with its original escaping intact — the tags
// and sentinels themselves are consumed and never emitted.
//
// Markers may be split across any number of chunk boundaries (a chunk may end
// with "<thou", the next may begin "ght>"). Partial tag tails are held back
// until either the marker completes or the stream ends. If the stream ends
// with an unclosed marker prefix, that prefix is emitted as literal content
// rather than silently dropped.
//
// The zero value is not usable; construct with NewSplitter.
type Splitter struct {
	buf       strings.Builder
	inThought bool
}

// NewSplitter constructs a splitter in the content state.
func NewSplitter() *Splitter {
	return &Splitter{}
}

// InThought reports whether the splitter is currently inside a reasoning
// block.
func (s *Splitter) InThought() bool {
	return s.inThought
}

// Write ingests one rune-safe string chunk and dispatches every complete
// segment to emit as a Frame. Any marker that is still incomplete at the end
// of the chunk (e.g. "<thou") is held back until the next Write or Flush.
func (s *Splitter) Write(chunk string, emit func(Frame)) {
	if chunk != "" {
		s.buf.WriteString(chunk)
	}
	s.scan(emit)
}

// Flush processes any remaining buffered text. A trailing partial marker is
// treated as literal text (it is what it is — the stream is over, so no more
// bytes can complete it). It also emits a synthetic Frame when a thought block
// was left open at stream end so consumers can finalize the reasoning
// pipeline. After Flush the splitter is empty and back in the content state.
func (s *Splitter) Flush(emit func(Frame)) {
	s.scan(emit)
	if s.buf.Len() > 0 {
		text := s.buf.String()
		s.buf.Reset()
		kind := ChunkContent
		if s.inThought {
			kind = ChunkReasoning
		}
		emit(Frame{Kind: kind, Text: text})
	}
	if s.inThought {
		// Unclosed reasoning block at stream end: finalize the pipeline so
		// consumers can mark it complete without emitting anything visible.
		s.inThought = false
		emit(Frame{Kind: ChunkReasoning, Text: ""})
	}
}

// scan processes everything currently buffered, splitting on the earliest
// active marker. While in content state the open markers <thought> and the
// reasoning sentinel are recognized; while reasoning the close markers
// </thought> and the (symmetric) reasoning sentinel are. It holds back only a
// partial marker tail at the very end of the buffer.
func (s *Splitter) scan(emit func(Frame)) {
	openers := []string{ThoughtOpen, ReasoningSentinel}
	closers := []string{ThoughtClose, ReasoningSentinel}
	for {
		raw := s.buf.String()
		markers := openers
		if s.inThought {
			markers = closers
		}
		idx, marker := earliestMarker(raw, markers)
		if idx >= 0 {
			pre := raw[:idx]
			s.buf.Reset()
			s.buf.WriteString(raw[idx+len(marker):])
			s.emitText(pre, emit)
			s.inThought = !s.inThought
			continue
		}
		// No complete marker: check whether the buffer ends with a partial
		// marker prefix that a future chunk may complete.
		if hold := partialMarkerSuffix(raw, markers); hold > 0 {
			emitLen := len(raw) - hold
			s.emitText(raw[:emitLen], emit)
			s.buf.Reset()
			s.buf.WriteString(raw[emitLen:])
			return
		}
		s.emitText(raw, emit)
		s.buf.Reset()
		return
	}
}

// earliestMarker returns the index of the earliest occurrence of any marker
// and the marker that matched. Returns (-1, "") when none is present.
func earliestMarker(raw string, markers []string) (int, string) {
	best := -1
	bestM := ""
	for _, m := range markers {
		if i := strings.Index(raw, m); i >= 0 && (best < 0 || i < best) {
			best = i
			bestM = m
		}
	}
	return best, bestM
}

// emitText routes verbatim text to the pipeline matching the current state.
// Empty text never produces a Frame.
func (s *Splitter) emitText(text string, emit func(Frame)) {
	if text == "" {
		return
	}
	kind := ChunkContent
	if s.inThought {
		kind = ChunkReasoning
	}
	emit(Frame{Kind: kind, Text: text})
}

// partialMarkerSuffix returns the length of the longest proper prefix of any
// marker that appears as a suffix of raw — i.e. the marker may be split across
// the current chunk boundary and the tail must be withheld. Returns 0 when no
// prefix matches.
func partialMarkerSuffix(raw string, markers []string) int {
	best := 0
	for _, m := range markers {
		for l := len(m) - 1; l >= 1; l-- {
			if l <= best {
				break
			}
			if strings.HasSuffix(raw, m[:l]) {
				best = l
				break
			}
		}
	}
	return best
}

// Split is a convenience wrapper that runs Write over every chunk and Flush at
// the end, returning the collected frames. The openMarker argument selects the
// reason-extraction behavior: pass ThoughtOpen for raw text streams.
func Split(chunks []string) []Frame {
	s := NewSplitter()
	var frames []Frame
	emit := func(f Frame) {
		if f.Text == "" {
			return
		}
		frames = append(frames, f)
	}
	for _, c := range chunks {
		s.Write(c, emit)
	}
	s.Flush(emit)
	return frames
}

// ReasonBlock extracts the concatenated reasoning text (both <thought> tags
// and ReasoningSentinel frames) from a full response and returns it alongside
// the cleaned content with all reasoning markers removed. It is the
// non-streaming equivalent of the Splitter and is used to sanitize complete
// responses.
func ReasonBlock(raw string) (content, reasoning string) {
	var contentParts, reasoningParts strings.Builder
	s := NewSplitter()
	emit := func(f Frame) {
		switch f.Kind {
		case ChunkContent:
			contentParts.WriteString(f.Text)
		case ChunkReasoning:
			reasoningParts.WriteString(f.Text)
		}
	}
	s.Write(raw, emit)
	s.Flush(emit)
	return contentParts.String(), reasoningParts.String()
}
