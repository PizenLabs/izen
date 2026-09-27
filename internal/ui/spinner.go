package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ── DECOUPLED SPINNER TICKER ─────────────────────────────────────────────────
//
// # THE DEFECT THIS REMOVES
//
// The braille indicator was advanced by WHICHEVER loop happened to be live, and
// the loops have different cadences:
//
//	smoothStreamTickMsg   20ms   token pacing for a stream
//	tickMsg               20ms   legacy background-op loop
//	shimmerTickMsg        50ms   the async context-prep shimmer
//	spinnerTickMsg       100ms   the only ticker whose cadence matches the glyph
//
// Four writers to one counter, at four rates. Two of them were rate-limited
// after the fact — the smooth loop grew a `lastSpinnerAdvance` throttle, and the
// shimmer loop a `!shimmerActive` guard — so the animation was correct only as
// long as no two loops were live at once, and a "/plan synthesis running while
// the shimmer is still winding down" frame advanced the frame twice or not at
// all. The symptom is a spinner that stutters, or that runs at 20Hz on a fast
// provider and 5Hz on a slow one, for reasons invisible to the user.
//
// # THE DECOUPLATION
//
// The animation rate is a PROPERTY OF THE GLYPH, not of the frame loop. The
// braille cycle has ten frames, so 100ms gives a full 1.0s rotation; nothing
// about the 30/60 FPS render loop should influence it. So the counter has
// exactly one writer:
//
//	spinnerTickMsg ──▶ advanceSpinnerFrame ──▶ render
//
// at SpinnerInterval, and the frame loops that render the indicator do not touch
// it. They re-render it — the frame that draws a glyph is redrawn on a steady,
// independent cadence — which is what removes the visible stutter between
// animation steps. Separating "when does the glyph change" from "when is the
// screen redrawn" is the whole trick, and it is why the two are separate
// tickers at all.
//
// # WHY THE TICKER IS ARMED, NOT ALWAYS RUNNING
//
// A permanently-armed spinner ticker is a wakeup every 100ms forever, on a
// prompt bar that is otherwise idle. The arming predicate is therefore the
// narrow one: a producer owns the indicator, or the indicator does not move. And
// because the arming happens on the frame loops (see ensureSpinnerTick), a
// background op that only ever dispatches the smooth tick still animates — the
// property the old /plan workaround existed to provide, now provided by the
// design instead of by a rate limiter.

// SpinnerInterval is the decoupled animation interval: 100ms (10Hz).
//
// 100ms is the animation rate, not the render rate: the braille glyph cycle has
// ten frames, so 10Hz gives a full 1.0s rotation. Faster cadences (the 30/60 FPS
// UI frame loop) re-render the SAME glyph 3–6 times per animation step, which is
// precisely what the decoupling above is for.
const SpinnerInterval = 100 * time.Millisecond

// advanceSpinnerFrame is the ONE writer of m.spinnerFrame.
//
// It is a function rather than an inline increment so that the ownership claim
// is checkable: every other tick handler calls this or nothing, and a second
// writer introduced anywhere else has to be a deliberate, greppable decision
// rather than a copy-pasted line. The modulo keeps the index inside the glyph
// cycle unconditionally, including for a zero-length cycle, where the existing
// max() guard in the renderers is the one that decides what to draw.
func (m *model) advanceSpinnerFrame() {
	if m == nil {
		return
	}
	m.spinnerFrame = (m.spinnerFrame + 1) % max(len(ProposalSpinnerFrames), 1)
	// Stamp the cadence watermark the leak detectors read. It is not a throttle
	// any more — nothing else consults it to decide whether to advance — but the
	// heartbeat is genuinely useful: it says "the indicator was animating as
	// recently as this", which is exactly the question the frozen-spinner
	// reconcile asks.
	m.lastSpinnerAdvance = time.Now()
}

// spinnerAnimated reports whether any indicator is currently on screen.
//
// It is deliberately a SUPERSET of the legacy re-arm predicate: every state in
// which some surface draws a spinner, snowflake, sweep or skeleton qualifies,
// including the two modal-wait states and the shimmer, because an indicator the
// user can see is one the ticker must keep moving. Arming is cheap (one 100ms
// timer) and a frozen indicator is indistinguishable from a hang; a wakeup
// while a modal is open costs one idle timer and buys an honest glyph.
//
// It is also deliberately ONE predicate used by three call sites — the ticker's
// own re-arm, the arming from the frame loops, and the leak reconcile — so the
// three can never answer differently about whether a spinner should be moving.
// That is not tidiness: the old arrangement had the tick re-arm on one predicate
// and the frame loops advance on a wider one, which is how a spinner could be
// simultaneously "live" and stopped.
func (m *model) spinnerAnimated() bool {
	if m == nil {
		return false
	}
	return m.isExecuting() ||
		m.state == StateAwaitingApproval ||
		m.streaming || m.execStreaming || m.agentRunning || m.reviewRunning ||
		m.investigateRunning || m.pipelineRunning ||
		m.shellRunning || m.planPending || m.autonomousActive ||
		m.shimmerActive || m.skeletonActive() ||
		m.indexingStatus == "indexing" || m.pendingArchArgs != ""
}

// spinnerTickCmd returns the next 100ms animation tick.
func (m *model) spinnerTickCmd() tea.Cmd {
	return tea.Tick(SpinnerInterval, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// ensureSpinnerTick arms the animation ticker when a producer owns the indicator
// and no tick is outstanding.
//
// The single-flight flag is what makes this safe to call from every frame loop:
// a "is the ticker running?" field, not a timer probe. Each arming clears the
// flag, each tick re-arms and sets it again, and a loop that calls this on every
// tick therefore dispatches exactly one live ticker however many loops are
// running — which is the property the old four-writer arrangement could not
// offer at any throttle.
//
// It returns nil when the indicator is idle or a tick is already outstanding, so
// a caller can hand the result straight to tea.Batch.
func (m *model) ensureSpinnerTick() tea.Cmd {
	if m == nil || m.spinnerTickArmed || !m.spinnerAnimated() {
		return nil
	}
	m.spinnerTickArmed = true
	return m.spinnerTickCmd()
}

// disarmSpinnerTick marks the animation ticker as no longer outstanding.
//
// It is called when a turn ends: a leaked 100ms wakeup on an idle prompt is
// cheap but not free, and the predicate that re-arms it (spinnerAnimated) is
// false at that point anyway, so the next arming would restart it cleanly.
func (m *model) disarmSpinnerTick() {
	if m == nil {
		return
	}
	m.spinnerTickArmed = false
}
