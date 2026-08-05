package ui

import "testing"

func TestStreamBufferMergesConsecutiveSameKindTokens(t *testing.T) {
	b := NewStreamBuffer()
	b.Append(KindContent, "Hel")
	b.Append(KindContent, "lo ")
	b.Append(KindThinking, "think")
	b.Append(KindThinking, "ing")
	b.Append(KindContent, "world")

	blocks := b.Blocks()
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	if blocks[0].Kind != KindContent || blocks[0].Text != "Hello " {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Kind != KindThinking || blocks[1].Text != "thinking" {
		t.Errorf("block 1 = %+v", blocks[1])
	}
	if blocks[2].Kind != KindContent || blocks[2].Text != "world" {
		t.Errorf("block 2 = %+v", blocks[2])
	}
}

func TestStreamBufferContentAndThinking(t *testing.T) {
	b := NewStreamBuffer()
	b.Append(KindThinking, "step one ")
	b.Append(KindThinking, "step two")
	b.Append(KindContent, "The answer.")

	if got := b.Content(); got != "The answer." {
		t.Errorf("Content() = %q", got)
	}
	if got := b.Thinking(); got != "step one step two" {
		t.Errorf("Thinking() = %q", got)
	}
	if !b.HasThinking() || !b.HasContent() {
		t.Error("HasThinking/HasContent misreported")
	}
}

func TestStreamBufferLenAndReset(t *testing.T) {
	b := NewStreamBuffer()
	b.Append(KindContent, "abc")
	b.Append(KindThinking, "xyz")
	if b.Len() != 6 {
		t.Errorf("Len() = %d, want 6", b.Len())
	}
	b.Reset()
	if b.Len() != 0 || b.HasContent() || b.HasThinking() {
		t.Errorf("buffer not empty after Reset: len=%d", b.Len())
	}
	if len(b.Blocks()) != 0 {
		t.Error("Blocks() not empty after Reset")
	}
}

func TestStreamBufferEmptyTextIsNoOp(t *testing.T) {
	b := NewStreamBuffer()
	b.Append(KindContent, "")
	b.Append(KindThinking, "")
	if len(b.Blocks()) != 0 {
		t.Error("empty appends created blocks")
	}
}

func TestStreamBufferBlocksReturnsCopy(t *testing.T) {
	b := NewStreamBuffer()
	b.Append(KindContent, "x")
	got := b.Blocks()
	got[0].Text = "mutated"
	if b.Blocks()[0].Text != "x" {
		t.Error("Blocks() mutation leaked into the buffer")
	}
}
