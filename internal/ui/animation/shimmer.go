// Package animation implements the frame-driven TEXT colour wave used by the
// transient pre-execution indicators.
//
// WHY A SINE AND NOT A TRAVELLING BAND. A highlight band that travels across a
// line is a good fit for a long block of prose, but the indicators it dresses
// are one or two words of status ("[code] Formatting code block..."). On a
// short line a band either (a) sits still while the eye waits for it to arrive,
// or (b) wraps around the visible text and strobes, because the band is wider
// than the line. A CONTINUOUS SINE fixes both: every character is always
// mid-animation, so there is no arrival to wait for and no wrap to strobe on,
// and the wave is spatially periodic over a period of 2π/0.25 ≈ 25 characters
// — comfortably wider than the longest indicator, so the gradient reads as one
// smooth left-to-right ramp rather than as a visible cycle seam.
//
// The per-character rule is exactly:
//
//	phase     = (frameTick * 0.15) - (charIndex * 0.25)
//	intensity = (sin(phase) + 1.0) / 2.0
//
// intensity ∈ [0,1] is then used to interpolate the foreground from Muted
// Emerald to Active Emerald. Both constants are named (FramePhaseStep /
// CharPhaseStep) rather than inlined so the cadence and the spatial period are
// tunable in one place and a test can assert the formula instead of a
// hand-copied literal.
//
// ── ANTI-FLICKER CONTRACT ────────────────────────────────────────────────────
//
// 0.15 rad per frame at the 10 Hz animation cadence is a ~0.96 rad/second
// sweep: a character crosses its full dim→bright→dim cycle in about 6.5s. The
// eye reads that as a slow, continuous glow with no perceptible per-frame
// step, which is the whole point — a faster phase constant makes the
// brightness jump between frames and the indicator reads as a flicker, which
// is indistinguishable from an unstable render.
//
// ── NO LAYOUT WIDTH DISTORTION ──────────────────────────────────────────────
//
// Colours are emitted as raw 24-bit TrueColor SGR sequences
// (`ESC[38;2;R;G;Bm`), NOT through lipgloss, for two reasons:
//
//  1. lipgloss renders a style to PLAIN TEXT whenever the active terminal
//     colour profile is Ascii — which is exactly a pipe, a test harness, and
//     TERM=dumb. An indicator that loses its wave in every non-TTY capture is
//     an indicator nobody can screenshot or assert on. Raw SGR is emitted
//     unconditionally, so the wave is present in a capture and in a terminal
//     alike (and a real terminal still honours it, downgrading only if the
//     user's profile forces it).
//
//  2. The per-character colouring must not perturb the cell grid. Every rune is
//     emitted verbatim between SGR prefixes, and the prefixes are skipped when
//     the colour is unchanged from the previous cell, so
//     runewidth.StringWidth(Strip(Render(s, f))) == runewidth.StringWidth(s)
//     for every input. Render also strips embedded newlines: a status line that
//     emitted a newline would become two rows in the viewport, which is the
//     exact failure the single-line invariant exists to prevent.
package animation

import (
	"math"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Emerald ramp endpoints. MutedEmerald is the resting tone (a desaturated
// forest green that stays legible against a dark terminal without competing
// with the text around it); ActiveEmerald is the crest of the wave. Both live
// in one hue family so the wave can never read as a state change — a hue shift
// would look like an error, which would be a lie.
const (
	MutedEmerald  = "#1f4d3e"
	ActiveEmerald = "#34d399"
)

// Phase constants of the colour wave. See the package doc for the formula and
// the anti-flicker reasoning behind FramePhaseStep.
const (
	// FramePhaseStep is radians of phase advanced per animation frame.
	FramePhaseStep = 0.15
	// CharPhaseStep is radians of phase advanced per character of horizontal
	// distance — the spatial period is 2π/CharPhaseStep ≈ 25.1 cells.
	CharPhaseStep = 0.25
)

// Reset is the SGR sequence that clears every attribute. Every Render ends
// with it so a shimmering line can never leak its colour into the next row.
const Reset = "\x1b[0m"

// sgrPrefix is the literal head of a 24-bit foreground SGR sequence. The
// numeric parameters are appended in decimal; building the sequence by
// concatenation (rather than fmt.Sprintf) keeps the per-cell cost to a handful
// of byte appends.
const sgrPrefix = "\x1b[38;2;"

// Elision is the marker used when a line is too wide for the terminal. It is
// one cell wide in every font that has a braille block (the same block the
// spinner uses), and falls back to "..." semantics through ClampCells.
const Elision = "…"

// RGB is an 8-bit-per-channel colour.
type RGB struct {
	R uint8
	G uint8
	B uint8
}

// String renders the colour as a "#rrggbb" literal.
func (c RGB) String() string {
	return "#" + hex2(c.R) + hex2(c.G) + hex2(c.B)
}

func hex2(v uint8) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>4], digits[v&0x0f]})
}

// Intensity returns the wave intensity in [0,1] for the character at cell
// index `index` on animation frame `frame`:
//
//	phase     = (frame * FramePhaseStep) - (index * CharPhaseStep)
//	intensity = (sin(phase) + 1.0) / 2.0
//
// A negative frame is treated as its positive counterpart (the wave is a pure
// function of the phase, so the sign only shifts the wave position).
func Intensity(frame, index int) float64 {
	if frame < 0 {
		frame = -frame
	}
	if index < 0 {
		index = -index
	}
	phase := float64(frame)*FramePhaseStep - float64(index)*CharPhaseStep
	return (math.Sin(phase) + 1.0) / 2.0
}

// ColorAt resolves the wave colour for one cell. It is the single place the
// ramp interpolation lives, so Intensity/ColorAt/Render can never disagree
// about what a given frame looks like.
func ColorAt(frame, index int) RGB {
	return Lerp(ParseHex(MutedEmerald), ParseHex(ActiveEmerald), Intensity(frame, index))
}

// Lerp linearly interpolates a → b by t, clamping t to [0,1] so a caller can
// pass a computed intensity without re-checking the range.
func Lerp(a, b RGB, t float64) RGB {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return RGB{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
	}
}

// ParseHex parses a "#rrggbb" literal. Anything else yields the zero colour
// (black), which keeps a malformed constant from panicking the render path.
func ParseHex(hex string) RGB {
	if len(hex) != 7 || hex[0] != '#' {
		return RGB{}
	}
	rv, ok1 := parsePair(hex[1:3])
	gv, ok2 := parsePair(hex[3:5])
	bv, ok3 := parsePair(hex[5:7])
	if !ok1 || !ok2 || !ok3 {
		return RGB{}
	}
	return RGB{R: rv, G: gv, B: bv}
}

func parsePair(s string) (uint8, bool) {
	if len(s) != 2 {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 8)
	if err != nil {
		return 0, false
	}
	return uint8(v), true
}

// Render colours text with the emerald wave for the given animation frame and
// returns it as a single line of TrueColor SGR.
//
// Guarantees, all of which the single-line indicator contract depends on:
//
//   - The result contains no '\n'. Embedded newlines/CR/TAB are folded to a
//     single space, so the output always occupies exactly one physical row.
//   - Strip(out) == text (modulo the control folding above), and
//     VisibleWidth(out) == VisibleWidth(text): no cell is added or removed by
//     the styling.
//   - Empty (or whitespace-only) text returns "" — there is nothing to
//     animate, and a lone SGR prefix would be pure noise.
//   - The line ends with Reset, and the SGR prefix is emitted only when the
//     colour actually changes, so an all-muted frame costs almost no bytes.
//
// The wave is indexed by DISPLAY CELL, not by rune ordinal, so a wide (CJK)
// glyph advances the gradient by the two cells it occupies and the gradient
// stays visually uniform across mixed-width text. For the single-width ASCII
// the indicators use, cell index and char index are the same number.
func Render(text string, frame int) string {
	if text == "" {
		return ""
	}
	runes := Flatten(text)
	if len(runes) == 0 {
		return ""
	}
	var b strings.Builder
	// 24 bytes/rune is ample: one 19-byte SGR prefix per cell plus the rune.
	b.Grow(len(runes) * 24)

	cell := 0
	haveColor := false
	var prev RGB
	for _, r := range runes {
		c := ColorAt(frame, cell)
		if !haveColor || c != prev {
			b.WriteString(sgrPrefix)
			writeUint8(&b, c.R)
			b.WriteByte(';')
			writeUint8(&b, c.G)
			b.WriteByte(';')
			writeUint8(&b, c.B)
			b.WriteString("m")
			prev = c
			haveColor = true
		}
		b.WriteRune(r)
		cell += runeCells(r)
	}
	b.WriteString(Reset)
	return b.String()
}

// writeUint8 appends v (0–255) to b as decimal digits, with no allocation
// beyond the three-byte scratch on the stack. It is the hot inner loop of
// Render: three calls per styled cell.
func writeUint8(b *strings.Builder, v uint8) {
	if v >= 100 {
		b.WriteByte(byte('0' + v/100))
		v %= 100
		b.WriteByte(byte('0' + v/10))
		b.WriteByte(byte('0' + v%10))
		return
	}
	if v >= 10 {
		b.WriteByte(byte('0' + v/10))
		b.WriteByte(byte('0' + v%10))
		return
	}
	b.WriteByte(byte('0' + v))
}

// RenderPlain returns text in the resting (muted emerald) tone with no wave.
// It is what a mounted-but-unanimated indicator renders as on its first frame
// and what a width-constrained view falls back to, so even a frame that cannot
// animate still carries the state colour instead of default terminal text.
func RenderPlain(text string) string {
	runes := Flatten(text)
	if len(runes) == 0 {
		return ""
	}
	c := ParseHex(MutedEmerald)
	var b strings.Builder
	b.Grow(len(runes) * 16)
	b.WriteString(sgrPrefix)
	writeUint8(&b, c.R)
	b.WriteByte(';')
	writeUint8(&b, c.G)
	b.WriteByte(';')
	writeUint8(&b, c.B)
	b.WriteString("m")
	for _, r := range runes {
		b.WriteRune(r)
	}
	b.WriteString(Reset)
	return b.String()
}

// Flatten makes s safe to render on exactly one physical row: every C0/C1
// control character (including CR, LF and TAB) becomes a single space.
//
// It is deliberately MINIMAL. Printable text is preserved byte-for-byte —
// spaces are NOT collapsed and nothing is trimmed — because the renderer's
// contract is that styling never changes a line's cell width, and a
// space-collapsing normaliser would silently reflow any text that legitimately
// contains a double space. (The lifecycle subject normaliser in
// internal/ui/states DOES collapse runs, because there the goal is a tidy
// one-word subject, not a faithful rendering. Two different operations, two
// different names, one per intent.)
//
// A run of control characters becomes ONE space, so "a\r\n\r\nb" flattens to
// "a b" instead of "a   b" and a hostile path cannot inflate the line.
func Flatten(s string) []rune {
	if s == "" {
		return nil
	}
	need := false
	for _, r := range s {
		if isControl(r) {
			need = true
			break
		}
	}
	if !need {
		return []rune(s)
	}
	out := make([]rune, 0, len(s))
	space := false
	for _, r := range s {
		if isControl(r) {
			if !space {
				out = append(out, ' ')
			}
			space = true
			continue
		}
		space = false
		out = append(out, r)
	}
	return out
}

// isControl reports whether r is a C0/C1 control character. Control characters
// occupy zero display cells but DO move the cursor (or clear the screen), so
// they can never survive into a rendered status line.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// Strip removes every SGR/CSI escape sequence from s, leaving the visible text.
// It is the inverse of the styling Render applies, used to measure a rendered
// line's cell width without a dependency on an ANSI-stripping package.
func Strip(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] != 0x1b {
			b.WriteRune(runes[i])
			continue
		}
		// ESC [ ... final-byte(0x40–0x7e) — skip the whole CSI.
		if i+1 < len(runes) && runes[i+1] == '[' {
			j := i + 2
			for j < len(runes) && (runes[j] < 0x40 || runes[j] > 0x7e) {
				j++
			}
			if j < len(runes) {
				j++
			}
			i = j - 1
			continue
		}
		// ESC ] ... BEL/ST — skip a string sequence (OSC).
		if i+1 < len(runes) && runes[i+1] == ']' {
			j := i + 2
			for j < len(runes) && runes[j] != 0x07 {
				if runes[j] == 0x1b && j+1 < len(runes) && runes[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			if j < len(runes) {
				j++
			}
			i = j - 1
			continue
		}
		// Any other two-character escape.
		if i+1 < len(runes) {
			i++
		}
	}
	return b.String()
}

// VisibleWidth is the display-cell width of s with ANSI escapes ignored. It is
// the width a terminal actually allocates, so it is the number the single-line
// budget must be measured against.
func VisibleWidth(s string) int {
	return runewidth.StringWidth(Strip(s))
}

// ClampCells elides s to at most `cells` display columns, inserting a single
// Elision marker so the truncation is visible rather than silently cutting a
// file name mid-token. It is the width guard for a long subject: a status line
// that wraps is two rows, and two rows breaks the single-line invariant.
//
// ELISION STRATEGY: THE TAIL WINS. The tail of a path is what identifies the
// subject (".go", "shimmer_skeleton.go", the last path segment), while the head
// is a shared, already-familiar prefix. So the budget is spent on the tail
// first, the marker next, and the head gets whatever is left — which is what
// keeps "[mutation] Staging edit @…on.go" recognisable where a head-first
// truncation would have produced "[muta…".
//
// The result is guaranteed to be at most `cells` wide for every positive
// `cells`, and to contain the marker exactly once whenever the input did not
// already fit. A non-positive budget yields "".
func ClampCells(s string, cells int) string {
	if s == "" || cells <= 0 {
		return ""
	}
	flat := string(Flatten(s))
	if runewidth.StringWidth(flat) <= cells {
		return flat
	}
	markWidth := runewidth.StringWidth(Elision)
	if cells < markWidth {
		// No room even for the marker: there is no honest rendering of a
		// truncated string in zero cells.
		return ""
	}
	if cells == markWidth {
		return Elision
	}
	runes := []rune(flat)
	avail := cells - markWidth

	// Tail first, always leaving at least one cell for the head when the
	// budget allows it: a bare "…on.go" with no visible lead-in is less
	// readable than "e…on.go" and costs exactly the same.
	tailBudget := avail - 1
	if tailBudget < 0 {
		tailBudget = 0
	}
	tailStart, tailWidth := longestSuffix(runes, tailBudget)
	headEnd := longestPrefix(runes, avail-tailWidth)
	return string(runes[:headEnd]) + Elision + string(runes[tailStart:])
}

// longestPrefix returns the number of leading runes of s whose combined width is
// at most budget. A wide rune that would cross the boundary is excluded whole, so
// the prefix never splits a character.
func longestPrefix(s []rune, budget int) int {
	if budget <= 0 {
		return 0
	}
	w := 0
	for i, r := range s {
		rw := runeCells(r)
		if w+rw > budget {
			return i
		}
		w += rw
	}
	return len(s)
}

// longestSuffix returns the start index and width of the longest suffix of s
// that fits in budget. Like longestPrefix it never splits a wide rune.
func longestSuffix(s []rune, budget int) (int, int) {
	if budget <= 0 {
		return len(s), 0
	}
	w := 0
	for i := len(s) - 1; i >= 0; i-- {
		rw := runeCells(s[i])
		if w+rw > budget {
			return i + 1, w
		}
		w += rw
	}
	return 0, w
}

// runeCells is the display width of one rune, floored at 1 so a zero-width
// combining mark can never be used to smuggle content past a width budget.
func runeCells(r rune) int {
	if w := runewidth.RuneWidth(r); w > 0 {
		return w
	}
	return 1
}
