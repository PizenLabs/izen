package components

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── Strict Layout Bounding & Word Wrapping (framed surfaces) ──────────────────
//
// Lipgloss only PADS to a declared Width; it never guarantees the rendered box
// fits the terminal. A bordered card sized with `Width(w)` renders at
// `w + 2` cells (the border is applied after the content box), so any long
// unbreakable payload — an OpenRouter 403 JSON body, a `[PARTIAL]` truncation
// notice, a multi-kilobyte Go build error — silently pushes the right border
// past the viewport edge, wraps the terminal, and corrupts the frame.
//
// The framed surfaces that remain in the TUI (approval gates, permission and
// decomposition dialogs, thought/system log boxes, the Ask card, code fences and
// tables) therefore go through this module, which owns three guarantees:
//
//  1. BOUNDED: the outer width (border + padding included) never exceeds the
//     reported viewport width minus a safety margin.
//  2. WRAPPED: the wrap contract is stated explicitly at every call site via
//     Bounded.Wrap(true).
//  3. UNBREAKABLE-SAFE: the body is pre-wrapped with a word wrapper followed by
//     a hard wrapper, so a single 4000-cell token (URL, JSON blob, stack frame)
//     is split at the cell boundary instead of blowing past the frame.
//
// SYSTEM NOTICES, ERRORS AND STATUS BANNERS NO LONGER USE THIS FRAME MODEL.
// They are frameless (see RenderMinimalNotice below): with no right border there
// is no border to clip, and WrapBody still carries the unbreakable-safe pass. The
// bounding arithmetic here is retained for the boxes that remain.
//
// WRAP AND LIPGLOSS VERSIONS: lipgloss v1 wraps unconditionally whenever
// Width > 0 and exposes no Wrap(bool) setter, so the wrap intent is carried by
// the Bounded wrapper below and enforced structurally by Width being set. The
// Bounded type is the single seam that flips to a real opt-in when the
// dependency moves to lipgloss v2 (where wrapping becomes opt-in and
// style.Width(...).Wrap(true) is the real API); no call site changes.

const (
	// ViewportMargin is the safety gutter reserved on each side of the pane — one
	// cell per side, so a bounded frame is drawn at paneWidth-2 and can never
	// consume the pane's last drawable column (where a vertical scrollbar, a
	// tmux pane divider, or a hairline of rounding slack lives).
	//
	// It is 2 rather than a larger number because of an arithmetic constraint
	// the frame model imposes. The document's own wrap width is paneWidth-4, so
	// a frame drawn wider than that is re-wrapped by the document builder and
	// loses its right border there — the exact corruption this package exists to
	// prevent, one layer up. A style's horizontal padding is over-reserved out of
	// the budget (see usableWidth), so the drawn width of a standard padded card
	// is paneWidth-ViewportMargin-padding. Requiring that to stay within
	// paneWidth-4 gives ViewportMargin+padding >= 4, and every card in the tree
	// carries Padding(0,1) — a padding of 2 — which makes 2 the exact bound. Any
	// larger margin buys dead gutter; any smaller one tears the frame.
	ViewportMargin = 2

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
//	outer = Width + GetHorizontalBorderSize() + GetHorizontalMargins()
//
// with padding living INSIDE the declared Width. So the budget has to be reduced
// by every cell the frame adds on the OUTSIDE of that Width:
//
//	usableWidth = paneWidth - style.GetHorizontalFrameSize() - 2
//
// where the trailing 2 is the ViewportMargin gutter either side of the pane and
// GetHorizontalFrameSize() is the sum of margins, padding, and border.
//
// GetHorizontalFrameSize() over-reserves by exactly the horizontal padding. That
// is deliberate and safe: the alternative is a per-component list of which parts
// of the frame are "outside", and any style that grows a margin — which is the
// change most likely to be made later, and the one least likely to be re-audited
// against this function — silently pushes the right border off-screen.
// Over-reserving the padding costs two cells of gutter; under-reserving the
// margin costs the frame.
//
// A style that differs only in frame size must therefore get a DIFFERENT usable
// width. Returning one constant for every style is the bug this function exists
// to prevent: with a Margin(0,1) card the rendered box came out ViewportMargin-2
// cells too wide, which is the one-cell right-border clip on [PARTIAL] cards.
func usableWidth(style lipgloss.Style, viewportWidth int) int {
	frame := style.GetHorizontalFrameSize()
	if frame < 0 {
		frame = 0
	}
	return ContentWidth(viewportWidth) - frame
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

// ── Frameless Muted Notice System ─────────────────────────────────────────────
//
// System notices, errors, and status banners are FRAMELESS. They carry no
// full enclosure, no corners, and no right or bottom border. A notice is a
// compact prefix badge, an optional soft left-accent line, and word-wrapped
// muted body text.
//
// Removing the frame is not cosmetic: with no right border there is no border
// to push off-screen, so frame clipping becomes physically impossible. A long
// provider error can only WRAP, never tear a box around itself. The bounding
// arithmetic above still governs the boxes that remain (approval gates, code
// fences, tables); notices simply stopped being boxes.

// NoticeLevel selects the semantic accent of a frameless notice.
type NoticeLevel int

const (
	// NoticeInfo is an advisory/clarification surface.
	NoticeInfo NoticeLevel = iota
	// NoticeStatus is a neutral progress/state surface.
	NoticeStatus
	// NoticeSuccess is a completed/positive surface.
	NoticeSuccess
	// NoticeWarning is a recoverable warning or partial-result surface.
	NoticeWarning
	// NoticeError is a failure surface (provider error, build failure).
	NoticeError
)

var (
	// noticeAccent is the badge + accent-line colour per level. Every hue is a
	// muted Catppuccin Mocha tone rather than a saturated signal colour, so a
	// notice reads as recessed system chrome and never competes with the answer.
	noticeAccent = map[NoticeLevel]lipgloss.Color{
		NoticeInfo:    "#89b4fa",
		NoticeStatus:  "#89b4fa",
		NoticeSuccess: "#a6e3a1",
		NoticeWarning: "#f9e2af",
		NoticeError:   "#f38ba8",
	}

	// noticeIcon is the leading glyph the badge is prefixed with.
	noticeIcon = map[NoticeLevel]string{
		NoticeInfo:    "ℹ",
		NoticeStatus:  "●",
		NoticeSuccess: "✔",
		NoticeWarning: "▲",
		NoticeError:   "✖",
	}

	// noticeCaption is the fallback badge caption when a caller supplies neither
	// a prefix nor a message that already begins with a `[TAG]` marker.
	noticeCaption = map[NoticeLevel]string{
		NoticeInfo:    "[INFO]",
		NoticeStatus:  "[STATUS]",
		NoticeSuccess: "[OK]",
		NoticeWarning: "[WARNING]",
		NoticeError:   "[ERROR]",
	}

	// noticeBodyStyle is the dimmed, faint body text of every notice. It is
	// intentionally low-contrast: the badge carries the signal, the body is
	// reference material the developer reads only on demand.
	noticeBodyStyle = lipgloss.NewStyle().
			Faint(true).
			Foreground(lipgloss.Color("#6c7086"))
)

// leadingNoticeTag matches a leading `[TAG]` marker (`[PARTIAL]`, `[ERROR]`)
// so it can be promoted into the badge rather than repeated in the body. The
// tag must start with a letter, so decorator markers like `[!]` are left
// verbatim in the body instead of becoming a cryptic badge.
var leadingNoticeTag = regexp.MustCompile(`^\[([A-Za-z][A-Za-z0-9 _-]*)\]\s*`)

// noticeBadge resolves the badge and body for a notice.
//
// Precedence: an explicit prefix wins; otherwise a leading `[TAG]` in the
// message is promoted to the badge and stripped from the body; otherwise the
// level's default caption is used. The level icon is prepended exactly once, so
// a caller may pass either `"[PARTIAL]"` or `"▲ [PARTIAL]"` without doubling it.
func noticeBadge(prefix, message string, level NoticeLevel) (badge, body string) {
	badge = strings.TrimSpace(prefix)
	body = strings.TrimSpace(message)
	if badge == "" {
		if m := leadingNoticeTag.FindStringSubmatch(body); m != nil {
			badge = "[" + strings.ToUpper(m[1]) + "]"
			body = strings.TrimSpace(body[len(m[0]):])
		}
	}
	if badge == "" {
		badge = noticeCaption[level]
	}
	if icon := noticeIcon[level]; icon != "" && !strings.HasPrefix(badge, icon) {
		badge = icon + " " + badge
	}
	return badge, body
}

// RenderMinimalNotice renders the canonical frameless notice: a compact
// coloured prefix badge, then an optional soft left-accent line (`│ `) carrying
// the muted, faint, word-wrapped body.
//
// Wrapping is delegated to WrapBody — the same ANSI-aware word pass followed by
// a hard pass the bounded boxes use — against `paneWidth - 4`. The two cells
// the accent line occupies are then added back on top, so the widest physical
// row is `paneWidth - 2` and the notice can never reach, let alone exceed, the
// pane's last drawable column. Because there is no right border, even a
// pathological payload (a 4-KiB unbreakable token) can only wrap.
//
// An empty body renders a badge-only notice. An empty prefix derives the badge
// from a leading `[TAG]` in the message, or the level caption.
func RenderMinimalNotice(prefix, message string, level NoticeLevel, paneWidth int) string {
	if _, ok := noticeAccent[level]; !ok {
		level = NoticeInfo
	}
	accent := noticeAccent[level]
	badge, body := noticeBadge(prefix, message, level)

	badgeStyle := lipgloss.NewStyle().Bold(true).Foreground(accent)
	bar := lipgloss.NewStyle().Foreground(accent).Render("│")

	limit := paneWidth
	if limit < MinBoundWidth {
		limit = MinBoundWidth
	}
	bodyWidth := limit - 4
	if bodyWidth < 1 {
		bodyWidth = 1
	}

	var b strings.Builder
	b.WriteString(badgeStyle.Render(badge))
	if body == "" {
		return b.String()
	}
	for _, line := range strings.Split(WrapBody(body, bodyWidth), "\n") {
		b.WriteString("\n")
		if line == "" {
			b.WriteString(bar)
			continue
		}
		b.WriteString(bar + " " + noticeBodyStyle.Render(line))
	}
	return b.String()
}

// ── Banner Constructors (frameless) ──────────────────────────────────────────
//
// All four share one shape: a prefix badge, then the detail body as a soft
// left-accent block. They differ only in accent colour and default caption, so
// a status/error/policy/info surface can never drift into a different bounding
// discipline. None of them draws a border.

// BannerKind selects the semantic accent of a status banner. It is retained as
// the public compatibility surface for call sites that predate NoticeLevel.
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

// bannerLevel maps the legacy BannerKind onto the notice level scale.
func bannerLevel(kind BannerKind) NoticeLevel {
	switch kind {
	case BannerError:
		return NoticeError
	case BannerPolicy:
		return NoticeWarning
	case BannerInfo:
		return NoticeInfo
	default:
		return NoticeStatus
	}
}

// Banner renders one frameless notice. The detail body is preserved verbatim
// (raw provider text is never replaced with a generic message) but is
// guaranteed to word-wrap inside the pane.
//
// An empty detail renders a badge-only notice; an empty label falls back to the
// kind's default caption so the badge is never blank.
func Banner(kind BannerKind, label, detail string, viewportWidth int) string {
	level := bannerLevel(kind)
	caption := strings.TrimSpace(label)
	if caption == "" {
		caption = defaultBannerCaption(kind)
	}
	return RenderMinimalNotice(caption, detail, level, viewportWidth)
}

// ErrorBanner renders a frameless error notice from a raw error string. The
// message is preserved verbatim — an OpenRouter 403 body or a `[PARTIAL]`
// truncation notice is shown exactly as the provider reported it, only
// re-flowed.
func ErrorBanner(message string, viewportWidth int) string {
	return RenderMinimalNotice("[ERROR]", message, NoticeError, viewportWidth)
}

// StatusBanner renders a frameless status/state notice.
func StatusBanner(label, detail string, viewportWidth int) string {
	return Banner(BannerStatus, label, detail, viewportWidth)
}

// PolicyBanner renders a frameless policy/permission-gate notice.
func PolicyBanner(label, detail string, viewportWidth int) string {
	return Banner(BannerPolicy, label, detail, viewportWidth)
}

// InfoBanner renders a frameless advisory/clarification notice.
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
