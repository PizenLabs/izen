package animation

import (
	"math"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// referenceIntensity is an independent transcription of the specified formula:
//
//	phase     = (frameTick * 0.15) - (charIndex * 0.25)
//	intensity = (sin(phase) + 1.0) / 2.0
//
// Rendering the reference separately is what makes this a test of the SPEC
// rather than a test of the implementation: if the constants are retuned, the
// reference stops matching and the change is a deliberate, visible one.
func referenceIntensity(frame, index int) float64 {
	phase := float64(frame)*0.15 - float64(index)*0.25
	return (math.Sin(phase) + 1.0) / 2.0
}

func TestIntensityMatchesSpecifiedFormula(t *testing.T) {
	for frame := 0; frame < 40; frame++ {
		for index := 0; index < 30; index++ {
			want := referenceIntensity(frame, index)
			if got := Intensity(frame, index); math.Abs(got-want) > 1e-12 {
				t.Fatalf("Intensity(%d,%d) = %v, want %v", frame, index, got, want)
			}
		}
	}
}

func TestIntensityIsBounded(t *testing.T) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for frame := 0; frame < 200; frame++ {
		for index := 0; index < 60; index++ {
			v := Intensity(frame, index)
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
			if v < 0 || v > 1 {
				t.Fatalf("Intensity(%d,%d) = %v out of [0,1]", frame, index, v)
			}
		}
	}
	if hi-lo < 0.99 {
		t.Fatalf("wave never reaches full contrast: lo=%v hi=%v", lo, hi)
	}
}

func TestIntensityTreatsNegativesSymmetrically(t *testing.T) {
	for _, pair := range [][2]int{{-3, 0}, {0, -5}, {-7, -2}} {
		if got, want := Intensity(pair[0], pair[1]), Intensity(-pair[0], -pair[1]); got != want {
			t.Errorf("Intensity(%d,%d) = %v, want %v", pair[0], pair[1], got, want)
		}
	}
}

// TestColorAtInterpolatesBetweenTheTwoEndpoints is the ramp contract: the wave
// colour is always ON the muted→active segment, and the sampled (frame, cell)
// grid reaches within a rounding error of both endpoints, so the indicator can
// go fully dim and fully bright.
func TestColorAtInterpolatesBetweenTheTwoEndpoints(t *testing.T) {
	muted := ParseHex(MutedEmerald)
	active := ParseHex(ActiveEmerald)
	if muted != (RGB{0x1f, 0x4d, 0x3e}) {
		t.Fatalf("MutedEmerald parsed as %v", muted)
	}
	if active != (RGB{0x34, 0xd3, 0x99}) {
		t.Fatalf("ActiveEmerald parsed as %v", active)
	}
	// phase is always a multiple of 0.05, so sin(phase)==±1 is arithmetically
	// unreachable; the wave can only approach the endpoints. Assert the
	// approach, and that no sampled colour ever leaves the ramp's box.
	minI, maxI := 1.0, 0.0
	for frame := 0; frame < 400; frame++ {
		for index := 0; index < 60; index++ {
			v := Intensity(frame, index)
			if v < minI {
				minI = v
			}
			if v > maxI {
				maxI = v
			}
			c := ColorAt(frame, index)
			if !between(c.R, muted.R, active.R) ||
				!between(c.G, muted.G, active.G) ||
				!between(c.B, muted.B, active.B) {
				t.Fatalf("ColorAt(%d,%d) = %v left the ramp box %v..%v", frame, index, c, muted, active)
			}
		}
	}
	if minI > 0.001 {
		t.Errorf("the wave never approaches the muted endpoint (min intensity %v)", minI)
	}
	if maxI < 0.999 {
		t.Errorf("the wave never approaches the active endpoint (max intensity %v)", maxI)
	}
}

func between(v, lo, hi uint8) bool {
	if lo > hi {
		lo, hi = hi, lo
	}
	return v >= lo && v <= hi
}

func TestLerpClampsOutOfRangeT(t *testing.T) {
	a := RGB{0, 0, 0}
	b := RGB{100, 200, 255}
	if got := Lerp(a, b, -1); got != a {
		t.Errorf("Lerp(t=-1) = %v, want %v", got, a)
	}
	if got := Lerp(a, b, 2); got != b {
		t.Errorf("Lerp(t=2) = %v, want %v", got, b)
	}
	// Truncating channel arithmetic is intentional and documented: the wave
	// must not round a channel UP past the active endpoint.
	if got := Lerp(a, b, 0.5); got != (RGB{50, 100, 127}) {
		t.Errorf("Lerp(0.5) = %v, want {50 100 127}", got)
	}
}

func TestParseHexRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "#", "#fff", "ffffff", "#12345g", "1f4d3e", "#1f4d3ex"} {
		if got := ParseHex(in); got != (RGB{}) {
			t.Errorf("ParseHex(%q) = %v, want zero colour", in, got)
		}
	}
	// The two ramp constants must round-trip, or the wave would render black.
	for _, in := range []string{MutedEmerald, ActiveEmerald} {
		if ParseHex(in) == (RGB{}) {
			t.Errorf("ParseHex(%q) failed to parse", in)
		}
	}
}

func TestRGBString(t *testing.T) {
	if got := (RGB{0x1f, 0x4d, 0x3e}).String(); got != "#1f4d3e" {
		t.Errorf("String = %q", got)
	}
	if got := (RGB{0, 0x0a, 0xff}).String(); got != "#000aff" {
		t.Errorf("String = %q", got)
	}
}

// TestRenderIsAlwaysOneLine is the single-line invariant at the renderer: no
// input, however hostile, may produce a newline.
func TestRenderIsAlwaysOneLine(t *testing.T) {
	inputs := []string{
		"Formatting code block...",
		"a\nb",
		"a\r\nb",
		"line1\nline2\nline3",
		"tab\there",
		"\n",
		"\n\n\n",
		"\x00\x01\x02",
	}
	for _, in := range inputs {
		out := Render(in, 3)
		if strings.ContainsAny(out, "\n\r") {
			t.Errorf("Render(%q) leaked a line break: %q", in, out)
		}
	}
	if got := Render("", 0); got != "" {
		t.Errorf("Render(\"\") = %q, want empty", got)
	}
	// A whitespace-only string flattens to pure spaces, which the renderer
	// colours like any other text — it is not "no text", it is a blank row the
	// lifecycle has reserved.
	if got := Render("   ", 0); got == "" || strings.Contains(got, "\n") {
		t.Errorf("Render(whitespace) = %q", got)
	}
}

// TestRenderDoesNotDistortLayout is the "without layout width distortion"
// requirement: styling must not add or remove a single display cell.
func TestRenderDoesNotDistortLayout(t *testing.T) {
	inputs := []string{
		"[struct] Constructing table view...",
		"[mutation] Staging edit @internal/ui/components/spinner.go...",
		"",
		"a b  c",
		"日本語のテキスト",
		"mixed 日本語 and ascii",
		"emoji ✅ ok",
		"accented café naïve",
	}
	for _, in := range inputs {
		for _, frame := range []int{0, 1, 7, 42, 1000} {
			out := Render(in, frame)
			if runewidth.StringWidth(Strip(out)) != runewidth.StringWidth(in) {
				t.Errorf("Render(%q, %d) width = %d, want %d",
					in, frame, runewidth.StringWidth(Strip(out)), runewidth.StringWidth(in))
			}
			if got, want := Strip(out), string(Flatten(in)); got != want {
				t.Errorf("Render(%q, %d) stripped = %q, want %q", in, frame, got, want)
			}
		}
	}
}

// TestRenderUsesTrueColorSGR is the "direct TrueColor ANSI" requirement: the
// output must carry 24-bit foreground sequences, not 16-colour or lipgloss-
// degraded text. It also asserts the line is reset, so a shimmering row can
// never leak colour into the next one.
func TestRenderUsesTrueColorSGR(t *testing.T) {
	out := Render("[code] Formatting code block...", 5)
	if !strings.Contains(out, sgrPrefix) {
		t.Fatalf("Render emitted no TrueColor prefix: %q", out)
	}
	if !strings.HasSuffix(out, Reset) {
		t.Fatalf("Render must end with a reset: %q", out)
	}
	// A 16-colour profile would render \x1b[3Xm; none may appear.
	if i := strings.Index(out, "\x1b[3"); i >= 0 && !strings.HasPrefix(out[i:], sgrPrefix) {
		t.Errorf("Render emitted a non-TrueColor SGR at %d: %q", i, out[i:])
	}
	// Every channel must be a real 0–255 decimal in the emerald box.
	seen := 0
	for i := 0; i+len(sgrPrefix) < len(out); i++ {
		if out[i] != 0x1b || !strings.HasPrefix(out[i:], sgrPrefix) {
			continue
		}
		rest := out[i+len(sgrPrefix):]
		end := strings.IndexByte(rest, 'm')
		if end < 0 {
			t.Fatalf("unterminated SGR at %d: %q", i, out[i:])
		}
		parts := strings.Split(rest[:end], ";")
		if len(parts) != 3 {
			t.Fatalf("SGR params = %q, want 3", rest[:end])
		}
		c := ColorAt(5, seen)
		seen++
		want := []string{itoa(c.R), itoa(c.G), itoa(c.B)}
		for k := range parts {
			if parts[k] != want[k] {
				t.Errorf("SGR param %d = %q, want %q (colour %v)", k, parts[k], want[k], c)
			}
		}
		i += len(sgrPrefix) + end
	}
	if seen == 0 {
		t.Fatal("no SGR sequences were emitted")
	}
}

func itoa(v uint8) string {
	if v >= 100 {
		return string([]byte{byte('0' + v/100), byte('0' + (v%100)/10), byte('0' + v%10)})
	}
	if v >= 10 {
		return string([]byte{byte('0' + v/10), byte('0' + v%10)})
	}
	return string([]byte{byte('0' + v)})
}

// TestRenderSkipsRedundantPrefixes documents the byte-reduction rule: a cell
// whose colour matches its predecessor's emits no new SGR. The wave is
// continuous, so over 25 cells there are at most ~25 distinct colours and
// duplicates are common.
func TestRenderSkipsRedundantPrefixes(t *testing.T) {
	out := Render(strings.Repeat("x", 25), 0)
	prefixes := strings.Count(out, sgrPrefix)
	if prefixes == 0 || prefixes > 25 {
		t.Fatalf("expected 1..25 SGR prefixes for 25 cells, got %d", prefixes)
	}
	// Two identical frames must produce byte-identical output (no hidden state).
	if a, b := Render("hello", 11), Render("hello", 11); a != b {
		t.Error("Render is not deterministic")
	}
}

// TestRenderIsSmoothAcrossCharacters is the DoD clause "the color wave
// transitions smoothly across characters without visual flickering": adjacent
// cells must differ by a small, bounded amount — no hard edge anywhere.
func TestRenderIsSmoothAcrossCharacters(t *testing.T) {
	for _, frame := range []int{0, 3, 17, 99} {
		const text = "[mutation] Staging edit @internal/ui/model.go..."
		var prev RGB
		first := true
		for i := range []rune(text) {
			c := ColorAt(frame, i)
			if first {
				prev, first = c, false
				continue
			}
			if chanDist(c, prev) > 24 {
				t.Fatalf("frame %d cell %d: hard colour edge %v → %v", frame, i, prev, c)
			}
			prev = c
		}
	}
}

func chanDist(a, b RGB) int {
	d := 0
	for _, p := range [][2]uint8{{a.R, b.R}, {a.G, b.G}, {a.B, b.B}} {
		x, y := int(p[0]), int(p[1])
		if x < y {
			x, y = y, x
		}
		d += x - y
	}
	return d / 3
}

// TestRenderAnimatesOverTime asserts the wave actually moves: two distant frames
// must produce different output for the same text (a frozen gradient would read
// as a stuck indicator).
func TestRenderAnimatesOverTime(t *testing.T) {
	const text = "[code] Formatting code block..."
	if Render(text, 0) == Render(text, 3) {
		t.Error("the wave did not advance between frames")
	}
	// The glyph content must be identical frame to frame — only colour changes.
	if Strip(Render(text, 0)) != Strip(Render(text, 3)) {
		t.Error("the wave must not alter the visible characters")
	}
}

func TestRenderPlainIsMutedAndFlat(t *testing.T) {
	out := RenderPlain("Formatting code block...")
	if !strings.Contains(out, sgrPrefix) {
		t.Fatalf("RenderPlain emitted no colour: %q", out)
	}
	if strings.Count(out, sgrPrefix) != 1 {
		t.Errorf("RenderPlain must emit exactly one prefix: %q", out)
	}
	if !strings.Contains(out, itoa(ParseHex(MutedEmerald).R)) {
		t.Errorf("RenderPlain did not use the muted emerald tone: %q", out)
	}
	if got := RenderPlain("a\nb"); strings.Contains(got, "\n") {
		t.Errorf("RenderPlain leaked a newline: %q", got)
	}
	if got := RenderPlain(""); got != "" {
		t.Errorf("RenderPlain(\"\") = %q", got)
	}
}

func TestFlattenPreservesPrintableSpacingAndFoldsControls(t *testing.T) {
	cases := map[string]string{
		"a\nb":             "a b",
		"a  b":             "a  b", // printable spacing is preserved verbatim
		"  a":              "  a",
		"a\tb":             "a b",
		"a\r\n\r\nb":       "a b", // a control RUN collapses to one space
		"ab":               "ab",
		"":                 "",
		"\x1b[38;2;1;2;3m": " [38;2;1;2;3m", // ESC folds, so the rest is inert literal text
	}
	for in, want := range cases {
		if got := string(Flatten(in)); got != want {
			t.Errorf("Flatten(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFlattenBlocksANSIInjection is the security clause: a caller-supplied
// indicator text can never smuggle an SGR sequence into the render, because the
// ESC byte is folded to a space and the remainder renders as literal text.
func TestFlattenBlocksANSIInjection(t *testing.T) {
	hostile := "\x1b[31mred\x1b[0m \x1b]0;title\x07"
	out := Render(hostile, 1)
	if strings.Count(out, "\x1b[") != strings.Count(out, "\x1b[38;2;")+1 {
		// Exactly one escape family may appear: the wave's own SGR prefixes,
		// plus the single trailing Reset.
		t.Fatalf("Render let a foreign escape through: %q", out)
	}
	if !strings.HasSuffix(out, Reset) {
		t.Fatalf("Render did not reset: %q", out)
	}
	if strings.ContainsAny(Strip(out), "\x1b") {
		t.Fatal("Strip left an escape byte")
	}
}

func TestStripRemovesEscapeSequences(t *testing.T) {
	if got := Strip(Render("hello", 2)); got != "hello" {
		t.Errorf("Strip(Render) = %q", got)
	}
	if got := Strip("\x1b[38;2;1;2;3mhi\x1b[0m"); got != "hi" {
		t.Errorf("Strip = %q", got)
	}
	if got := Strip("\x1b]0;title\x07text"); got != "text" {
		t.Errorf("Strip OSC = %q", got)
	}
	if got := Strip("\x1bMtwo"); got != "two" {
		t.Errorf("Strip two-char escape = %q", got)
	}
	if got := Strip("plain"); got != "plain" {
		t.Errorf("Strip(plain) = %q", got)
	}
	if got := Strip("\x1b["); got != "" {
		t.Errorf("Strip(truncated CSI) = %q", got)
	}
}

func TestVisibleWidthIgnoresEscapes(t *testing.T) {
	if got := VisibleWidth(Render("abc", 0)); got != 3 {
		t.Errorf("VisibleWidth(Rendered) = %d, want 3", got)
	}
	if got := VisibleWidth("日本"); got != 4 {
		t.Errorf("VisibleWidth(wide) = %d, want 4", got)
	}
}

// TestClampCellsRespectsTheBudget is the width guard: a clamped line must never
// exceed the budget, which is what keeps the indicator on one row.
func TestClampCellsRespectsTheBudget(t *testing.T) {
	// Cases where the input already fits must come back untouched.
	for _, c := range []struct {
		in    string
		cells int
	}{
		{"short", 10},
		{"short", 5},
	} {
		if got := ClampCells(c.in, c.cells); got != c.in {
			t.Errorf("ClampCells(%q, %d) = %q, want it unchanged", c.in, c.cells, got)
		}
	}
	// Cases that must actually be elided.
	for _, c := range []struct {
		in    string
		cells int
	}{
		{"a very long file name indeed.go", 30},
		{"a very long file name indeed.go", 20},
		{"a very long file name indeed.go", 12},
		{"a very long file name indeed.go", 8},
		{"a very long file name indeed.go", 4},
		{"a very long file name indeed.go", 3},
		{"a very long file name indeed.go", 2},
		{"a very long file name indeed.go", 1},
		{"日本語のとても長いファイル名です.go", 10},
		{"日本語のとても長いファイル名です.go", 5},
		{"日本語のとても長いファイル名です.go", 2},
	} {
		got := ClampCells(c.in, c.cells)
		if w := runewidth.StringWidth(got); w > c.cells {
			t.Errorf("ClampCells(%q, %d) = %q (width %d)", c.in, c.cells, got, w)
		}
		if n := strings.Count(got, Elision); n != 1 {
			t.Errorf("ClampCells(%q, %d) = %q has %d elision markers, want 1", c.in, c.cells, got, n)
		}
	}
	if got := ClampCells("abc", 0); got != "" {
		t.Errorf("ClampCells(_, 0) = %q", got)
	}
	if got := ClampCells("abc", -1); got != "" {
		t.Errorf("ClampCells(_, -1) = %q", got)
	}
	if got := ClampCells("", 10); got != "" {
		t.Errorf("ClampCells(\"\", 10) = %q", got)
	}
}

// TestClampCellsIsIdentityWhenItFits: a line that already fits must come back
// byte-identical, so a width change that does not actually constrain anything
// cannot alter the rendered output.
func TestClampCellsIsIdentityWhenItFits(t *testing.T) {
	in := "[code] Formatting code block..."
	if got := ClampCells(in, runewidth.StringWidth(in)); got != in {
		t.Errorf("ClampCells at exact fit = %q, want %q", got, in)
	}
	if got := ClampCells(in, runewidth.StringWidth(in)+5); got != in {
		t.Errorf("ClampCells with slack = %q, want %q", got, in)
	}
}

// TestClampCellsKeepsTheTail asserts the elision strategy: the identifying part
// of a path (its last segment) survives, the expendable part (its prefix) goes.
func TestClampCellsKeepsTheTail(t *testing.T) {
	const in = "internal/ui/components/shimmer_skeleton.go"
	got := ClampCells(in, 18)
	if !strings.Contains(got, ".go") {
		t.Errorf("ClampCells dropped the identifying tail: %q", got)
	}
	if strings.Contains(got, "internal/") {
		t.Errorf("ClampCells kept the long head: %q", got)
	}
	if w := runewidth.StringWidth(got); w > 18 {
		t.Errorf("ClampCells exceeded its budget: %q (width %d)", got, w)
	}
}
