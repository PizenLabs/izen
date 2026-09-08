package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// FrameTickMsg is the debounced render ticker running at ~30ms intervals (~33 FPS).
// It decouples LLM token reception (stream ingestion) from TUI model re-renders.
// Token handlers MUST NOT invoke markdown AST parsing or table width layout inside
// StreamChunkMsg; instead they only append to the byte-safe StreamBuffer.
// FrameTickMsg drains the buffer via ReadValidString and triggers a re-render
// only when updated==true.
type FrameTickMsg time.Time

// FrameTickInterval is the debounced frame interval: 30ms (~33 FPS).
const FrameTickInterval = 30 * time.Millisecond

// FrameTickCmd returns a tea.Cmd that sends a FrameTickMsg after FrameTickInterval.
func FrameTickCmd() tea.Cmd {
	return tea.Tick(FrameTickInterval, func(t time.Time) tea.Msg {
		return FrameTickMsg(t)
	})
}
