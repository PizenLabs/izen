package stream

// TokenKind classifies a single unit of streamed output as either final
// response content or thinking/reasoning text.
type TokenKind int

const (
	// TokenKindContent is a token of the final answer destined for the
	// response pipeline. It is rendered bright/crisp as it arrives.
	TokenKindContent TokenKind = iota
	// TokenKindThinking is a token of reasoning/thinking text (delta
	// reasoning_content, <thought>…</thought> blocks, or reasoning
	// sentinels) destined for the dimmed/faint reasoning pipeline.
	TokenKindThinking
)

// Token is one verbatim unit of classified stream output. Kind carries the
// explicit thinking-vs-content metadata; Text is passed through byte-for-byte
// (markdown escapes, backticks, and JSON escape codes are preserved exactly).
type Token struct {
	Kind TokenKind
	Text string
}

// Classifier is a stateful stream classifier that tracks the current stream
// state (IsThinking) and separates incoming tokens into thinking
// (delta.reasoning_content / <thought>…</thought> / reasoning sentinels) vs
// content (delta.content) pipelines. It wraps the Splitter state machine so
// markers may be split across any number of chunk boundaries and are consumed
// exactly once.
//
// The zero value is not usable; construct with NewClassifier.
type Classifier struct {
	splitter *Splitter
}

// NewClassifier constructs a classifier in the content state.
func NewClassifier() *Classifier {
	return &Classifier{splitter: NewSplitter()}
}

// IsThinking reports whether the classifier is currently inside a reasoning
// block — the explicit stream state used by consumers to style output
// differentially.
func (c *Classifier) IsThinking() bool {
	return c.splitter.InThought()
}

// Write ingests one rune-safe chunk and emits every complete segment as a
// classified Token. Any marker that is still incomplete at the end of the
// chunk (e.g. "<thou") is held back until the next Write or Flush.
func (c *Classifier) Write(chunk string, emit func(Token)) {
	if chunk == "" {
		return
	}
	c.splitter.Write(chunk, func(f Frame) {
		emitToken(f, emit)
	})
}

// Flush processes any remaining buffered text, emitting any trailing partial
// marker as literal content (the stream is over, so no more bytes can complete
// it). After Flush the classifier is back in the content state.
func (c *Classifier) Flush(emit func(Token)) {
	c.splitter.Flush(func(f Frame) {
		emitToken(f, emit)
	})
}

// emitToken converts one splitter Frame into a typed Token. Empty text never
// produces a Token; the Splitter's synthetic end-of-thought frame (empty text)
// is consumed here so consumers see only real tokens.
func emitToken(f Frame, emit func(Token)) {
	if f.Text == "" {
		return
	}
	kind := TokenKindContent
	if f.Kind == ChunkReasoning {
		kind = TokenKindThinking
	}
	emit(Token{Kind: kind, Text: f.Text})
}
