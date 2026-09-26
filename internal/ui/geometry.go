package ui

import (
	"strings"
)

// ViewportGeometry is the single authoritative layout model for the scrollable
// conversation viewport. All coordinate transformations (rendering, mouse mapping,
// selection highlighting, viewport sizing) must derive from this source so they
// cannot drift.
//
// Origin: terminal cell (0,0) is the top-left of the entire screen.
// Top/Left are absolute terminal coordinates of the viewport rectangle.
// Width/Height are the viewport's dimensions in cells.
type ViewportGeometry struct {
	Top    int // absolute Y of the first viewport row (header height)
	Left   int // absolute X of the viewport (always 0 - viewport fills width)
	Width  int
	Height int
}

// viewportGeometry returns the authoritative geometry used by both the renderer
// (assembleScreen / View) and the mouse-to-logical coordinate mapper.
//
// It renders the same regions assembleScreen renders, normalises them the same
// way, and hands them to the same ViewportHeight arithmetic, so the two can never
// disagree. That matters most for the mouse mapper: a click is turned into a
// record index using this rectangle, and a rectangle that is one row taller than
// the drawn viewport maps every click in the last row to the wrong record.
//
// The proposal dock is MEASURED rather than estimated. It used to be sized by a
// hand-maintained per-branch line count that reserved 10 rows for a dock that
// actually drew 14, so the frame was four rows taller than the terminal and the
// terminal scrolled. The dock only renders in the two states that show it, so
// the extra work is confined to those states.
func (m *model) viewportGeometry() ViewportGeometry {
	width := max(m.PaneWidth(), minViewportWidth)

	borderStyle := m.modeStyle(m.resolver.Current())
	if m.inViMode {
		borderStyle = viBorderStyle
	}

	headerView := normalizeRegion(m.renderTopBar(width))
	footerView := normalizeRegion(m.renderFixedFooter(width, nil))
	inputView := normalizeRegion(m.renderInputRegion(width, borderStyle))

	var proposalView string
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		proposalView = normalizeRegion(m.renderProposalBlock())
	}
	proposalView = capProposalDock(proposalView, m.Screen().Height,
		regionHeight(headerView), regionHeight(inputView), regionHeight(footerView))

	return m.measureViewportGeometry(headerView, proposalView, inputView, footerView)
}

// viewportContentPrefixHeight returns the number of physical lines at the top
// of the viewport's SetContent that are NOT records (banner, context header,
// workspace mode header). The physical row of the first record inside the
// viewport content is this value. It is used to translate a viewport
// YOffset-relative physical row into a record-relative row.
// It mirrors the prefix construction in refreshViewportContent exactly.
func (m *model) viewportContentPrefixHeight() int {
	var prefix strings.Builder
	if m.showBanner && len(m.records) == 0 {
		b := m.renderStartupBanner(m.PaneWidth())
		if b != "" {
			prefix.WriteString(b)
			prefix.WriteString("\n")
		}
	}
	if m.resumeBriefing != nil {
		if b := m.renderResumeBriefing(); b != "" {
			prefix.WriteString(b)
			prefix.WriteString("\n")
		}
	}
	ctx := m.renderContextHeader()
	if ctx != "" {
		prefix.WriteString(ctx)
	}
	if !m.showBanner || len(m.records) > 0 {
		prefix.WriteString(m.renderWorkspaceHeader())
	}
	if prefix.Len() == 0 {
		return 0
	}
	return countLines(prefix.String())
}
