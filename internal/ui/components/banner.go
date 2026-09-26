package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── Strict Layout Bounding & Word Wrapping (status / error / policy banners) ──
//
// Lipgloss only PADS to a declared Width; it never guarantees the rendered box
// fits the terminal. A bordered card sized with `Width(w)` renders at
// `w + 2` cells (the border is applied after the content box), so any long
// unbreakable payload — an OpenRouter 403 JSON body, a `[PARTIAL]` truncation
// notice, a multi-kilobyte Go build error — silently pushes the right border
// past the viewport edge, wraps the terminal, and corrupts the frame.
//
// Every status surface in the TUI therefore goes through this module, which
// owns three guarantees:
//
//  1. BOUNDED: the outer width (border + padding included) never exceeds the
//     reported viewport width minus a safety margin.
//  2. WRAPPED: the wrap contract is stated explicitly at every call site via
//     Bounded.Wrap(true).
//  3. UNBREAKABLE-SAFE: the body is pre-wrapped with a word wrapper followed by
//     a hard wrapper, so a single 4000-cell token (URL, JSON blob, stack frame)
//     is split at the cell boundary instead of blowing past the frame.
//
// WRAP AND LIPGLOSS VERSIONS: lipgloss v1 wraps unconditionally whenever
// Width > 0 and exposes no Wrap(bool) setter, so the wrap intent is carried by
// the Bounded wrapper below and enforced structurally by Width being set. The
// Bounded type is the single seam that flips to a real opt-in when the
// dependency moves to lipgloss v2 (where wrapping becomes opt-in and
// style.Width(...).Wrap(true) is the real API); no call site changes.

const (
	// ViewportMargin is the safety gutter reserved on each side of the
	// viewport: the scrollbar column, the document gutter, and the rounding
	// slack between the declared terminal width and the drawable area.
	ViewportMargin = 4

	// BorderCells is the horizontal space a left+right border consumes.
	BorderCells = 2

	// MinBoundWidth is the narrowest content width a banner is allowed to
	// shrink to. Below this a banner is useless, so it degrades to this floor
	// rather than collapsing; the frame may then scroll horizontally, which is
	// strictly better than emitting a corrupted border.
	MinBoundWidth = 16
)

// Bounded is a lipgloss.Style carrying an explicit word-wrap contract.
type Bounded struct {
	lipgloss.Style
}

// Width constrains the style to i cells of CONTENT (border excluded) and keeps
// the Bounded contract. It shadows the promoted lipgloss method so the
// Width(...).Wrap(true) chain type-checks.
func (b Bounded) Width(i int) Bounded { return Bounded{Style: b.Style.Width(i)} }

// Wrap states that the style's content must be word-wrapped to its width.
// Under lipgloss v1 this is structurally guaranteed by a non-zero Width; the
// method exists so the contract is explicit, greppable, and preserved verbatim
// across the v2 migration.
func (b Bounded) Wrap(bool) Bounded { return b }

// ContentWidth returns the safe content width for a banner inside a viewport of
// viewportWidth cells: the viewport minus the safety margin, floored at
// MinBoundWidth so a narrow terminal never produces a zero/negative width.
func ContentWidth(viewportWidth int) int {
	w := viewportWidth - ViewportMargin
	if w < MinBoundWidth {
		return MinBoundWidth
	}
	return w
}

// usableWidth is the CONTENT-BOX width a framed style of this shape may be given
// so that its rendered OUTER width stays within ContentWidth(viewportWidth).
//
// The arithmetic is the whole point of this module, so it is derived rather than
// hardcoded. Lipgloss lays a frame out as:
//
//	outer = Width + GetHorizontalBorderSize()
//
// with padding and margins living INSIDE the declared Width. So to land the
// outer edge on ContentWidth, Width must be the target minus the style's own
// border size — read off the style, not assumed to be 2. A style with a margin,
// with a single side of the border switched off, or with a doubled border all
// carry a different horizontal border size, and a hardcoded 2 would push the
// right border off-screen for exactly those frames.
func usableWidth(style lipgloss.Style, viewportWidth int) int {
	border := style.GetHorizontalBorderSize()
	if border < 0 {
		border = 0
	}
	return ContentWidth(viewportWidth) - border
}

// Bound constrains style to the safe content width of a viewport of
// viewportWidth cells and declares the word-wrap contract.
//
// The returned style's rendered OUTER width (border included) equals
// ContentWidth(viewportWidth): lipgloss treats Width as the content box and
// adds the border on top, so the style's own horizontal border size is
// subtracted before applying it. See usableWidth.
func Bound(style lipgloss.Style, viewportWidth int) Bounded {
	return Bounded{Style: style}.
		Width(max(usableWidth(style, viewportWidth), MinBoundWidth-BorderCells)).
		Wrap(true)
}

// BoundBox is Bound for a bordered style. OuterWidth reports the drawn width so
// callers can size separator rules to match the frame exactly.
func BoundBox(style lipgloss.Style, viewportWidth int) Bounded {
	return Bound(style, viewportWidth)
}

// OuterWidth returns the total number of terminal cells a Bound-rendered string
// occupies, border included. It is the target Bound aims the outer edge at, and
// the budget every other bounded surface (inner rules, wrapped bodies) is
// measured against.
func OuterWidth(viewportWidth int) int {
	return ContentWidth(viewportWidth)
}

// InnerWidth is the content width available INSIDE a frame drawn with Bound:
// the outer width, minus the frame's own border and horizontal padding. Any
// wrapped body or horizontal rule placed inside such a card must be measured
// against this, or it outgrows the frame and tears the right border off.
//
// It reads the border size off the style rather than assuming a full border, so
// a half-bordered or borderless box is measured correctly instead of being given
// two cells of budget it cannot spend.
func InnerWidth(viewportWidth int, style lipgloss.Style) int {
	inner := OuterWidth(viewportWidth) -
		style.GetHorizontalBorderSize() -
		style.GetHorizontalPadding()
	if inner < MinBoundWidth {
		return MinBoundWidth
	}
	return inner
}

// BodyBreakpoints are the characters a banner body may break AFTER, in addition
// to whitespace. They are the natural separators of the payloads these banners
// carry: URLs, JSON error bodies, file:line coordinates, and package paths.
//
// Without them the word pass has no break opportunity inside
// `{"message":"This model's maximum context length is 8192 tokens…"}` and the
// hard pass has to guillotine the JSON mid-token, which mangles the one thing
// the user is reading it for. With them, the break lands on a separator and the
// payload stays copy-pasteable.
//
// The set covers the separators the payloads actually use — `/ : ; , . _ = & ?`
// — plus `+ @ #` so an email address, a URL query string, and a `file:line:col`
// coordinate all break cleanly rather than being cut mid-token.
const BodyBreakpoints = " \t/:;,._=&?+@#"

// WrapBody pre-wraps a banner body to width cells. It word-wraps first (so
// prose breaks at spaces and the body never gains or loses a line) and then
// hard-wraps every resulting line (so a single genuinely unbreakable token — a
// 4000-cell base64 blob, a long hash — is cut at the cell boundary rather than
// blowing past the frame). Both passes are ANSI-aware, so escape sequences are
// never split and cell widths (not byte lengths) are measured.
func WrapBody(body string, width int) string {
	if body == "" {
		return ""
	}
	if width < 1 {
		width = 1
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	word := ansi.Wordwrap(body, width, BodyBreakpoints)
	return ansi.Hardwrap(word, width, true)
}

// MaxLineWidth returns the widest visual cell count across the lines of s, ANSI
// aware. Tests and narrow-width fallbacks use it to assert a frame is bounded.
func MaxLineWidth(s string) int {
	widest := 0
	for _, line := range strings.Split(s, "\n") {
		if w := ansi.StringWidth(line); w > widest {
			widest = w
		}
	}
	return widest
}

// ── Banner Constructors ────────────────────────────────────────────────────────
//
// All four share one shape: an icon+label header line, then the detail body
// word-wrapped inside a bounded, wrapping box. They differ only in border
// colour and label colour, so a status/error/policy/info surface can never drift
// into a different bounding discipline.

// BannerKind selects the semantic accent of a status banner.
type BannerKind int

const (
	// BannerError is a failure surface (provider error, build failure).
	BannerError BannerKind = iota
	// BannerStatus is a neutral progress/state surface.
	BannerStatus
	// BannerPolicy is a gate/permission/policy-rejection surface.
	BannerPolicy
	// BannerInfo is an advisory/clarification surface.
	BannerInfo
)

var (
	// bannerBody is the dimmed detail text inside every banner.
	bannerBody = lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))

	// bannerAccent is the label + border colour per kind.
	bannerAccent = map[BannerKind]lipgloss.Color{
		BannerError:  "#f38ba8",
		BannerStatus: "#89b4fa",
		BannerPolicy: "#fab387",
		BannerInfo:   "#a6e3a1",
	}

	// bannerIcon is the leading glyph per kind.
	bannerIcon = map[BannerKind]string{
		BannerError:  "✗",
		BannerStatus: "●",
		BannerPolicy: "◈",
		BannerInfo:   "ℹ",
	}
)

// Banner renders one bounded status card. The detail body is preserved
// verbatim (raw provider text is never replaced with a generic message) but is
// guaranteed to break inside the viewport.
//
// An empty detail renders a label-only card; an empty label falls back to the
// kind's default caption so the frame is never blank.
func Banner(kind BannerKind, label, detail string, viewportWidth int) string {
	accent, ok := bannerAccent[kind]
	if !ok {
		kind, accent = BannerStatus, bannerAccent[BannerStatus]
	}
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(accent)

	header := strings.TrimSpace(label)
	if header == "" {
		header = defaultBannerCaption(kind)
	}
	header = strings.TrimSpace(bannerIcon[kind] + " " + header)

	// Chrome consumed by the frame itself: its border and horizontal padding.
	// The body wraps to whatever is left, so a long message breaks exactly at
	// the inner edge and the right border stays intact. The width is read off
	// the same style Bound will render with, so the two can never disagree.
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1)
	inner := InnerWidth(viewportWidth, frame)

	var b strings.Builder
	b.WriteString(labelStyle.Render(header))
	if detail = strings.TrimSpace(detail); detail != "" {
		b.WriteString("\n")
		b.WriteString(WrapBody(bannerBody.Render(detail), inner))
	}

	return Bound(frame, viewportWidth).Render(b.String())
}

// ErrorBanner renders a bounded error card from a raw error string. The message
// is preserved verbatim — an OpenRouter 403 body or a `[PARTIAL]` truncation
// notice is shown exactly as the provider reported it, only re-flowed.
func ErrorBanner(message string, viewportWidth int) string {
	return Banner(BannerError, "Error", message, viewportWidth)
}

// StatusBanner renders a bounded status/state card.
func StatusBanner(label, detail string, viewportWidth int) string {
	return Banner(BannerStatus, label, detail, viewportWidth)
}

// PolicyBanner renders a bounded policy/permission-gate card.
func PolicyBanner(label, detail string, viewportWidth int) string {
	return Banner(BannerPolicy, label, detail, viewportWidth)
}

// InfoBanner renders a bounded advisory/clarification card.
func InfoBanner(label, detail string, viewportWidth int) string {
	return Banner(BannerInfo, label, detail, viewportWidth)
}

func defaultBannerCaption(kind BannerKind) string {
	switch kind {
	case BannerError:
		return "Error"
	case BannerPolicy:
		return "Policy"
	case BannerInfo:
		return "Info"
	default:
		return "Status"
	}
}
