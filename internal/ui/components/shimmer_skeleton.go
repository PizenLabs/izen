// Package components — shimmer_skeleton.go implements the TRANSIENT
// pre-execution skeleton: a zero-border, exactly-one-line placeholder the
// viewport mounts while the runtime is producing a surface it cannot show yet
// (a table being constructed, a code block being formatted, an edit being
// staged, a tool being prepared, a workspace being mapped).
//
// WHY A SEPARATE WIDGET AND NOT ANOTHER LOADING DOCK. The loading dock
// (loading.go) is a MODAL status surface: it owns a status line plus a
// contextual tip, it is driven by the shimmer, and it disappears when the first
// stream token arrives. It is the wrong shape for a skeleton, because a
// skeleton is:
//
//   - ONE LINE, ALWAYS. The dock is two lines the moment a tip is available. A
//     pre-execution indicator is a placeholder for content that is not here
//     yet, and a placeholder that occupies two rows displaces a row of real
//     output when it is replaced. Height is part of the contract, not a
//     rendering detail, so Height() is a constant rather than a measurement.
//
//   - ZERO-BORDER. The dock is a surface with its own visual identity. A
//     skeleton is a line INSIDE the conversation thread: no frame, no padding
//     box, no background — anything that reads as a container would make the
//     eventual replacement look like the content was swapped out from under a
//     panel the user never asked for.
//
//   - ANIMATED BY A COLOUR WAVE, NOT A MOVING BAND. A one-line status has no
//     room for a travelling highlight to read as motion; the continuous sine
//     wave in internal/ui/animation keeps every cell mid-animation instead, and
//     cannot strobe on a line shorter than its own period.
//
//   - MOUNTED/UNMOUNTED BY EXACT LIFECYCLE, NOT BY A FLAG. See the view-side
//     ledger in internal/ui/skeleton.go, which owns the atomic replacement.
//
// COMPONENT BOUNDARY. The widget is a pure projection: it holds text, a subject,
// a frame counter, and a width budget, and renders. It performs no I/O, takes no
// locks, and mutates nothing outside itself, so it is safe to call from the
// render path and trivial to assert on in a test.
//
// GOROUTINE CONFINEMENT. Like every other Bubble Tea model value in this
// package's siblings, a Skeleton is confined to the event loop: its mutators
// (Set/SetText/SetTarget/SetFrame/SetWidth/SetIndent) and its View are all called
// from Update/View on one goroutine, so it needs no synchronisation. It is
// deliberately NOT atomic-backed: unlike Spinner, nothing publishes to a
// skeleton from a backend worker, so an atomic would be cost without a
// producer. A caller that needs cross-goroutine access must confine the widget
// itself.
package components

import (
	"strings"

	"github.com/PizenLabs/izen/internal/ui/animation"
)

// Skeleton geometry constants.
const (
	// SkeletonGlyphCells is the display width of the spinner glyph that
	// prefixes the line. The braille dot set is a single cell; the constant
	// exists so the width budget is computed in one named place instead of a
	// bare 1 in an arithmetic expression.
	SkeletonGlyphCells = 1

	// SkeletonGapCells is the single space between the glyph and the text.
	SkeletonGapCells = 1

	// SkeletonIndent is the default left indent. Two cells matches every other
	// inline dock in the conversation thread, so a skeleton never reads as
	// misaligned chrome.
	SkeletonIndent = "  "
)

// Skeleton is the transient pre-execution indicator. It is a value-shaped
// widget designed to be held by pointer and mutated in place from the view's
// render/lifecycle seams; every mutator is nil-safe.
//
// The zero value renders "" — an unmounted skeleton is inert, which is what
// makes "forgot to unmount" fail visibly (nothing rendered) rather than
// silently (a stuck spinner).
type Skeleton struct {
	text   string
	target string
	frame  uint64
	width  int
	indent string
}

// NewSkeleton builds a mounted skeleton for the given indicator line and its
// subject. text is the full canonical indicator (including any "@file" the
// lifecycle state folded in); target is that subject on its own, kept so the
// width budget can elide the file name before it elides the sentence around
// it. A zero width means "unconstrained" until SetWidth is called.
func NewSkeleton(text, target string) *Skeleton {
	return &Skeleton{
		text:   text,
		target: target,
		indent: SkeletonIndent,
	}
}

// Set replaces the indicator text and its subject.
func (s *Skeleton) Set(text, target string) {
	if s == nil {
		return
	}
	s.text = text
	s.target = target
}

// SetText replaces only the indicator text, keeping the current subject.
func (s *Skeleton) SetText(text string) {
	if s == nil {
		return
	}
	s.text = text
}

// SetTarget replaces only the subject, keeping the current indicator text.
func (s *Skeleton) SetTarget(target string) {
	if s == nil {
		return
	}
	s.target = target
}

// SetFrame advances the animation frame. The counter is the SHARED master frame
// of the host (m.frame, advanced by the Bubble Tea FrameTickMsg at ~30 FPS), not
// a private clock, so the glyph and the colour wave advance in lockstep and the
// line never shows a moving glyph over a frozen gradient.
//
// The frame is the widget's ONLY clock: it is a pure function of the text, the
// subject and this counter, so advancing it is sufficient — and necessary — to
// keep the row alive. A surface that stopped receiving frames would render
// byte-identical output forever, which is what a user reads as a hang.
func (s *Skeleton) SetFrame(frame uint64) {
	if s == nil {
		return
	}
	s.frame = frame
}

// SetWidth sets the terminal width budget in cells. A non-positive width means
// "unconstrained", which is the correct reading of a pre-bootstrap frame where
// WindowSizeMsg has not arrived yet.
func (s *Skeleton) SetWidth(width int) {
	if s == nil {
		return
	}
	s.width = width
}

// SetIndent overrides the left indent. An empty string selects the default
// (SkeletonIndent) rather than rendering flush left, so a caller that clears the
// field gets the standard inline alignment instead of a row that jumps to
// column zero.
func (s *Skeleton) SetIndent(indent string) {
	if s == nil {
		return
	}
	s.indent = indent
}

// Text returns the indicator text.
func (s *Skeleton) Text() string {
	if s == nil {
		return ""
	}
	return s.text
}

// Target returns the subject (the file name or tool target) the width budget
// elides first.
func (s *Skeleton) Target() string {
	if s == nil {
		return ""
	}
	return s.target
}

// Frame returns the current animation frame.
func (s *Skeleton) Frame() uint64 {
	if s == nil {
		return 0
	}
	return s.frame
}

// Width returns the current width budget in cells (0 = unconstrained).
func (s *Skeleton) Width() int {
	if s == nil {
		return 0
	}
	return s.width
}

// Active reports whether the skeleton has anything to render. An unmounted or
// empty skeleton is inactive.
func (s *Skeleton) Active() bool {
	if s == nil {
		return false
	}
	return strings.TrimSpace(s.text) != ""
}

// Height is the widget's viewport footprint in physical rows. It is a constant
// 1 — this is the single-line invariant expressed as code rather than as a
// comment, so a caller can budget from it without measuring, and a future
// multi-line regression fails here rather than on a user's screen.
func (s *Skeleton) Height() int { return 1 }

// WidthOf is the display width the skeleton occupies at the current settings,
// in cells. It is measured from the rendered row itself, so it can never
// disagree with View — a caller budgeting a one-line layout against WidthOf is
// budgeting against exactly what will be drawn.
func (s *Skeleton) WidthOf() int {
	if s == nil {
		return 0
	}
	return animation.VisibleWidth(s.View())
}

// effectiveIndent is the configured indent, or the shared default when the
// caller never set one or explicitly cleared it (an empty string selects the
// default, so a cleared field cannot produce a row flush against column zero).
func (s *Skeleton) effectiveIndent() string {
	if s == nil || s.indent == "" {
		return SkeletonIndent
	}
	return s.indent
}

// rung is one level of the composition ladder: a prefix plus the number of cells
// that prefix itself consumes. The ladder is ordered richest-first, so the
// narrowest terminal is served by dropping the least informative element.
type rung struct {
	prefix string
	lead   int
}

// View renders the current frame: one line, never a trailing newline.
//
// The composition is:
//
//	<indent><glyph> <shimmered text>
//
// The glyph is coloured by the shared emerald ramp (SpinnerGlyph) and the text
// by the emerald sine wave (animation.Render), so the two halves share a hue
// family and read as one indicator rather than as a spinner bolted onto a
// caption.
//
// WIDTH LADDER. A terminal can be narrower than the full composition, and a row
// that overflows WRAPS — which is two physical rows, which is exactly the
// failure the single-line invariant exists to prevent. So the row is composed
// from a ladder, richest rung first, and the first rung that both fits the
// width budget and can carry at least one cell of the indicator is used:
//
//  1. <indent><glyph> <text>   — the normal inline row
//  2. <glyph> <text>           — the indent is dropped
//  3. <glyph>                  — the spinner alone, still one line
//
// Each rung is safe: the row never exceeds the budget and never gains a line,
// so the eventual atomic replacement of this row by the finalized content
// changes the row COUNT by zero in every case.
//
// Returns "" when inactive, and ALWAYS returns a string with no '\n' — the
// widget is a physical row, and a newline in a row is a layout bug the caller
// would have to discover.
func (s *Skeleton) View() string {
	if s == nil || !s.Active() {
		return ""
	}
	glyph := SpinnerGlyph(s.frame)
	indent := s.effectiveIndent()
	indentCells := animation.VisibleWidth(indent)

	ladders := []rung{
		{indent + glyph + " ", indentCells + SkeletonGlyphCells + SkeletonGapCells},
		{glyph + " ", SkeletonGlyphCells + SkeletonGapCells},
		{glyph, SkeletonGlyphCells},
	}

	budget := s.width
	// bare is the richest spinner-only row that fits, used when no rung can
	// spare a cell for the indicator.
	var bare string
	haveBare := false
	for _, r := range ladders {
		if budget > 0 && r.lead > budget {
			continue
		}
		cells := -1
		if budget > 0 {
			cells = budget - r.lead
		}
		body := clampBody(s.text, s.target, cells)
		line := r.prefix
		if body != "" {
			line += animation.Render(body, int(s.frame))
		}
		if budget > 0 && animation.VisibleWidth(line) > budget {
			continue
		}
		if body != "" {
			return sanitizeRow(line)
		}
		if !haveBare {
			bare, haveBare = line, true
		}
	}
	if haveBare {
		return sanitizeRow(bare)
	}
	return ""
}

// sanitizeRow is the boundary enforcement of the one-row invariant. Flatten
// inside the renderer already removed control characters from the body, and the
// prefix is built from a glyph and a caller-supplied indent, so this should
// never fire — it exists so the invariant is enforced where the row is actually
// produced rather than only inside a helper, and so a future edit to the
// composition cannot silently break it.
func sanitizeRow(line string) string {
	return strings.ReplaceAll(line, "\n", " ")
}

// RenderSkeleton is the stateless form of View: it renders one frame of a
// skeleton for the given text/subject without constructing (or mutating) a
// widget. The view uses it for a one-shot mount, and tests use it to assert the
// rendering contract directly.
func RenderSkeleton(text, target string, frame uint64, width int) string {
	s := NewSkeleton(text, target)
	s.SetFrame(frame)
	s.SetWidth(width)
	return s.View()
}

// clampBody constrains the indicator text to `cells` display columns.
//
// The SUBJECT IS ELIDED FIRST. A skeleton line's job is to name the pending
// work; a line that has lost its file name to a width clamp ("[mutation]
// Staging edit @…") still says what kind of work is pending, whereas a line
// that has lost its sentence tail to a width clamp ("[mut…") says nothing at
// all. So when the subject is present in the text, the budget is taken out of
// the subject (keeping its tail, which is the identifying part) and the frame
// around it is preserved verbatim. Only a text with no recognisable subject
// falls back to a whole-line tail-preserving elision.
//
// cells < 0 means unconstrained. cells == 0 yields "".
func clampBody(text, target string, cells int) string {
	if text == "" {
		return ""
	}
	flat := string(animation.Flatten(text))
	if cells < 0 || animation.VisibleWidth(flat) <= cells {
		return flat
	}
	if target != "" {
		if idx := strings.Index(flat, target); idx >= 0 {
			head := flat[:idx]
			tail := flat[idx+len(target):]
			avail := cells - animation.VisibleWidth(head) - animation.VisibleWidth(tail)
			if avail >= 1 {
				return head + animation.ClampCells(target, avail) + tail
			}
		}
	}
	return animation.ClampCells(flat, cells)
}
