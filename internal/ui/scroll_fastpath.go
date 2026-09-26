package ui

import tea "github.com/charmbracelet/bubbletea"

// ── PRIORITY ZERO: ZERO-ALLOCATION MOUSE SCROLL FAST PATH ────────────────────
//
// # WHAT THIS FILE IS
//
// The single earliest statement in model.Update(). A wheel event is answered
// here — before the workspace guard, before the permission interceptor, before
// the settings/modal swallows, before the emergency escape hatch, before the
// state machine, before the main type switch — and it is answered with an offset
// mutation and a nil command.
//
// # WHY IT HAS TO BE FIRST
//
// The scroll path and the token path compete for exactly one resource: the UI
// goroutine. A frame is 16ms at 60Hz and 33ms at the 30FPS stream cadence, so
// every millisecond the loop spends on anything other than answering input is a
// millisecond of wheel latency. A wheel event that arrives behind a workspace
// stat(), a config projection, a ledger mutex or a render pass does not arrive
// late by itself — it arrives late BY the cost of everything it was queued
// behind, and during active streaming the queue is deep, because the provider is
// producing tokens faster than the frame loop can retire them.
//
// Placing the interceptor first makes the wheel's latency a function of the
// number of events between it and the front of the queue — which is bounded by
// one frame, because the frame loop is the only thing that retires them.
//
// # THE ZERO-TIMER CONTRACT
//
// This path returns ONLY nil commands. No tea.Tick, no time.After, no
// goroutine, no state-changing message. Consecutive wheel frames therefore
// coalesce: nothing this path emits can generate work for the next event, so a
// burst of wheel notches is N offset mutations and zero extra frames.
//
// # ZERO ALLOCATION
//
// There is no string building, no layout, no markdown, no slice growth and no
// fmt on this path. m.scrollBy is pure integer mutation against a cached bound
// (maxAppScroll) and the reasoning panel's LineUp/LineDown/AtBottom are integer
// reads on a pre-rendered viewport. That is the whole contract: a scroll frame
// allocates nothing, so it cannot be the frame that triggers a GC pause in the
// middle of a stream.

// wheelScrollRows is the number of document rows one wheel notch moves on both
// scroll surfaces. It is a single constant because the two surfaces must feel
// identical: a reader who scrolls the reasoning panel three rows per notch and
// the conversation three rows per notch is reading at the same speed, and a
// split would make the panel feel slower than the answer it belongs to.
const wheelScrollRows = 3

// wheelTarget is the resolved destination of one wheel event. It is computed by
// a single O(1) classification so the decision "which surface owns this scroll"
// is made in exactly one place, exactly once, and never re-derived downstream
// from geometry that could disagree with it.
type wheelTarget int

const (
	// wheelTargetConsumed: a surface that is not the document owns the mouse.
	// The event is swallowed — never forwarded — but it still counts as
	// handled, because a modal that lets a wheel event fall through would let
	// the document scroll behind a dialog that is supposed to be opaque.
	wheelTargetConsumed wheelTarget = iota
	// wheelTargetReasoning: the expanded reasoning panel holds the scroll focus.
	wheelTargetReasoning
	// wheelTargetConversation: the main conversation document.
	wheelTargetConversation
)

// classifyWheel resolves the destination of a wheel event in constant time.
//
// The reasoning panel's claim is a FIELD (reasoningScrollLocked), not an
// inference: a hit-test on the panel's rectangle would make every scroll depend
// on sub-row geometry, and any off-by-one in that geometry is a user-visible
// wrong scroll. A field makes the hand-back exact — one Ctrl+O later, the very
// next wheel event lands on the conversation, with no re-measure and no
// dependency on where the pointer happened to be resting.
//
// The panel's lock additionally requires it to have been given rows. In a pane
// too cramped to split there is no band to scroll, and the conversation must
// keep its wheel rather than have it swallowed by a panel that is not on screen.
func (m *model) classifyWheel() wheelTarget {
	if m == nil {
		return wheelTargetConsumed
	}
	if m.wheelBlockedByOverlay() {
		return wheelTargetConsumed
	}
	if m.reasoningScrollLocked() {
		return wheelTargetReasoning
	}
	return wheelTargetConversation
}

// wheelBlockedByOverlay reports whether a surface other than the scrollable
// document owns the mouse at this instant.
//
// It is the one place the fast path is allowed to be slow, and it is slow only
// in the sense that it reads state: no stat, no allocation, no lock beyond what
// the guards themselves hold. Every predicate here mirrors a swallow that
// ALREADY existed further down Update() for the same event — a fast path that
// answered the wheel where the old path swallowed it would be a behaviour
// change dressed up as an optimisation, and the modals are the one thing that
// must not be optimised away.
func (m *model) wheelBlockedByOverlay() bool {
	// The permission interceptor blocks the whole surface while a tool call is
	// held: the wheel must not scroll the document behind an undecided call.
	if m.pendingPermission != nil {
		return true
	}
	// Onboarding owns the terminal until the workspace exists. A wheel event
	// during setup is swallowed (the main switch swallows every non-exempt
	// message), so swallowing it here preserves that exactly.
	if m.initStage != initNone && m.initStage != initComplete {
		return true
	}
	// Quit-confirm, approval diffs, the model picker, settings and the
	// standalone status overlay all own the interaction surface outright.
	return m.isModalForMouse()
}

// interceptWheel is the PRIORITY ZERO mouse interceptor. It is the first thing
// model.Update() asks, and it answers three classes of wheel event:
//
//   - blocked  → consumed here, no command, no state change.
//   - reasoning→ the panel's own viewport scrolls, the panel's own detach latch
//     moves, and the conversation is left completely alone.
//   - conversation → the cached document offset moves and the scroll burst
//     watermark is stamped; nothing is re-rendered on this frame.
//
// The third return value reports whether the event belonged to the fast path at
// all. A non-wheel mouse event (a click, a drag, a motion) returns false and
// Update() continues into the selection lifecycle, which needs the full
// geometry, the hit map and the panel row bounds — none of which may be paid
// for by a wheel notch.
func (m *model) interceptWheel(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if msg.Button != tea.MouseButtonWheelUp && msg.Button != tea.MouseButtonWheelDown {
		return nil, nil, false
	}

	switch m.classifyWheel() {
	case wheelTargetConsumed:
		// Consumed, not forwarded. Returning (m, nil, true) is what makes the
		// overlay opaque: the event never reaches the type switch below.
		return m, nil, true

	case wheelTargetReasoning:
		// The panel owns the wheel. Its detach latch moves with the wheel and
		// only with the wheel, so a scroll inside the reasoning trace can never
		// decide whether the CONVERSATION is following its own stream.
		out, cmd := m.routeReasoningScroll(msg)
		return out, cmd, true
	}

	// ── CONVERSATION ────────────────────────────────────────────────
	// This is a pure scroll frame, which is the ONE case in which the chrome
	// cache is not invalidated: the header, the prompt and the footer are
	// byte-identical across consecutive scroll notches, so re-deriving them
	// would be pure waste. Every non-scroll message sets scrollChromeDirty;
	// only this branch keeps it false, and the render path reads that to reuse
	// chrome across a burst.
	m.scrollChromeDirty = false
	if !m.Ready {
		return m, nil, true
	}
	if msg.Button == tea.MouseButtonWheelUp {
		m.scrollBy(-wheelScrollRows)
	} else {
		m.scrollBy(wheelScrollRows)
	}
	// STRICT DETACH STATE ISOLATION: m.scrollBy is the only writer of the
	// conversation's detach latch, and it writes it on EVERY notch in both
	// directions — an upward notch always engages the latch (the reader has
	// said they left the tail, even if the document had nowhere to go), and a
	// downward notch releases it only when the offset lands on the absolute
	// bottom. A reader who is still catching up is therefore never yanked by
	// the next arriving token, and the release is not deferred to a frame or
	// a timer, so the very next wheel event already agrees with it.
	return m, nil, true
}
