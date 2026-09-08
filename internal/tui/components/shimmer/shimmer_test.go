package shimmer

import (
	"strings"
	"testing"
)

// stripANSI removes ANSI escape sequences so tests can inspect the runes.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestRenderPreservesText(t *testing.T) {
	text := "Thinking..."
	out := Render(text, 0, 0)
	if stripANSI(out) != text {
		t.Fatalf("render dropped text: got %q want %q", stripANSI(out), text)
	}
	if !strings.Contains(out, ansiReset) {
		t.Fatal("render must terminate with an ANSI reset")
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := Render("", 0, 0); got != "" {
		t.Fatalf("Render(\"\") = %q, want empty", got)
	}
}

func TestRenderWideRunes(t *testing.T) {
	text := "评估政策..."
	out := Render(text, 0, 0)
	if stripANSI(out) != text {
		t.Fatalf("wide-rune render dropped text: got %q want %q", stripANSI(out), text)
	}
}

func TestRenderWaveMovesRight(t *testing.T) {
	// With a single-frame step the wave centre moves one cell per frame, so
	// frame 1 must highlight a different rune than frame 0 for a long text.
	text := "Executing strategy for the current task"
	f0 := Render(text, 0, 0)
	f1 := Render(text, 1, 0)
	if f0 == f1 {
		t.Fatal("frame 0 and frame 1 rendered identically — sweep is not animating")
	}
}

func TestRenderConvergesToBaseFarFromWave(t *testing.T) {
	// The leftmost rune at a large frame offset must be drawn in the resting
	// base colour (no highlight bleed when the wave has moved away).
	text := "Evaluating policy..."
	out := Render(text, 100000, 0)
	// Resting colour is the first 24-bit fg sequence emitted.
	if !strings.Contains(out, "38;2;108;112;134") {
		t.Fatalf("expected resting base colour #6c7086 (108;112;134) in output, got %q", out)
	}
}

func TestModelUpdateActiveLoop(t *testing.T) {
	m := New("Thinking...")
	m.SetActive(true)
	if !m.Active {
		t.Fatal("SetActive(true) did not enable animation")
	}
	m2, cmd := m.Update(FrameMsg{})
	if m2.Frame != m.Frame+1 {
		t.Fatalf("frame = %d, want %d", m2.Frame, m.Frame+1)
	}
	if cmd == nil {
		t.Fatal("active shimmer must re-schedule the next tick")
	}
}

func TestModelUpdateInactiveStopsLoop(t *testing.T) {
	m := New("Thinking...")
	// Active is false by default.
	m2, cmd := m.Update(FrameMsg{})
	if m2.Frame != m.Frame {
		t.Fatalf("inactive shimmer advanced frame: %d → %d", m.Frame, m2.Frame)
	}
	if cmd != nil {
		t.Fatal("inactive shimmer must not re-schedule the tick loop")
	}
}

func TestTickSchedulesFrame(t *testing.T) {
	cmd := Tick()
	if cmd == nil {
		t.Fatal("Tick() returned nil")
	}
	// Execute the cmd to prove it yields a FrameMsg.
	msg := cmd()
	if _, ok := msg.(FrameMsg); !ok {
		t.Fatalf("Tick() produced %T, want shimmer.FrameMsg", msg)
	}
}

func TestViewInactiveStatic(t *testing.T) {
	m := New("Thinking...")
	out := m.View()
	if stripANSI(out) != "Thinking..." {
		t.Fatalf("inactive View = %q, want static text", stripANSI(out))
	}
}

func TestViewActiveAnimates(t *testing.T) {
	m := New("Thinking...")
	m.SetActive(true)
	v0 := m.View()
	m.Frame++
	v1 := m.View()
	if v0 == v1 {
		t.Fatal("active View did not change between frames")
	}
}

func TestSetText(t *testing.T) {
	m := New("Thinking...")
	m.SetText("Evaluating policy...")
	if m.Text != "Evaluating policy..." {
		t.Fatalf("SetText left Text = %q", m.Text)
	}
}
