package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── PROMPT INPUT PATH ISOLATION ──────────────────────────────────────────────
//
// # THE PROBLEM
//
// Typing and streaming compete for one goroutine, and the loop is a single
// thread: a keypress is not painted until every message ahead of it in the queue
// has run its Update AND its View. During a stream that queue is deep — the
// provider produces tokens faster than the frame loop retires them — so the
// latency of a keystroke is the cost of everything it was queued behind, not the
// cost of typing a character.
//
// Before this file, a keystroke sat behind the DOCUMENT. Every key went through
// the full key-routing chain and then, on the very next View(), through the whole
// document composition: the conversation rows, the reasoning panel, the tail
// panels, the proposal dock. None of that changed when a letter was typed, and
// all of it is O(document).
//
// # THE SPLIT
//
// A keystroke changes exactly ONE region of the frame: the prompt bar. So the
// frame is now described by two independent claims about itself.
//
//	promptDirty   the bottom prompt region needs re-composing
//	documentGen   the document region has not changed since generation N
//
// A keypress sets the first and does not touch the second. View() then
// re-composites the prompt region alone and reuses the document bands the last
// full compose produced. That is the entire mechanism, and it is why the key path
// is O(prompt) rather than O(document) — which is what makes the <1ms budget in
// the DoD reachable while a 100 tok/s answer is streaming behind it.
//
// # WHY THE DOCUMENT CLAIM IS A GENERATION AND NOT A FLAG
//
// A flag has to be cleared at every site that mutates the document, and a site
// that forgets is a stale frame that no test can see. A generation is bumped BY
// the document pipeline itself, at the one choke point every mutation already
// passes through, so forgetting is impossible: the counter moves whether or not
// anyone remembers to notify. The direction of the risk is what matters —
// forgetting a clear costs one full compose (a frame of work nobody notices),
// while forgetting a bump would serve rows that are not on screen any more.
//
// # WHAT THE FAST PATH IS NOT ALLOWED TO DO
//
// It is not a second renderer. It re-runs renderInputRegion — the same function,
// the same styles, the same autocomplete dropdown — and re-runs the same
// bottom-anchored compositor over the same cached bands. Byte parity with the
// full path is a property of reusing those two functions, not something this file
// has to maintain. And it declines outright whenever anything but the prompt
// could have changed, so the cases it does not handle are the cases the full path
// still handles.

// promptRegionCache is the document-side of the last full compose: every band of
// the frame except the prompt, together with the geometry they were measured
// against. The prompt-only path reuses the bands verbatim and re-derives nothing
// about them.
type promptRegionCache struct {
	// valid is false until a full compose has run. A zero model — every headless
	// harness in this package — therefore never takes the fast path, which is the
	// correct default: no cached document, no reuse.
	valid bool
	// gen is the documentGen the bands below were composed at. A mismatch means
	// the document moved and the bands are stale.
	gen uint64
	// bounds/width are the pane the bands were measured for. A resize makes every
	// band wrong, so it is checked rather than assumed.
	bounds ScreenBounds
	width  int
	// borderColor is the rule colour the input region is drawn with; it is part
	// of the prompt region's own inputs, kept here so the fast path renders the
	// prompt exactly as the full path would.
	borderColor lipgloss.Style

	header    string
	viewport  string
	reasoning string
	dock      string

	// Measured heights. The fast path re-measures the bands it re-renders (the
	// header, the footer and the input region) and refuses if any of them
	// disagrees with what the budget assigned, because a changed height means a
	// changed viewport budget, and guessing at that is how a prompt bar ends up
	// floating up the screen.
	inputRows     int
	reasoningRows int
	viewportRows  int
	headerRows    int
	footerRows    int
}

// markDocumentChanged records that the document region — its rows, their order,
// or the scroll offset that selects them — is no longer what was last composed.
//
// It is called from the document pipeline, never from the key path. That
// asymmetry is the isolation: a keystroke has no reason to call it, and no way to
// call it by accident.
func (m *model) markDocumentChanged() {
	if m == nil {
		return
	}
	m.documentGen++
	m.promptDirty = true
}

// markPromptChanged records that the prompt region needs re-composing. It is the
// ONLY dirtying a keystroke performs.
func (m *model) markPromptChanged() {
	if m == nil {
		return
	}
	m.promptDirty = true
}

// documentRegionClean reports whether the cached document bands still describe
// the current document. It is the fast path's entry condition and nothing else.
func (m *model) documentRegionClean() bool {
	return m != nil && m.promptRegions.valid && m.promptRegions.gen == m.documentGen
}

// interceptPromptKey is the PROMPT FAST PATH. It is asked by model.Update very
// early — before the modal interceptors, before the key-routing chain, before
// the main type switch — whether this key is a keystroke destined for the prompt
// bar and nothing else. If it is, the key goes straight into the text input, the
// prompt region is marked dirty, and the document is left completely alone.
//
// If it is not, it returns false and Update continues into the ordinary chain,
// which is what keeps every binding, modal, picker and interceptor exactly where
// it was.
//
// # THE GUARD IS THE WHOLE RISK
//
// The fast path skips ~400 lines of routing, so it must skip only the parts that
// provably cannot claim a plain printable keystroke. Each condition below is the
// negation of one interceptor in the chain, and the reason a given key class is
// excluded is written next to it rather than left to be rediscovered:
//
//   - not a permission / quit / status / settings modal: those own the keyboard.
//   - not an approval or processing state: handleKey owns the keyboard outright.
//   - not vi-mode, a session picker, a model picker, or a diff viewer: each
//     routes keys to its own widget.
//   - not onboarding: the init stage intercepts every key.
//   - not an escape-sequence fragment: `[<0;26;37M` is an orphaned SGR mouse
//     report that must be dropped, and its runes are printable-looking.
//   - not Alt-modified and not a paste: those are keybinding mechanisms and
//     bracketed-paste deliveries, not typing.
//   - not the bare `?` on an empty buffer: that opens the help overlay.
//   - not a reasoning panel that is mounted: its band must be recomposed, not
//     reused. (Its KEY router cannot claim a printable rune — routeReasoningKey
//     declines those explicitly — so this condition is about the frame, not the
//     key, and the frame path checks it independently too.)
//   - not inside a live scroll burst: the scroll fast path composes from a
//     different chrome cache, and a keystroke that landed inside a burst would
//     have to keep the two in step.
//
// It calls the SAME handler the chain calls (forwardToInput), which is also where
// the prompt region is marked dirty — so the resulting model state is identical by
// construction, and there is no second copy of that transition here to drift. What
// the fast path removes is the reads in between and, on the following View, the
// whole document composition.
func (m *model) interceptPromptKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m == nil || !m.promptKeyUncontested(msg) {
		return nil, false
	}
	// The triple-Esc window is reset by every non-Esc keystroke, and a printable
	// rune is never an Esc. Writing it here rather than relying on the chain is
	// what makes this state transition identical rather than merely similar.
	m.escCount = 0
	return m.forwardToInput(msg), true
}

// promptKeyUncontested is the guard described on interceptPromptKey, split out so
// it can be read — and tested — as one predicate.
func (m *model) promptKeyUncontested(msg tea.KeyMsg) bool {
	// A plain, unmodified printable run is the canonical "the user is typing".
	// Alt is excluded by isPrintableRunes itself.
	if !isPrintableRunes(msg) {
		return false
	}
	// A bracketed paste is one KeyRunes carrying many runes. Bubbletea delivers
	// it as a paste, and a paste must go through the paste path (badge folding,
	// atomic cursor move), not the single-character path.
	if msg.Paste {
		return false
	}
	// Orphaned SGR mouse fragments are printable-looking runes and must be dropped.
	if len(msg.Runes) >= 2 && msg.Runes[0] == '[' && msg.Runes[1] == '<' {
		return false
	}
	if isControlSequence(msg.Runes) || IsSGRMouseFragmentRunes(msg.Runes) {
		return false
	}
	// The prompt bar only owns the keyboard when it is focused.
	if !m.ti.Focused() {
		return false
	}
	// The bare `?` on an empty buffer is a help-overlay toggle, not text.
	if len(msg.Runes) == 1 && msg.Runes[0] == '?' && strings.TrimSpace(m.ti.Value()) == "" {
		return false
	}
	// Modals own the keyboard outright.
	if m.pendingPermission != nil || m.pendingQuitConfirm || m.showStatus || m.showSettings {
		return false
	}
	// Approval / processing states route to handleKey.
	switch m.state { //nolint:staticcheck // the state is compared, never assigned
	case StateAwaitingApproval, StateProcessing, StateHotfixAmbiguous:
		return false
	}
	// A diff viewer, a vi-mode session, a picker, and onboarding each intercept.
	if m.diffActive() || m.inViMode {
		return false
	}
	if m.showSessionPicker && m.sessionPicker != nil {
		return false
	}
	if m.showModelPicker {
		return false
	}
	if m.showHelpOverlay {
		return false
	}
	if m.initStage != initNone && m.initStage != initComplete {
		return false
	}
	if !m.isProjectInitialized() {
		return false
	}
	// The reasoning panel's key router is consulted below the chain's PRIORITY 1
	// for printable keys, so it cannot claim this one — but its DOCUMENT state
	// (the expanded panel) means the frame is not just a document plus a prompt,
	// and the fast path declines so the reasoning band is recomposed too.
	if m.reasoningExpanded {
		return false
	}
	// A live mouse selection means the framebuffer overlay is on screen, which the
	// cached bands do not carry.
	if m.mouseSel.Active || m.mouseSel.Dragging {
		return false
	}
	// Inside a live scroll burst the scroll fast path owns chrome reuse and the
	// prompt renders its scroll-suppressed static view. A keystroke there has to go
	// the long way so the burst's window and the prompt's cursor stay in step —
	// and so that the burst watermark the chain clears is cleared by the chain.
	if m.isScrollActive() {
		return false
	}
	return true
}

// promptComposeFrame re-composites ONLY the prompt region and returns the
// resulting frame. It reports false whenever the cached document bands cannot be
// reused, in which case the caller runs the full path — so every case this
// declines is a case that is already correct today.
//
// The four reuse conditions are deliberately coarse. Each one is a property the
// full path would have had to re-derive anyway, and each is cheap to check:
//
//   - the document has not moved since the cached bands were composed;
//   - no overlay is mounted (BuildWorkspace would have replaced the whole frame);
//   - the pane has not been resized (every band is measured against it);
//   - the re-rendered prompt region and the freshly rendered header/footer have
//     exactly the heights the budget assigned them.
//
// The last one is the safety valve that matters: a prompt region that grew (the
// autocomplete dropdown opening) or shrank changes the viewport budget, and
// rather than reason about the new budget the path declines and lets the full
// compose measure it.
func (m *model) promptComposeFrame(bounds ScreenBounds) (string, bool) {
	if m == nil || !m.promptDirty || !m.documentRegionClean() {
		return "", false
	}
	c := &m.promptRegions
	if c.bounds != bounds {
		return "", false
	}
	width := max(m.PaneWidth(), minViewportWidth)
	if width != c.width {
		return "", false
	}
	if m.workspaceOverlayGate() || !m.Ready || m.viewRegistry == nil {
		return "", false
	}
	// The cached bands are a PLAIN composition. A surface that paints over the
	// document — the vi-mode cursor, the framebuffer selection overlay — is not
	// in them, so a frame composed from them would silently drop it.
	//
	// These are checked here rather than trusted from the key guard because this
	// is a cache, and a cache validates its own preconditions instead of assuming
	// that whoever dirtied it also knew what else was on screen.
	if m.inViMode || m.mouseSel.Active || m.mouseSel.Dragging || m.reasoningExpanded {
		return "", false
	}
	inputView := normalizeRegion(m.renderInputRegion(width, c.borderColor))
	if regionHeight(inputView) != c.inputRows {
		return "", false
	}
	// The capability set is re-derived through the current mode's own builder, not
	// read from the cache: a plan can be approved and a diff can be proposed
	// without the document moving at all, and the footer renders this set.
	actions := m.currentModeActions()
	// The header and the footer are single live lines (execution state, tok/s).
	// They are re-rendered rather than reused — a streaming frame must not paint
	// a stale token count — and their heights are re-checked, because a height
	// change is a viewport-budget change and this path does not re-derive it.
	headerView := normalizeRegion(m.renderTopBar(width))
	footerView := normalizeRegion(m.renderFixedFooter(width, actions))
	if regionHeight(headerView) != c.headerRows || regionHeight(footerView) != c.footerRows {
		return "", false
	}
	geo := m.measureViewportGeometry(headerView, c.dock, inputView, footerView)
	if geo.Height != c.viewportRows {
		return "", false
	}
	m.promptDirty = false
	m.promptComposes++
	return renderBoundedWorkspace(Workspace{
		Header:         headerView,
		Viewport:       c.viewport,
		ViewportRows:   c.viewportRows,
		ReasoningPanel: c.reasoning,
		ReasoningRows:  c.reasoningRows,
		ProposalDock:   c.dock,
		Input:          inputView,
		Footer:         footerView,
		Actions:        actions,
	}, bounds), true
}

// capturePromptRegions records the document-side bands of a full compose. It is
// called once, at the end of assembleScreen, from the one place that has just
// rendered every band and measured them against a known budget.
func (m *model) capturePromptRegions(ws Workspace, width int, bounds ScreenBounds, borderColor lipgloss.Style) {
	if m == nil {
		return
	}
	c := &m.promptRegions
	c.valid = true
	c.gen = m.documentGen
	c.bounds = bounds
	c.width = width
	c.borderColor = borderColor
	c.header = ws.Header
	c.viewport = ws.Viewport
	c.viewportRows = ws.ViewportRows
	c.reasoning = ws.ReasoningPanel
	c.reasoningRows = ws.ReasoningRows
	c.dock = ws.ProposalDock
	c.inputRows = regionHeight(ws.Input)
	c.headerRows = regionHeight(ws.Header)
	c.footerRows = regionHeight(ws.Footer)
	m.promptDirty = false
}
