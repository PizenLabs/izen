package ui

// ── Transient pre-execution skeletons (lifecycle + atomic replacement) ────────
//
// A skeleton is a ONE-ROW placeholder the viewport mounts while the runtime is
// producing a surface it cannot show yet. The row is real content — it scrolls,
// it is selectable, it occupies a line in the document — and it is TRANSIENT:
// the instant the surface exists, the finalized content must occupy that exact
// position, with no blank line, no stale frame, and no shift of the terminal's
// scroll history.
//
// This file is the ledger that owns that lifecycle. It deliberately contains
// no rendering of the indicator itself (that is components.Skeleton) and no
// knowledge of WHAT is pending (that is internal/ui/states). Its only job is
// the mount → advance → release transition, and the guarantee that release is
// atomic from the viewport's point of view.
//
// ── WHY RELEASE IS ATOMIC ────────────────────────────────────────────────────
//
// The viewport is a pure projection: refreshViewportContent rebuilds the whole
// visible window (chrome + document + tail) from state on every frame, and the
// bubbles Viewport is handed exactly that pre-sliced window with YOffset pinned
// to 0. There is therefore NO incremental buffer to append to and nothing to
// erase — a released skeleton is simply absent from the next projection. That is
// what makes the replacement atomic: the row count and the row ORDER are decided
// once, from state, in a single pass. A "blank line left behind" is not
// representable in this design, which is exactly why the indicator is modelled
// as mounted-state rather than as something drawn and then erased.
//
// The corollary is that release MUST happen in the same turn as the content's
// arrival, or the user sees one frame of the indicator sitting under content
// that already exists. finalizeSkeleton therefore repaints synchronously;
// releaseSkeleton is the raw, repaint-free form for callers that are already
// inside a projection (the streaming block path).
//
// ── OWNERSHIP ────────────────────────────────────────────────────────────────
//
// Exactly one indicator is mounted at a time (states.Machine is a single slot),
// and each release is keyed by the state that mounted it. A finalizer for a
// state that no longer owns the row is a no-op: a late "code block finished"
// event can never tear down the "staging edit" indicator that replaced it.

import (
	"github.com/PizenLabs/izen/internal/ui/components"
	"github.com/PizenLabs/izen/internal/ui/markdown"
	"github.com/PizenLabs/izen/internal/ui/states"
)

// ensureSkeleton lazily allocates the lifecycle machine. It exists so a model
// built as a struct literal (every headless test harness in this package does
// that) behaves identically to one built by the program bootstrap: the ledger is
// always usable and the zero value is the resting state.
func (m *model) ensureSkeleton() {
	if m.lifecycle == nil {
		m.lifecycle = states.NewMachine()
	}
}

// skeletonActive reports whether a pre-execution indicator is currently mounted.
func (m *model) skeletonActive() bool {
	if m == nil || m.skeleton == nil {
		return false
	}
	return m.skeleton.Active()
}

// mountSkeleton mounts the indicator for st/target, superseding any indicator
// already mounted. It reports whether a mount happened; a non-pending state is
// rejected, so this can never produce a row that renders nothing.
func (m *model) mountSkeleton(st states.State, target string) bool {
	if m == nil {
		return false
	}
	m.ensureSkeleton()
	snap, ok := m.lifecycle.Enter(st, target)
	if !ok {
		return false
	}
	m.skeleton = components.NewSkeleton(snap.Indicator(), snap.Target)
	m.skeleton.SetFrame(m.skeletonFrame)
	m.skeleton.SetWidth(m.skeletonWidth())
	return true
}

// advanceSkeleton moves a mounted indicator to st/target WITHOUT superseding it,
// so a finalizer that is already in flight for this mount still applies. It is
// the seam for a block whose subject becomes known mid-flight (the fence
// language arrives after the fence itself).
func (m *model) advanceSkeleton(st states.State, target string) {
	if m == nil || !m.skeletonActive() {
		return
	}
	m.ensureSkeleton()
	snap := m.lifecycle.Advance(st, target)
	if !snap.Active {
		// A non-pending Advance exits the machine; drop the row to match.
		m.skeleton = nil
		return
	}
	m.skeleton.Set(snap.Indicator(), snap.Target)
	m.skeleton.SetWidth(m.skeletonWidth())
}

// relabelSkeleton replaces only the mounted indicator's subject, leaving the
// state alone. An empty label keeps the current one, so a caller that briefly
// has no target cannot blank a line it already resolved.
func (m *model) relabelSkeleton(target string) {
	if m == nil || !m.skeletonActive() {
		return
	}
	m.ensureSkeleton()
	snap := m.lifecycle.Relabel(target)
	if !snap.Active {
		m.skeleton = nil
		return
	}
	m.skeleton.Set(snap.Indicator(), snap.Target)
	m.skeleton.SetWidth(m.skeletonWidth())
}

// releaseSkeleton unmounts the indicator IF it is the one st mounted. It
// performs no repaint: use it from inside a projection that is already building
// the frame the caller wants to see. It reports whether the row was released.
func (m *model) releaseSkeleton(st states.State) bool {
	if m == nil || m.skeleton == nil || m.lifecycle == nil {
		return false
	}
	snap := m.lifecycle.Snapshot()
	if !snap.Active || snap.State != st {
		return false
	}
	m.lifecycle.Exit()
	m.skeleton = nil
	return true
}

// unmountSkeleton removes the indicator regardless of which state mounted it,
// without substituting content and without repainting. It is the cancellation /
// clear path: there is no finalized content to put in the row's place, and the
// caller owns the repaint. It reports whether anything was unmounted, so a
// redundant unmount is observable rather than silently successful.
func (m *model) unmountSkeleton() bool {
	if m == nil || m.skeleton == nil {
		return false
	}
	if m.lifecycle != nil {
		m.lifecycle.Exit()
	}
	m.skeleton = nil
	return true
}

// finalizeSkeleton performs the ATOMIC REPLACEMENT: it releases the row st
// mounted and substitutes the finalized content in its place.
//
// `finalized` is the content that takes the row's place in the viewport buffer:
//
//   - A non-empty string is committed as a record, so it renders through the
//     document layout in exactly the position the indicator occupied.
//   - An empty string means the finalized content ALREADY landed in the
//     document or in another surface (a rendered code block, a mutation card, a
//     tool card). Releasing the row is then the whole replacement, and pushing
//     anything would duplicate work that is already visible.
//
// Either way the repaint is SYNCHRONOUS, on this turn: the indicator and its
// replacement must never coexist across a frame, or the user sees a duplicate.
// The function reports whether a replacement happened; a finalizer for a state
// that no longer owns the row is a no-op and reports false.
func (m *model) finalizeSkeleton(st states.State, finalized string) bool {
	if !m.releaseSkeleton(st) {
		return false
	}
	if finalized != "" {
		m.push(roleStatus, finalized)
	}
	m.refreshViewportContentImmediate()
	return true
}

// skeletonWidth is the terminal budget the mounted indicator must fit inside. It
// is the wrap width when one is established (the same budget the document layout
// uses, so the row cannot be wider than the content it sits among) and the raw
// terminal width before the first layout. A non-positive result is passed through
// as "unconstrained", which the widget reads as the pre-bootstrap case.
func (m *model) skeletonWidth() int {
	if m == nil {
		return 0
	}
	if m.wrapWidth > 0 {
		return m.wrapWidth
	}
	return m.width
}

// skeletonSyncWidth re-aligns a mounted indicator with the current terminal
// width. It is called from the resize handler and from mount, never from the
// render path, so View stays a pure projection.
func (m *model) skeletonSyncWidth() {
	if m == nil || m.skeleton == nil {
		return
	}
	m.skeleton.SetWidth(m.skeletonWidth())
}

// advanceSkeletonFrame advances the indicator's animation frame.
//
// The indicator has NO private clock. Its frame is the model's single master
// animation counter (m.frame, see advanceAnimationFrame), which the ~30 FPS
// FrameTickMsg advances. This function is the ~100ms shimmer tick's nudge into
// that same counter, so the two loops cooperate on one monotonic timeline
// instead of racing two counters that both write the row — a row whose frame
// could be pulled backwards by whichever loop ran last is a row that visibly
// stutters.
//
// Advancing from either loop therefore moves the wave forward and never back.
func (m *model) advanceSkeletonFrame() {
	if m == nil {
		return
	}
	m.advanceAnimationFrame()
}

// skeletonRenderLine renders the mounted indicator as exactly ONE physical row,
// or "" when nothing is mounted. It is the only place the row enters the
// viewport, so the one-row invariant has exactly one enforcement point.
//
// A mounted indicator always yields a row (at worst the bare glyph, at a
// degenerate width), because a mount that renders nothing is indistinguishable
// from no mount at all. The empty return is therefore the inactive case plus a
// total-function guard.
func (m *model) skeletonRenderLine() string {
	if !m.skeletonActive() {
		return ""
	}
	line := m.skeleton.View()
	if line == "" {
		return ""
	}
	return line
}

// skeletonStatusMirror returns the mounted indicator's plain-text line, or ""
// when nothing is mounted.
//
// It is the CANONICAL sentence for the mounted state: a pure function of the
// lifecycle snapshot, so the viewport row, the mount/release assertions, and
// any diagnostic can all read it without ever rewording it.
//
// It is deliberately NOT a status-bar input. A transient indicator is claimed
// inside the viewport, on the streaming cursor line, and nowhere else: the
// bottom bar is a fixed surface whose whole purpose is core telemetry (model,
// latency, tokens, tok/s), and a second copy of a moving claim there can only
// add a way for two surfaces to disagree about the same fact. Nothing outside
// this package renders the string.
func (m *model) skeletonStatusMirror() string {
	if m == nil || m.lifecycle == nil {
		return ""
	}
	return m.lifecycle.Snapshot().Indicator()
}

// ── Streaming block derivation ───────────────────────────────────────────────

// streamingOwnedStates are the indicator states the streaming block derivation
// may mount and release. It is an explicit list, not a filter over states.All():
// the ownership boundary is the whole point of this file, so it is spelled out
// where a reader can see that the runtime's three states are NOT in it.
var streamingOwnedStates = [...]states.State{
	states.StateCodePending,
	states.StateTablePending,
}

// pendingBlockState derives the pre-execution state from the streaming block
// renderer's own state. It is a pure READ of state the renderer already holds —
// it fabricates nothing and consults no independent flag, so the indicator can
// never claim a block is being formatted when the renderer is not actually
// holding one back.
//
// Three truthful cases, in priority order:
//
//  1. An OPEN FENCE in the committed renderer: the code lines are buffered and
//     nothing is renderable until the closing fence arrives → StateCodePending.
//  2. An OPEN TABLE in the committed renderer — or a LATCHED table holdback:
//     the rows are in the full holdback buffer and the column-width grid cannot
//     be computed until the block terminates → StateTablePending.
//  3. The still-growing TRAILING LINE is itself an in-flight construct (a
//     half-received fence, a table row whose closing pipe has not arrived):
//     the block buffer reports it as held back → StateCodePending /
//     StateTablePending accordingly.
//
// ── WHY CASE 2 IS A LATCH AND NOT A DERIVATION ──────────────────────────────
//
// The obvious implementation — "is the latest chunk table-shaped?" — is wrong,
// and wrong in the most visible way possible. A table's rows arrive interleaved
// with fragments that are not table syntax, and each such fragment reports
// "nothing is being held back". The indicator therefore mounts and unmounts
// once per cell boundary: the row blinks, its glyph restarts from frame zero,
// and the document height changes on every flip. That IS the flicker.
//
// So the table case reads m.tableLatch, a hysteretic one-way hold (states.Latch)
// that turns ON at the opening pipes and is released ONLY by an explicit
// termination signal:
//
//   - a complete line following a valid row that carries no cell boundary
//     (a zero-pipe line — and, as a strict superset, any complete line that is
//     not itself a table row);
//   - a blank line, i.e. the double line break "\n\n";
//   - stream completion: StreamEnd, a PARTIAL token-limit truncation, or Ctrl+C.
//
// The latch is reconciled from the authoritative holdback once per streaming
// tick (syncTableLatch), so it is never derived from the most recent chunk and
// can never be released by a pipe-free intermediate fragment.
//
// Note the priority order: an open fence SUPERSEDES the table latch, because
// inside a block the block buffer deliberately reports the line as ordinary
// text (the block owns it) and because a fence is a strictly stronger claim —
// it is guaranteed more content is coming.
func (m *model) pendingBlockState() (states.State, string, bool) {
	if m == nil || !m.streaming {
		return states.StateIdle, "", false
	}
	r := m.aiStreamRenderer
	if r == nil {
		return states.StateIdle, "", false
	}
	if r.inCode {
		// The fence language is the most useful subject available and is a
		// truthful one: it is the language the block is being formatted FOR.
		return states.StateCodePending, r.lang, true
	}
	// LATCH ON: engaged only by a real table BLOCK. Either the authoritative
	// holdback is open, or a prior projection already engaged the latch. Engage
	// is idempotent, so re-asserting it every frame is free and never bumps the
	// epoch — which is what keeps the row from blinking.
	if r.tableHolding() || m.ensureTableLatch().On() {
		m.ensureTableLatch().Engage()
		return states.StateTablePending, "", true
	}
	if m.aiStreamUncommitted != nil {
		if kind, incomplete := m.aiStreamUncommitted.Kind(); incomplete {
			switch kind {
			case markdown.BlockTable:
				// The opening pipes are arriving on the still-growing line. This
				// mounts the SAME indicator but does NOT latch: a partial line
				// is not yet a block — it may still resolve into prose that
				// merely begins with a pipe — and a mount that outlives its
				// evidence is exactly the fabricated work the no-fabricated-work
				// invariant forbids. Both paths yield StateTablePending, so a
				// real table's indicator is continuous across the promotion from
				// "trailing line" to "latched block" with no frame in between.
				return states.StateTablePending, "", true
			case markdown.BlockFence:
				return states.StateCodePending, "", true
			}
		}
	}
	return states.StateIdle, "", false
}

// streamingSkeletonOwns reports whether a state is one the streaming derivation
// may mount and release. It is the boundary that keeps the streaming path from
// reaching into an indicator another subsystem owns: a "staging edit" indicator
// mounted from a runtime event is never released because a code block finished.
func streamingSkeletonOwns(st states.State) bool {
	for _, owned := range streamingOwnedStates {
		if st == owned {
			return true
		}
	}
	return false
}

// syncStreamingSkeleton reconciles the mounted indicator with the streaming
// block renderer's state. It is PURE STATE: no repaint, no content, no I/O. It
// is called from inside the projection pass that is already building the frame,
// so the reconciliation is visible in the very frame it is computed for — which
// is what makes the release atomic without a second pass.
func (m *model) syncStreamingSkeleton() {
	if m == nil {
		return
	}
	st, target, pending := m.pendingBlockState()

	if !pending {
		// Nothing is being held back. Release an indicator WE own and nothing
		// else: a runtime-owned indicator is released by its own finalizer.
		for _, owned := range streamingOwnedStates {
			if m.releaseSkeleton(owned) {
				return
			}
		}
		return
	}

	m.ensureSkeleton()
	snap := m.lifecycle.Snapshot()
	switch {
	case !snap.Active:
		m.mountSkeleton(st, target)
	case streamingSkeletonOwns(snap.State):
		if snap.State == st {
			m.relabelSkeleton(target)
		} else {
			// The held-back construct CHANGED (a partial table row became a
			// fence, or a fence closed and a row began). Re-point the existing
			// row rather than releasing and re-mounting, so the row count never
			// flickers by one for a single frame.
			m.advanceSkeleton(st, target)
		}
	}
	// A non-streaming state owns the row: stay silent. The runtime's own
	// finalizer will release it.
}
