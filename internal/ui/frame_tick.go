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

// FrameTickInterval is the default debounced frame interval: 30ms (~33 FPS).
const FrameTickInterval = FrameInterval

// FrameTickCmd returns a tea.Cmd that sends a FrameTickMsg after FrameTickInterval.
func FrameTickCmd() tea.Cmd {
	return frameTickCmdEvery(FrameTickInterval)
}

// frameTickCmdEvery returns a tea.Cmd that sends a FrameTickMsg after d. It is
// the per-model cadence seam: a model armed for the 60 FPS low-latency profile
// re-arms through the same FrameTickMsg handler at HighFrameInterval, so the
// message type — and therefore every consumer — stays single-shaped while the
// visual cadence is a live setting. A non-positive d falls back to the default.
func frameTickCmdEvery(d time.Duration) tea.Cmd {
	if d <= 0 {
		d = FrameTickInterval
	}
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return FrameTickMsg(t)
	})
}
