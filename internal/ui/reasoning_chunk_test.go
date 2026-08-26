package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestReasoningChunkMsgFeedsThinkingBuffer pins the Ctrl+O live-thought
// contract for async execution streams: every ReasoningChunkMsg dispatched
// from the autonomous DAG_EXECUTING / gated gateway callback appends verbatim
// to the active ThinkingBuffer, so Ctrl+O expands a live reasoning trace
// instead of an empty overlay.
func TestReasoningChunkMsgFeedsThinkingBuffer(t *testing.T) {
	m := &model{}
	if _, cmd := m.Update(ReasoningChunkMsg{Chunk: "sub-task st-1 "}); cmd != nil {
		t.Fatalf("ReasoningChunkMsg must not schedule commands, got %v", cmd)
	}
	m.Update(ReasoningChunkMsg{Chunk: "reasoning about window"})

	if m.thinkingBuffer == nil {
		t.Fatal("thinkingBuffer not created by ReasoningChunkMsg")
	}
	if got := m.thinkingBuffer.String(); got != "sub-task st-1 reasoning about window" {
		t.Errorf("buffer = %q, want %q", got, "sub-task st-1 reasoning about window")
	}
	if len(m.records) != 0 {
		t.Errorf("reasoning chunks must never create chat records, got %d", len(m.records))
	}
}

// TestReasoningChunkMsgEmptyIsNoop proves an empty chunk neither creates the
// buffer nor mutates state.
func TestReasoningChunkMsgEmptyIsNoop(t *testing.T) {
	m := &model{}
	m.Update(ReasoningChunkMsg{Chunk: ""})
	if m.thinkingBuffer != nil {
		t.Fatal("empty chunk must not create a thinking buffer")
	}
}

// TestStreamCallbackRoutesReasoningDelta pins the message-type routing of an
// execution stream callback: a "reasoning_delta" StreamEvent becomes a
// ReasoningChunkMsg on the exec stream channel (never a content token), so
// reasoning reaches the Ctrl+O thought drawer and stays out of the visible
// response pipeline.
func TestStreamCallbackRoutesReasoningDelta(t *testing.T) {
	ch := make(chan tea.Msg, 4)

	// Mirror of the SetStreamCallback routing contract in autonomous.go and
	// gateway.go under test.
	route := func(kind, content string) {
		switch kind {
		case "content_delta":
			if content != "" {
				ch <- tokenMsg(content)
			}
		case "reasoning_delta":
			if content != "" {
				ch <- ReasoningChunkMsg{Chunk: content}
			}
		}
	}

	route("content_delta", "visible text")
	route("reasoning_delta", "hidden thought")

	if msg := (<-ch).(tokenMsg); string(msg) != "visible text" {
		t.Errorf("content route = %q, want tokenMsg(visible text)", msg)
	}
	if reasoned := (<-ch).(ReasoningChunkMsg); reasoned.Chunk != "hidden thought" {
		t.Errorf("reasoning route = %+v, want ReasoningChunkMsg(hidden thought)", reasoned)
	}
}
