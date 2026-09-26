package components

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// brailleDot matches the smooth braille dot glyphs. Every animation frame must
// be one of these: an ASCII fallback or a snowflake breaks the visual identity
// with the rest of the TUI and is the classic "the spinner changed shape
// between frames" jitter report.
var brailleDot = regexp.MustCompile(`^[\x{2800}-\x{28ff}]$`)

// TestSpinnerTickRateIs100ms pins the animation cadence. 100ms × a 10-glyph
// cycle is a 1.0s rotation — slow enough to read as deliberate motion, fast
// enough that the indicator never looks stuck.
func TestSpinnerTickRateIs100ms(t *testing.T) {
	if SpinnerTickInterval != 100*time.Millisecond {
		t.Fatalf("SpinnerTickInterval = %v, want 100ms", SpinnerTickInterval)
	}
	frames := uint64(len(DotsFrames))
	if frames == 0 {
		t.Fatal("DotsFrames is empty: the spinner has no glyph cycle")
	}
	rotation := time.Duration(frames) * SpinnerTickInterval
	if rotation > 1500*time.Millisecond {
		t.Errorf("full rotation = %v over %d frames; too slow to read as live", rotation, frames)
	}
}

// TestSpinnerUsesSmoothBrailleDotSet pins the glyph set to the canonical
// bubbles' MiniDot cycle, so it can never drift from the framework definition.
func TestSpinnerUsesSmoothBrailleDotSet(t *testing.T) {
	if len(DotsFrames) != 10 {
		t.Fatalf("DotsFrames has %d glyphs, want the 10-glyph MiniDot cycle", len(DotsFrames))
	}
	seen := map[string]bool{}
	for i, f := range DotsFrames {
		if !brailleDot.MatchString(f) {
			t.Errorf("frame %d %q is not a braille dot glyph", i, f)
		}
		if seen[f] {
			t.Errorf("frame %d repeats glyph %q: the cycle would visibly stutter", i, f)
		}
		seen[f] = true
	}
}

// TestSpinnerGlyphAdvancesMonotonically proves the animation is a clean orbit:
// every frame yields a different glyph, wrapping only after a full cycle. A
// stuck or repeating frame is the "the spinner is frozen" report.
func TestSpinnerGlyphAdvancesMonotonically(t *testing.T) {
	n := uint64(len(DotsFrames))
	seen := make([]string, n)
	for f := range n {
		raw := ansi.Strip(SpinnerGlyph(f))
		if !brailleDot.MatchString(raw) {
			t.Fatalf("frame %d rendered %q, want a bare braille glyph", f, raw)
		}
		seen[f] = raw
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] == seen[i-1] {
			t.Errorf("frames %d and %d render the same glyph %q: no motion", i-1, i, seen[i])
		}
	}
	// Wrapping past the cycle must return to frame 0, never a fresh glyph.
	if got := ansi.Strip(SpinnerGlyph(n)); got != seen[0] {
		t.Errorf("frame %d rendered %q, want wrap-around %q", n, got, seen[0])
	}
}

// TestSpinnerEmeraldRampTransitions pins the colour contract: the glyph is
// rendered through a single-hue green→emerald ramp that ADVANCES with the frame.
// A static colour makes the eye read the indicator as a stuck shape; a ramp
// spanning multiple hues would make a healthy "working" state look like a
// warning.
func TestSpinnerEmeraldRampTransitions(t *testing.T) {
	sgrRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	extract := func(frame uint64) string {
		return strings.Join(sgrRe.FindAllString(SpinnerGlyph(frame), -1), ",")
	}

	first := extract(0)
	if first == "" {
		t.Fatal("spinner glyph is unstyled: the emerald ramp is not applied")
	}
	// Frames 0..n-1 must not all share one colour, or there is no transition.
	distinct := map[string]bool{}
	for f := range uint64(len(DotsFrames)) {
		distinct[extract(f)] = true
	}
	if len(distinct) < 2 {
		t.Errorf("spinner renders a single colour across all %d frames: no perceived motion", len(DotsFrames))
	}
	// The ramp must stay inside one hue family (green/emerald): every step is a
	// near-literal triple from the ramp, never an amber/red literal.
	for _, c := range emeraldRamp {
		if c != "#a6e3a1" && !isGreenStep(string(c)) {
			t.Errorf("ramp colour %s is outside the green/emerald family", c)
		}
	}
}

// isGreenStep reports whether a hex colour is a green-dominant step of the
// emerald ramp (G strictly greater than R and B).
func isGreenStep(hex string) bool {
	if len(hex) != 7 {
		return false
	}
	hexTo := func(i int) int {
		v := 0
		for _, c := range hex[i : i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= int(c - '0')
			case c >= 'a' && c <= 'f':
				v |= int(c-'a') + 10
			}
		}
		return v
	}
	r, g, b := hexTo(1), hexTo(3), hexTo(5)
	return g > r && g >= b
}

// TestSpinnerViewEmitsColouredGlyphThroughAFullCycle drives the real
// Spinner/Update/View trio for one full rotation and asserts each rendered frame
// is a styled, distinct braille glyph.
func TestSpinnerViewEmitsColouredGlyphThroughAFullCycle(t *testing.T) {
	sp := NewSpinner(nil)
	seen := map[string]bool{}
	for range len(DotsFrames) {
		if cmd := sp.Update(SpinnerTickMsg(time.Now())); cmd == nil {
			t.Fatal("Update must re-arm the ticker on every tick")
		}
		out := sp.View()
		if !strings.Contains(out, "\x1b[") {
			t.Errorf("View() frame %d is unstyled: %q", sp.Frame(), out)
		}
		glyph := ansi.Strip(out)
		if len(glyph) < 1 {
			t.Fatalf("View() produced no glyph: %q", out)
		}
		seen[glyph] = true
	}
	if len(seen) != len(DotsFrames) {
		t.Errorf("a full rotation produced %d distinct frames, want %d", len(seen), len(DotsFrames))
	}
}

// TestSpinnerViewIsNilSafe keeps the degraded harness path panic-free.
func TestSpinnerViewIsNilSafe(t *testing.T) {
	var sp *Spinner
	if out := sp.View(); !brailleDot.MatchString(ansi.Strip(out)) {
		t.Errorf("nil Spinner.View() = %q, want a braille glyph", ansi.Strip(out))
	}
	if sp.Update(SpinnerTickMsg(time.Now())) != nil {
		t.Error("nil Spinner.Update must return a nil cmd")
	}
}
