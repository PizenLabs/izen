package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestBiModalVirtualCursor pins the acceptance contract for the bi-modal
// virtual cursor end-to-end through the Update pipeline:
//   - the zero-value blink phase renders the reverse-video SGR block
//   - cursorBlinkTickMsg flips the phase while focused, idle, and not scrolled
//     and perpetually re-arms itself (a self-sustaining 500ms loop, no
//     per-focus-site arming)
//   - a phase flip mid-scroll-burst is suppressed (the scroll frame freezes)
//   - a scroll burst renders the FROZEN static frame (cursor pinned ON) that
//     stays byte-identical across wheel frames, watermarked, zero timers
//   - watermark expiry restores the active blink-mode frame
func TestBiModalVirtualCursor(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := readyChatModel(newTestModel())
	m.applyVirtualCursorMode()
	m.ti.SetValue("bi-modal")
	m.ti.Focus()

	// 1) Zero-value phase = blink-ON reversed SGR block.
	on := m.renderPromptView()
	if !strings.Contains(on, "\x1b[7m") {
		t.Fatalf("blink-ON frame must render a reverse-video SGR block: %q", on)
	}

	// 2) Idle tick flips the phase to hidden (plain character) and re-arms.
	if _, cmd := m.Update(cursorBlinkTickMsg(time.Now())); !m.cursorHiddenPhase {
		t.Fatal("cursorBlinkTickMsg must flip the cursor to the hidden phase while idle+not scrolled")
	} else if cmd == nil {
		t.Fatal("active blink must perpetually re-arm (self-sustaining tick loop)")
	}
	off := m.renderPromptView()
	if strings.Contains(off, "\x1b[7m") {
		t.Fatalf("hidden-phase frame must render the cursor plain (invisible): %q", off)
	}
	if off == on {
		t.Fatal("hidden and visible phases must produce distinct frames")
	}

	// 3) Scroll burst FREEZES the phase: a blink tick mid-burst is inert.
	m.markScrollBurst()
	if !m.isScrollActive() {
		t.Fatal("markScrollBurst must open the burst window")
	}
	before := m.cursorHiddenPhase
	_, _ = m.Update(cursorBlinkTickMsg(time.Now()))
	if m.cursorHiddenPhase != before {
		t.Fatal("blink tick must NOT flip the phase mid-scroll (frame freeze contract)")
	}
	frozen := m.renderPromptViewStatic()
	if !strings.Contains(frozen, "\x1b[7m") {
		t.Fatalf("frozen scroll frame must pin the cursor ON as a reverse-video block: %q", frozen)
	}
	var firstInput string
	for i := 0; i < 5; i++ {
		_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		if !m.isScrollActive() {
			t.Fatalf("wheel %d must keep the burst window open", i)
		}
		ws := m.assembleScreen(nil)
		if !strings.Contains(ws.Input, frozen) {
			t.Fatalf("wheel frame %d must carry the frozen static prompt view", i)
		}
		if i == 0 {
			firstInput = ws.Input
		} else if ws.Input != firstInput {
			t.Fatalf("scroll frames must stay byte-frozen (frame 0 != frame %d)", i)
		}
	}

	// 4) Watermark expiry restores the active frame (still hidden phase).
	m.lastScrollTime = time.Now().Add(-scrollActiveWindow - time.Millisecond)
	if m.isScrollActive() {
		t.Fatal("expired watermark must not read as an active scroll burst")
	}
	if ws := m.assembleScreen(nil); !strings.Contains(ws.Input, off) {
		t.Error("post-expiry frame must restore the active blink-mode prompt view")
	}
}
