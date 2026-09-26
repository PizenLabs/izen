package components

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/PizenLabs/izen/internal/ui/animation"
	"github.com/PizenLabs/izen/internal/ui/states"
)

// indicator is the canonical mount used across these tests, taken straight from
// the lifecycle state machine so the two can never drift apart.
func indicator(s states.State, target string) string {
	return s.Describe(target)
}

// TestSkeletonIsExactlyOneLine is the DoD's first structural clause: the widget
// occupies one physical row, always, for every state and every width.
func TestSkeletonIsExactlyOneLine(t *testing.T) {
	for _, st := range states.All() {
		target := "internal/ui/components/shimmer_skeleton.go"
		if !st.Targeted() {
			target = ""
		}
		line := indicator(st, target)
		for _, width := range []int{-1, 0, 1, 2, 4, 8, 20, 60, 200} {
			for frame := uint64(0); frame < 12; frame++ {
				out := RenderSkeleton(line, target, frame, width)
				if strings.ContainsAny(out, "\n\r") {
					t.Fatalf("state %s width %d frame %d produced a multi-line row: %q",
						st, width, frame, out)
				}
				if out == "" && line != "" && width > SkeletonGlyphCells+SkeletonGapCells {
					t.Fatalf("state %s width %d frame %d rendered nothing", st, width, frame)
				}
			}
		}
	}
}

// TestSkeletonHeightIsConstantOne makes the single-line invariant a value the
// caller can budget from rather than a claim in a comment.
func TestSkeletonHeightIsConstantOne(t *testing.T) {
	s := NewSkeleton(indicator(states.StateCodePending, ""), "")
	if s.Height() != 1 {
		t.Fatalf("Height = %d, want 1", s.Height())
	}
	// The zero value is also exactly one line tall (and renders nothing).
	var zero Skeleton
	if zero.Height() != 1 {
		t.Fatalf("zero-value Height = %d, want 1", zero.Height())
	}
	if zero.View() != "" {
		t.Fatalf("zero-value View = %q, want empty", zero.View())
	}
	if zero.Active() {
		t.Fatal("zero-value skeleton must be inactive")
	}
}

// TestSkeletonCarriesTheFullIndicator is the "displays a single-line shimmering
// emerald indicator" clause: the visible text is the lifecycle's line, verbatim.
func TestSkeletonCarriesTheFullIndicator(t *testing.T) {
	cases := []struct {
		st     states.State
		target string
	}{
		{states.StateTablePending, ""},
		{states.StateCodePending, ""},
		{states.StateWorkspacePatch, "internal/ui/model.go"},
		{states.StateToolExecution, ""},
		{states.StateAstIndexing, ""},
	}
	for _, c := range cases {
		want := indicator(c.st, c.target)
		got := RenderSkeleton(want, c.target, 4, 200)
		plain := animation.Strip(got)
		if !strings.Contains(plain, want) {
			t.Errorf("state %s: rendered row does not carry %q (got %q)", c.st, want, plain)
		}
	}
}

// TestSkeletonIsShimmeredAndSpinnerLed pins the composition: a MiniDot glyph from
// the emerald ramp, a single space, then the emerald colour wave.
func TestSkeletonIsShimmeredAndSpinnerLed(t *testing.T) {
	s := NewSkeleton(indicator(states.StateCodePending, ""), "")
	s.SetWidth(200)
	out := s.View()

	wantGlyph := DotsFrames[0]
	if !strings.Contains(animation.Strip(out), wantGlyph) {
		t.Fatalf("row does not lead with the spinner glyph %q: %q", wantGlyph, animation.Strip(out))
	}
	// The body is TrueColor-shaded.
	if !strings.Contains(out, "\x1b[38;2;") {
		t.Fatalf("body carries no TrueColor wave: %q", out)
	}
	// Indent + glyph + gap, exactly.
	if !strings.HasPrefix(animation.Strip(out), SkeletonIndent+wantGlyph+" ") {
		t.Fatalf("unexpected composition: %q", animation.Strip(out))
	}
	// No border, no box-drawing chrome: a skeleton is a line, not a panel.
	for _, chrome := range []string{"│", "┌", "└", "─", "█", "╭", "╰"} {
		if strings.Contains(out, chrome) {
			t.Errorf("skeleton rendered chrome %q: %q", chrome, out)
		}
	}
}

// TestSkeletonGlyphCycles is the frame-driven half of the contract: successive
// frames must advance the glyph, or the row reads as a frozen indicator.
func TestSkeletonGlyphCycles(t *testing.T) {
	s := NewSkeleton(indicator(states.StateCodePending, ""), "")
	s.SetWidth(200)
	seen := map[string]bool{}
	for frame := uint64(0); frame < uint64(len(DotsFrames)); frame++ {
		s.SetFrame(frame)
		out := s.View()
		for _, g := range DotsFrames {
			if strings.Contains(animation.Strip(out), g) {
				seen[g] = true
			}
		}
	}
	if len(seen) < 2 {
		t.Fatalf("the glyph did not cycle: saw %d distinct glyphs", len(seen))
	}
}

// TestSkeletonWaveAdvancesWithTheFrame is the colour half: the same text on two
// frames must differ in styling while the visible characters stay identical.
// The two frames are a full glyph-cycle apart so the braille glyph is the same
// and the ONLY difference measured is the colour wave.
func TestSkeletonWaveAdvancesWithTheFrame(t *testing.T) {
	const text = "[index] Mapping workspace context..."
	a := RenderSkeleton(text, "", 0, 200)
	b := RenderSkeleton(text, "", uint64(len(DotsFrames)), 200)
	if a == b {
		t.Fatal("the shimmer did not advance between frames")
	}
	if animation.Strip(a) != animation.Strip(b) {
		t.Fatalf("the wave altered the visible characters: %q vs %q", animation.Strip(a), animation.Strip(b))
	}
}

// TestSkeletonWaveIsSmoothAcrossCharacters is the "transitions smoothly across
// characters without visual flickering" clause, measured on the rendered row:
// no two adjacent cells may jump more than the engine allows.
func TestSkeletonWaveIsSmoothAcrossCharacters(t *testing.T) {
	const text = "[mutation] Staging edit @internal/ui/model.go..."
	for frame := 0; frame < 30; frame++ {
		var prev animation.RGB
		first := true
		for i := range []rune(text) {
			c := animation.ColorAt(frame, i)
			if !first {
				if chanGap(c, prev) > 24 {
					t.Fatalf("frame %d cell %d: hard colour edge %v → %v", frame, i, prev, c)
				}
			}
			prev, first = c, false
		}
	}
}

func chanGap(a, b animation.RGB) int {
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

// TestSkeletonElidesTheSubjectFirst is the "dynamically wraps file names or tool
// targets if terminal width is constrained" clause. The subject goes before the
// sentence around it, because losing the file name is survivable and losing the
// "[mutation] Staging edit" frame is not.
func TestSkeletonElidesTheSubjectFirst(t *testing.T) {
	const target = "internal/ui/components/shimmer_skeleton.go"
	text := indicator(states.StateWorkspacePatch, target)
	s := NewSkeleton(text, target)
	s.SetWidth(46) // fits "[mutation] Staging edit @" but not the whole path
	out := s.View()
	plain := animation.Strip(out)

	if strings.Contains(plain, target) {
		t.Fatalf("the subject was not elided at width 46: %q", plain)
	}
	if !strings.Contains(plain, "[mutation] Staging edit") {
		t.Fatalf("the indicator frame was sacrificed instead of the subject: %q", plain)
	}
	if !strings.Contains(plain, animation.Elision) {
		t.Fatalf("elision was not marked: %q", plain)
	}
	// The identifying tail of the path must survive.
	if !strings.HasSuffix(strings.TrimSpace(plain), "animation.Elision") &&
		!strings.Contains(plain, ".go") {
		t.Fatalf("elision dropped the identifying tail: %q", plain)
	}
}

// TestSkeletonNeverExceedsItsWidthBudget is the hard single-line guard: at any
// width, the rendered row must fit, or the terminal wraps it into two rows and
// the one-line contract is void.
func TestSkeletonNeverExceedsItsWidthBudget(t *testing.T) {
	const target = "internal/ui/components/shimmer_skeleton.go"
	for _, st := range states.All() {
		tgt := ""
		if st.Targeted() {
			tgt = target
		}
		text := indicator(st, tgt)
		for width := SkeletonGlyphCells + SkeletonGapCells + 1; width <= 120; width++ {
			s := NewSkeleton(text, tgt)
			s.SetWidth(width)
			s.SetFrame(uint64(width))
			if got := runewidth.StringWidth(animation.Strip(s.View())); got > width {
				t.Fatalf("state %s at width %d rendered %d cells: %q",
					st, width, got, animation.Strip(s.View()))
			}
		}
	}
}

// TestSkeletonDegradesToGlyphWhenTheBodyCannotFit documents the narrow-terminal
// floor: with no room for a sentence, the row is the spinner alone — still
// exactly one line, and still replaced seamlessly by the real content.
func TestSkeletonDegradesToGlyphWhenTheBodyCannotFit(t *testing.T) {
	s := NewSkeleton(indicator(states.StateWorkspacePatch, "a.go"), "a.go")
	s.SetWidth(1)
	out := s.View()
	if strings.Contains(out, "\n") {
		t.Fatalf("narrow render wrapped: %q", out)
	}
	if runewidth.StringWidth(animation.Strip(out)) > 1 {
		t.Fatalf("narrow render exceeded the budget: %q", out)
	}
	// The indent is dropped too when it does not fit, rather than pushing the
	// row over the edge.
	if !strings.Contains(animation.Strip(out), DotsFrames[0]) {
		t.Fatalf("narrow render lost the glyph: %q", out)
	}
}

// TestSkeletonStaysOnOneLineForHostileText keeps the widget total for text that
// arrived from a file name, a tool name, or a model.
func TestSkeletonStaysOnOneLineForHostileText(t *testing.T) {
	hostile := []string{
		"line one\nline two",
		"carriage\rreturn",
		"tab\tseparated",
		"\x1b[31mred\x1b[0m",
		"nul\x00byte",
		strings.Repeat("x", 500),
		"日本語" + strings.Repeat("語", 80),
	}
	for _, in := range hostile {
		for _, width := range []int{-1, 0, 3, 10, 40, 200} {
			out := RenderSkeleton(in, in, 5, width)
			if strings.ContainsAny(out, "\n\r") {
				t.Fatalf("hostile text %q at width %d produced a multi-line row: %q", in, width, out)
			}
			if width > 0 && runewidth.StringWidth(animation.Strip(out)) > width {
				t.Fatalf("hostile text %q at width %d exceeded the budget: %q", in, width, out)
			}
		}
	}
}

// TestSkeletonSetAccessorsAndNilSafety: every mutator is nil-safe so a partially
// constructed view (or a test harness) can never panic the render path.
func TestSkeletonSetAccessorsAndNilSafety(t *testing.T) {
	s := NewSkeleton("a", "a.go")
	s.Set("b", "b.go")
	if s.Text() != "b" || s.Target() != "b.go" {
		t.Fatalf("Set → %q / %q", s.Text(), s.Target())
	}
	s.SetText("c")
	if s.Text() != "c" || s.Target() != "b.go" {
		t.Fatalf("SetText clobbered the target: %q / %q", s.Text(), s.Target())
	}
	s.SetTarget("c.go")
	if s.Target() != "c.go" {
		t.Fatalf("SetTarget = %q", s.Target())
	}
	s.SetFrame(9)
	if s.Frame() != 9 {
		t.Fatalf("Frame = %d", s.Frame())
	}
	s.SetWidth(33)
	if s.Width() != 33 {
		t.Fatalf("Width = %d", s.Width())
	}
	s.SetIndent("> ")
	if !strings.HasPrefix(animation.Strip(s.View()), "> ") {
		t.Fatalf("SetIndent ignored: %q", animation.Strip(s.View()))
	}
	s.SetIndent("")
	if !strings.HasPrefix(animation.Strip(s.View()), SkeletonIndent) {
		t.Fatalf("an empty indent must fall back to the default: %q", animation.Strip(s.View()))
	}
	s.SetText("   ")
	if s.Active() {
		t.Fatal("a whitespace-only indicator must be inactive")
	}
	if s.View() != "" {
		t.Fatalf("inactive skeleton rendered %q", s.View())
	}

	var nilSkel *Skeleton
	nilSkel.Set("a", "b")
	nilSkel.SetText("a")
	nilSkel.SetTarget("b")
	nilSkel.SetFrame(1)
	nilSkel.SetWidth(1)
	nilSkel.SetIndent("")
	if nilSkel.View() != "" || nilSkel.Active() || nilSkel.Text() != "" ||
		nilSkel.Target() != "" || nilSkel.Frame() != 0 || nilSkel.Width() != 0 ||
		nilSkel.WidthOf() != 0 {
		t.Fatal("nil skeleton is not inert")
	}
}

// TestSkeletonUnsetWidthIsUnconstrained: a pre-bootstrap frame (no WindowSizeMsg
// yet) must render the full indicator rather than clamping to nothing.
func TestSkeletonUnsetWidthIsUnconstrained(t *testing.T) {
	const text = "[mutation] Staging edit @internal/ui/model.go..."
	s := NewSkeleton(text, "internal/ui/model.go")
	plain := animation.Strip(s.View())
	if plain != SkeletonIndent+DotsFrames[0]+" "+text {
		t.Fatalf("unconstrained render = %q", plain)
	}
}

// TestSkeletonWidthOfMatchesTheRenderedRow keeps the budgeting API honest: what
// WidthOf reports is exactly what View occupies.
func TestSkeletonWidthOfMatchesTheRenderedRow(t *testing.T) {
	for _, st := range states.All() {
		tgt := ""
		if st.Targeted() {
			tgt = "internal/ui/model.go"
		}
		text := indicator(st, tgt)
		for _, width := range []int{-1, 0, 20, 50, 200} {
			s := NewSkeleton(text, tgt)
			s.SetWidth(width)
			s.SetFrame(3)
			if got, want := s.WidthOf(), runewidth.StringWidth(animation.Strip(s.View())); got != want {
				t.Errorf("state %s width %d: WidthOf = %d, rendered %d", st, width, got, want)
			}
		}
	}
}

// TestSkeletonClampBodyIsIdentityWhenItFits: a width change that does not
// actually constrain must not alter a single byte of the row.
func TestSkeletonClampBodyIsIdentityWhenItFits(t *testing.T) {
	const text = "[exec] Preparing tool execution..."
	if got := clampBody(text, "", 100); got != text {
		t.Errorf("clampBody with slack = %q, want %q", got, text)
	}
	if got := clampBody(text, "", -1); got != text {
		t.Errorf("clampBody unconstrained = %q, want %q", got, text)
	}
	if got := clampBody("", "", 10); got != "" {
		t.Errorf("clampBody(\"\") = %q", got)
	}
	// No target, no room: fall back to a whole-line elision rather than
	// silently dropping the sentence. The documented strategy is tail-wins, so
	// the assertion is on the budget, the marker, and the surviving tail —
	// not on which particular rune happened to lead.
	got := clampBody(text, "", 8)
	if runewidth.StringWidth(got) > 8 {
		t.Errorf("clampBody without a target exceeded the budget: %q", got)
	}
	if !strings.Contains(got, animation.Elision) {
		t.Errorf("clampBody without a target lost the marker: %q", got)
	}
	if !strings.HasSuffix(got, "n...") {
		t.Errorf("clampBody without a target dropped the identifying tail: %q", got)
	}
	// A target that is not present in the text must not confuse the search.
	if got := clampBody(text, "not/in/text.go", 8); !strings.Contains(got, animation.Elision) {
		t.Errorf("clampBody with an absent target = %q", got)
	}
	// A target that leaves no room at all falls back to whole-line elision.
	if got := clampBody("[mutation] Staging edit @x.go", "x.go", 6); !strings.Contains(got, animation.Elision) {
		t.Errorf("clampBody with no room for the target = %q", got)
	}
}

// TestSkeletonFrameSequenceIsDeterministic walks the whole animation cycle the
// way the event loop does — mutate, render, mutate, render — and asserts that
// (a) the same settings always render the same row (no hidden state, so a
// paused/resumed frame can never show a stale colour) and (b) every frame in
// the cycle is still exactly one line inside the budget.
func TestSkeletonFrameSequenceIsDeterministic(t *testing.T) {
	const target = "internal/ui/model.go"
	text := indicator(states.StateWorkspacePatch, target)
	s := NewSkeleton(text, target)
	s.SetWidth(60)
	seen := make(map[string]struct{})
	for round := 0; round < 3; round++ {
		for frame := uint64(0); frame < uint64(len(DotsFrames))*2; frame++ {
			s.SetFrame(frame)
			out := s.View()
			if strings.ContainsAny(out, "\n\r") {
				t.Fatalf("frame %d produced a multi-line row", frame)
			}
			if w := runewidth.StringWidth(animation.Strip(out)); w > 60 {
				t.Fatalf("frame %d exceeded the width budget: %d cells", frame, w)
			}
			if round == 0 {
				seen[out] = struct{}{}
			} else if _, ok := seen[out]; !ok {
				t.Fatalf("frame %d rendered differently on a later pass: %q", frame, out)
			}
		}
	}
	// The cycle must actually animate: more than one distinct row per glyph.
	if len(seen) < 2 {
		t.Fatalf("the frame sequence produced %d distinct rows", len(seen))
	}
}
