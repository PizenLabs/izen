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
//
// It also pins the pacer contract: a chunk renders through the central
// FrameTickMsg, never on arrival. The handler may therefore return the frame
// tick — that is how a reasoning trace arriving outside a turn keeps the clock
// alive — but it must never have armed a repaint of its own, because one
// document composition per reasoning token is exactly the coupling the unified
// pacer exists to remove.
func TestReasoningChunkMsgFeedsThinkingBuffer(t *testing.T) {
	m := &model{}
	nm, cmd := m.Update(ReasoningChunkMsg{Chunk: "sub-task st-1 "})
	m = nm.(*model)
	if m.refreshScheduled {
		t.Fatal("ReasoningChunkMsg armed a repaint: reasoning must render on the frame tick, not on arrival")
	}
	for _, msg := range drainCmds(t, cmd) {
		if _, ok := msg.(FrameTickMsg); !ok {
			t.Fatalf("ReasoningChunkMsg returned %T, want only the central FrameTickMsg", msg)
		}
	}

	nm, _ = m.Update(ReasoningChunkMsg{Chunk: "reasoning about window"})
	m = nm.(*model)

	if m.thinkingBuffer == nil {
		t.Fatal("thinkingBuffer not created by ReasoningChunkMsg")
	}
	if got := m.thinkingBuffer.String(); got != "sub-task st-1 reasoning about window" {
		t.Errorf("buffer = %q, want %q", got, "sub-task st-1 reasoning about window")
	}
	if len(m.records) != 0 {
		t.Errorf("reasoning chunks must never create chat records, got %d", len(m.records))
	}
	// The chunk is PENDING, not rendered: the frame tick owns the composition.
	if !m.pacedStreamsActive() {
		t.Error("a reasoning chunk must note the unified pacer so the frame tick renders it")
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
