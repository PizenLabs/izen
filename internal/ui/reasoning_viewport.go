package ui

// ── BOUNDED REASONING PREVIEW WINDOW ─────────────────────────────────────────
//
// # THE BUDGET, IN ONE LINE
//
//	H_reasoning = 0                                          if disabled or folded
//	H_reasoning = min(contentLines, ⌊0.25 × H_pane⌋, 6)      when active
//
// Every term is a clamp, and the clamps are not decoration — each one removes a
// specific way this band used to eat the frame:
//
//   - contentLines — a three-line trace does not get a six-row band. A panel
//     padded to its ceiling is four rows of blank gutter, and blank gutter is
//     rows taken from the answer for nothing.
//   - ⌊0.25 × H_pane⌋ — the panel is a DRAWER on the conversation, not a second
//     conversation. A quarter is the largest share that still leaves the answer
//     readable in every pane the terminal can be resized to. Forty percent was
//     enough to make the reasoning the main event.
//   - 6 — the absolute ceiling. Two lines are not enough to read a step, and
//     more than six is not a preview, it is a pane. The window is scrollable;
//     the ceiling is what keeps the band from becoming one.
//
// # WHY THE CEILING IS THE INTERESTING TERM
//
// A bounded window is only useful if it is a WINDOW: the content keeps its
// natural height and the viewport follows the tail. Capping the height and
// then parking the offset at the top would show the reader the first six lines
// of a three-hundred-line trace and call it a preview. The tail anchor is
// therefore part of the same contract as the ceiling, not a nicety bolted on
// afterwards — see reanchorReasoningWindow and the re-clamp in
// applyReasoningSplit.
//
// # WHAT 0 MEANS
//
// Zero is the answer for three different states — CoT disabled in settings,
// folded by Ctrl+O, and "no reasoning behind the band" — and they are all the
// same thing to the compositor: the band does not exist. A released band gives
// its rows back to the conversation in the same frame, because the budget is
// recomputed synchronously on the toggle rather than on the next layout pass
// (see recomputeLayout). A panel that unmounted a frame late would hold the
// wheel lock and spend rows on nothing in the meantime.

const (
	// reasoningPanelPercent is the share of the scrollable area the band may
	// take. See the header comment for why the panel is subordinate.
	reasoningPanelPercent = 25
	// reasoningPanelMaxRows is the hard ceiling. Six rows is a preview; more
	// is a pane, and a pane is what the conversation is for.
	reasoningPanelMaxRows = 6
	// reasoningPanelReserveRows is the conversation's protected share.
	reasoningPanelReserveRows = 8
	// reasoningPanelMinAvailable is the smallest scrollable area that can be
	// split at all. It equals the reserve: below it the conversation's own
	// share cannot survive, and a one-row band is not a preview either.
	reasoningPanelMinAvailable = reasoningPanelReserveRows
)

// reasoningPanelHeight returns the row budget for the reasoning band.
//
// available is the number of rows the scrollable area may use at all — the pane
// minus the header, the dock, the prompt bar and the footer. Using it rather
// than the raw pane height is deliberate and strictly stronger than the
// quarter-of-the-pane rule: the scrollable area is never larger than the pane,
// so ⌊0.25 × available⌋ is always within ⌊0.25 × H_pane⌋.
//
// contentLines is the number of rows the panel's own content occupies. Zero
// means UNKNOWN rather than empty — the content has not been pushed into the
// viewport yet — and unknown content is bounded by the two ceilings alone. That
// asymmetry is safe in one direction only, which is the direction that matters:
// an unknown count can make the band no LARGER than the ceiling, never so small
// that the tail it is anchored to falls outside the window.
func reasoningPanelHeight(available, contentLines int) int {
	if available < reasoningPanelMinAvailable {
		// A refusal, not an oversight: there is no split in which both bands
		// stay legible, so the conversation is shown alone.
		return 0
	}
	rows := available * reasoningPanelPercent / 100
	if rows > reasoningPanelMaxRows {
		rows = reasoningPanelMaxRows
	}
	if contentLines > 0 && contentLines < rows {
		rows = contentLines
	}
	if rows < 1 {
		// Mounted and asked for rows: a mounted band that renders nothing is a
		// band holding the wheel away from a conversation that has content.
		rows = 1
	}
	// The reserve is applied last, and it is the term that survives. In a pane
	// with room for only a handful of rows, available-reserve goes negative,
	// and a min() applied before the percentage floor would hand the band most
	// of what is left.
	ceiling := available - reasoningPanelReserveRows
	if ceiling < 1 {
		ceiling = 1
	}
	return min(rows, ceiling)
}

// reasoningContentLines reports how many rows the panel's content occupies, or 0
// when that is not yet known.
//
// The answer is read from the viewport rather than re-derived from the source,
// and that is the whole reason this is O(1): the wrapped row count of a
// multi-thousand-line trace costs a full re-wrap, and the layout pass runs once
// per rendered message. The viewport already performed that re-wrap when the
// frame tick pushed the content in, and its total line count is the exact number
// the band will draw — so the budget and the content can never disagree.
//
// The frame tick runs before the layout pass in every path that matters, and the
// toggle path pushes the content synchronously (setReasoningExpanded), so the
// count is populated on the very frame the band mounts. A zero therefore means
// "not synced yet", and reasoningPanelHeight treats it as unbounded-by-content.
func (m *model) reasoningContentLines() int {
	if m == nil || !m.reasoningSynced {
		return 0
	}
	return m.reasoningViewport.TotalLineCount()
}

// recomputeLayout re-derives the whole frame budget synchronously.
//
// # WHAT IT IS FOR
//
// The reasoning band is a participant in an EXACT equation, not a decoration
// drawn on top of a finished frame:
//
//	H_main = H_pane − (H_header + H_reasoning + H_footer + H_prompt)
//
// Every term but one is fixed, so the moment H_reasoning changes — a Ctrl+O,
// an Alt+O, a settings change that hides CoT, a pane resize — H_main has to
// change in the same frame. Leaving that to the next layout pass opens a
// one-frame window in which the viewport is still sized for the old band, and
// that window is visible in both directions:
//
//   - the band GREW and the viewport has not shrunk: the composed frame is one
//     band-height too tall, the bottom stack is pushed off the pane's edge, and
//     the prompt bar lands in scrollback — which is never redrawn, so it is
//     orphaned there for the rest of the session.
//   - the band SHRANK and the viewport has not grown: the frame comes up SHORT,
//     the prompt bar floats above the bottom edge, and stale terminal content
//     shows through underneath it.
//
// Those are the same bug seen from two sides, and both are what "toggling
// reasoning mid-stream duplicates the prompt bar" actually is. So the budget is
// not recomputed "next frame": it is recomputed HERE, on the event, before any
// slice of the frame is generated.
//
// # WHY IT DOES NOT DUPLICATE THE RECOMPUTATION
//
// recomputeLayout re-derives the SAME numbers the render path would
// (measureViewportGeometry → applyReasoningSplit), from the same cached chrome.
// It is a re-entrant call, not a second opinion: the arithmetic lives in
// applyReasoningSplit, and this is the function that guarantees the keypress
// path reaches it before View() does.
//
// The cost is one cached-chrome measurement and a handful of integer writes. It
// is paid on a keypress and on a settings change — both rare, both already
// repainting the frame — and never on a token or a wheel notch, which is the
// only place the per-frame budget matters.
func (m *model) recomputeLayout() {
	if m == nil {
		return
	}
	// A panel with no reasoning behind it has to give its rows back and release
	// the wheel in THIS frame, so the mount reconciliation is answered here
	// rather than deferred to the next frame. It is two integer reads.
	m.reconcileReasoningMount()
	if m.Ready {
		m.Viewport.Height = m.computeVpHeight()
		// RE-CLAMP BOTH WINDOWS. Each offset was chosen against the height this
		// call is about to replace, and an offset past the new bottom renders an
		// EMPTY window rather than the tail:
		//
		//   - the conversation offset. A band that shrinks GROWS the viewport,
		//     so maxAppScroll falls and an offset that was legal one frame ago
		//     is now past the end. The fast path slices the rendered line cache
		//     at that offset, so without the clamp the first frame after the
		//     toggle is a blank band of transcript where the answer was.
		//   - the reasoning window. Symmetrically, a band whose height just
		//     changed may hold an offset chosen against the old height.
		m.clampDocScrollOffset()
		m.Viewport.SetYOffset(m.Viewport.YOffset)
	}
	// The reasoning window is clamped for the same reason. It is deliberately
	// NOT re-anchored to the tail here: a band that is re-mounted must land the
	// reader back on the line they were reading, and the tail anchor belongs to
	// the content sync (syncReasoningContent), which knows whether this is a
	// FIRST mount (anchor the tail), a live stream (follow the tail) or a
	// re-open of unchanged content (keep the reader's offset). A second
	// authority for "where the window should be" would be one more place for the
	// reader's position to be taken away from them.
	m.clampReasoningWindow()
}

// clampDocScrollOffset re-asserts the conversation's document offset against
// the CURRENT scroll bound, in the correct direction.
//
// It is one comparison, and it is on the toggle path for the same reason the
// viewport re-clamp is: the scroll fast path composes the visible window as a
// raw slice of the rendered line cache at m.docScrollOffset, so an offset left
// past the end is not clamped by the renderer — it is DRAWN, as a blank window.
// Only a downward move can leave the offset stranded, and only a shrink of the
// scrollable area can strand it, so the clamp is a min() and nothing else.
func (m *model) clampDocScrollOffset() {
	if m == nil {
		return
	}
	if maxOff := m.maxAppScroll(); m.docScrollOffset > maxOff {
		m.docScrollOffset = maxOff
	}
	if m.docScrollOffset < 0 {
		m.docScrollOffset = 0
	}
}

// clampReasoningWindow re-asserts the reasoning window's offset against its
// CURRENT height.
//
// This is the tail-anchor SAFETY NET, not the tail anchor. The anchor lives in
// syncReasoningContent; what this does is guarantee that a height change cannot
// strand the offset. An offset past the bottom of a viewport renders an EMPTY
// window rather than the reasoning, and a band that claims six rows and draws
// nothing is worse than a band that is not there at all — it still holds the
// wheel away from the conversation.
func (m *model) clampReasoningWindow() {
	if m == nil || !m.reasoningExpanded || m.reasoningPanelRows <= 0 {
		return
	}
	m.reasoningViewport.SetYOffset(m.reasoningViewport.YOffset)
}
