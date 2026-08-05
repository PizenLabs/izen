package ui

import "strings"

// StreamBlockKind classifies a stored stream block as either final response
// content (rendered bright) or thinking/reasoning text (rendered dimmed/faint).
type StreamBlockKind int

const (
	// KindContent is a block of final answer text.
	KindContent StreamBlockKind = iota
	// KindThinking is a block of reasoning/thinking text.
	KindThinking
)

// StreamBlock is one typed run of streamed output. Consecutive tokens of the
// same kind are coalesced into a single block so the renderer can apply one
// style per block — bright content immediately, dimmed reasoning as it streams.
type StreamBlock struct {
	Kind StreamBlockKind
	Text string
}

// StreamBuffer stores streamed output as typed blocks (KindContent vs
// KindThinking). Incoming tokens are dynamically appended to the active block
// when their kind matches the trailing block, otherwise a new block is started.
// It is the structured counterpart of the flat currentStreamContent string:
// thinking text is never merged into content, so the renderer can distinguish
// the two streams by construction.
type StreamBuffer struct {
	blocks []StreamBlock
}

// NewStreamBuffer constructs an empty structured stream buffer.
func NewStreamBuffer() *StreamBuffer {
	return &StreamBuffer{}
}

// Append adds text of the given kind. Empty text is a no-op. Consecutive
// same-kind tokens merge into the active block; a kind change starts a new
// block.
func (b *StreamBuffer) Append(kind StreamBlockKind, text string) {
	if text == "" {
		return
	}
	if n := len(b.blocks); n > 0 && b.blocks[n-1].Kind == kind {
		b.blocks[n-1].Text += text
		return
	}
	b.blocks = append(b.blocks, StreamBlock{Kind: kind, Text: text})
}

// Blocks returns a copy of the stored blocks so callers can iterate without
// racing a concurrent Append on the UI goroutine.
func (b *StreamBuffer) Blocks() []StreamBlock {
	out := make([]StreamBlock, len(b.blocks))
	copy(out, b.blocks)
	return out
}

// HasThinking reports whether any thinking block is stored.
func (b *StreamBuffer) HasThinking() bool {
	for _, bl := range b.blocks {
		if bl.Kind == KindThinking {
			return true
		}
	}
	return false
}

// HasContent reports whether any content block is stored.
func (b *StreamBuffer) HasContent() bool {
	for _, bl := range b.blocks {
		if bl.Kind == KindContent {
			return true
		}
	}
	return false
}

// Len returns the total number of text bytes across all blocks.
func (b *StreamBuffer) Len() int {
	n := 0
	for _, bl := range b.blocks {
		n += len(bl.Text)
	}
	return n
}

// Content returns the concatenated content blocks (the final answer).
func (b *StreamBuffer) Content() string {
	var sb strings.Builder
	for _, bl := range b.blocks {
		if bl.Kind == KindContent {
			sb.WriteString(bl.Text)
		}
	}
	return sb.String()
}

// Thinking returns the concatenated thinking blocks (the reasoning text).
func (b *StreamBuffer) Thinking() string {
	var sb strings.Builder
	for _, bl := range b.blocks {
		if bl.Kind == KindThinking {
			sb.WriteString(bl.Text)
		}
	}
	return sb.String()
}

// Reset clears all stored blocks.
func (b *StreamBuffer) Reset() {
	b.blocks = nil
}
