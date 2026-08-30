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
// (assembleScreen / View) and the mouse-to-logical coordinate mapper. It
// mirrors the layout partitioning in assembleScreen/View exactly so there is
// only one source of truth.
func (m *model) viewportGeometry() ViewportGeometry {
	width := m.width
	if width < 40 {
		width = 40
	}

	// Header height: rendered fixed header line count (0 when no runtime ctx).
	headerView := m.renderTopBar(width)
	headerLines := countLines(headerView)

	footerView := m.renderFixedFooter(width, nil)
	footerLines := countLines(footerView)

	// Input + proposal heights use the same helpers as assembleScreen.
	// We must reconstruct the same inputView/proposal heights without
	// duplicating the view strings arbitrarily.
	// Autocomplete dropdown height when active.
	autoH := m.getAutocompleteHeight()
	// The old idle-telemetry status bar row is gone: the prompt bar anchors
	// directly above the single-line lifecycle footer, so no separate status
	// height is reserved here.
	statusH := 0
	// Proposal dock height when present.
	proposalH := 0
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		proposalH = m.getProposalDockCurrentHeight()
	}
	// Input area already includes autocomplete because assembleScreen builds
	// inputView with autocomplete inside and counts it. Our autoH is part of
	// that, but the constant 3 is just the rule+prompt+rule. When autocomplete
	// is active the inputView gains autoH extra lines, which we add here.
	// Similarly proposal is separate.
	//
	// Total fixed height outside the viewport:
	// header + footer + input (3 + autoH) + proposal
	//
	// assembleScreen computes:
	//   totalFixed = headerLines + inputLines + footerLines
	//   where inputLines = countLines(inputView) which already includes autoH
	//   and the footer is the single-line lifecycle bar (no status row).
	// So we replicate that:
	inputLines := 3 + autoH
	// When we are in a narrow mode where header/footer may be empty we already
	// have 0 for those; keep the same formula.
	totalFixed := headerLines + inputLines + statusH + footerLines
	height := m.height - totalFixed - proposalH
	if height < 1 {
		height = 1
	}

	return ViewportGeometry{
		Top:    headerLines + m.viewportPaneTop,
		Left:   m.viewportPaneLeft,
		Width:  m.Viewport.Width,
		Height: height,
	}
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
		b := m.renderStartupBanner(m.width)
		if b != "" {
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
