package ui

// ── Presenter: the binding between the reducer and the model (Phase 15) ──────
//
// The reducer (reducer.go) owns the two-tier viewport state. The HUD
// (viewport.go) owns in-place telemetry. Neither knows about the Bubble Tea
// model, and that is deliberate: both are pure state machines that can be
// exercised with no terminal attached.
//
// This file is the only place the two meet the model. It exists so the wiring is
// ONCE and auditable, rather than re-derived at each of the hundred-odd call
// sites that append a record. A presentation rule enforced by every call site
// remembering it is a rule that survives exactly until the call site someone
// forgot to update.
//
// Two things happen here and nowhere else:
//
//  1. RECORD INGESTION. push, logActivity and pushRecords all route through
//     commitRecord, so the narrative filter, the step classification and the
//     single-active-node invariant apply uniformly — whether the record came from
//     a domain event, a stream delta, a slash command or a future handler.
//
//  2. TELEMETRY ROUTING. Live token counts and cost are written to the HUD. They
//     are never records, so they can never appear in the conversation, and the
//     absence is structural rather than a suppression rule. The MCP status slot is
//     part of the HUD's closed vocabulary and is written through
//     ProjectionReducer.RouteTelemetry, which is what keeps the set of slots fixed
//     and a new call site from inventing one.
//
// Architectural boundary: this package's projection files import NO execution
// runtime package. The presenter receives plain values (roles, strings, counts)
// and never an executor result, a runtime event type, or a contract. That is
// what lets the whole projection be tested headless, and it is asserted
// mechanically by the architecture lock rather than by review.

import (
	tea "github.com/charmbracelet/bubbletea"
)

// hudUsageMsg carries a live provider token reading from a stream goroutine to
// the update loop, which is the only place model state may be written.
//
// It exists because the alternative was tempting and wrong: writing the HUD
// directly from the executor's callback would mutate model state off the UI
// goroutine. The HUD's own mutex would make the write safe in isolation, but the
// surrounding model is not so defended, and a telemetry counter is not worth
// introducing a race into the render path.
type hudUsageMsg struct {
	promptTokens int
}

var _ tea.Msg = hudUsageMsg{}

// projection returns the model's projection reducer, creating it on first use.
//
// Lazy construction rather than a constructor field because the trace demuxer is
// itself created lazily (push and logActivity may be the first caller), and
// binding the reducer to a demuxer that is about to be replaced would silently
// detach the divert — losing infrastructure telemetry instead of moving it, which
// is the one outcome the boundary must never produce.
func (m *model) projection() *ProjectionReducer {
	if m == nil {
		return nil
	}
	if m.telemetryDemuxer == nil {
		m.telemetryDemuxer = NewTelemetryDemuxer()
	}
	if m.narrative == nil {
		m.narrative = NewProjectionReducer(m.telemetryDemuxer, m.sidebarHUD())
	} else if m.narrative.Trace() != m.telemetryDemuxer {
		// The demuxer was replaced after the reducer was built (a /clear, a
		// re-init, a test harness). Rebind rather than keep diverting into an
		// orphaned buffer nobody renders — and rebind IN PLACE, because replacing
		// the reducer would drop the projected history along with the buffer.
		m.narrative.RebindTrace(m.telemetryDemuxer)
	}
	return m.narrative
}

// sidebarHUD returns the model's telemetry surface, creating it on first use.
func (m *model) sidebarHUD() *SidebarHUD {
	if m == nil {
		return nil
	}
	if m.hud == nil {
		m.hud = NewSidebarHUD()
	}
	return m.hud
}

// HUD returns the model's in-place telemetry surface. It is the accessor
// telemetry writers use; it is nil-safe, so a headless model that never renders
// costs nothing.
func (m *model) HUD() *SidebarHUD {
	if m == nil {
		return nil
	}
	return m.sidebarHUD()
}

// commitRecord is the ONE place a record enters the Main Viewport, and it keeps
// `m.records` in exact 1:1 correspondence with the reducer's projection.
//
// That correspondence is what makes the Single Active Node Invariant visible
// rather than merely asserted. The viewport renderer, the hit map, selection and
// copy mode all index `m.records`, so a projection that existed only BESIDE the
// flat buffer would dedupe into a state nobody renders. Committing only what the
// reducer says is NEW means:
//
//	replaced     → nothing appended; the step already occupies a node
//	diverted     → nothing appended; the line belongs to Trace
//	rejected     → nothing appended; there was nothing to say
//	opened/sealed → the new active node is appended
//	appended     → the active node's MERGED text replaces the last entry
//
// The last case is the only one that is not a pure append, and it is correct
// because a streamed continuation extends the record that is already last.
// `IncrementalLayoutUpdate` already handles a mutated trailing record, so the
// layout, the hit map and selection stay aligned with what is on screen.
func (m *model) commitRecord(rec record) bool {
	r := m.projection()
	if r == nil {
		return false
	}
	switch r.Reduce(rec) {
	case ProjectSealed, ProjectOpened:
		m.records = append(m.records, rec)
		m.cacheRecordToHistory(rec)
		return true
	case ProjectAppended:
		// The reducer merged this record into the active node; the render input
		// must show the merged text, not the fragment that produced it.
		projected := r.Records()
		if len(projected) == 0 {
			return false
		}
		last := projected[len(projected)-1]
		if len(m.records) == 0 {
			m.records = append(m.records, last)
		} else {
			m.records[len(m.records)-1] = last
		}
		return true
	default:
		// ProjectReplaced keeps the existing node (its text is identical), and
		// ProjectDiverted / ProjectRejected add nothing to the narrative. All three
		// still had their intended effect on the Trace buffer via the reducer.
		return false
	}
}

// sealNarrative seals the active node into history. The model calls it whenever
// an execution reaches a terminal state, so the final step of a run enters
// history on the same frame the run ends.
func (m *model) sealNarrative() {
	if m == nil || m.narrative == nil {
		return
	}
	m.narrative.SealActive()
}

// narrativeState exposes the two-tier projection for tests and diagnostics.
func (m *model) narrativeState() MainViewportState {
	if m == nil || m.narrative == nil {
		return MainViewportState{}
	}
	return m.narrative.State()
}

// routeContextTokens writes the compiled-context metric into the HUD.
//
// It takes a count rather than a formatted string so the HUD owns the slot's
// formatting: a caller that passed pre-formatted text would let a new call site
// invent a different rendering for the same slot, which is precisely the
// variable layout the fixed HUD exists to prevent.
func (m *model) routeContextTokens(tokens int) {
	if m == nil {
		return
	}
	m.HUD().SetTokens(tokens)
}

// executionStepLedger returns the model's Phase 16 evidence ledger of
// runtime-attested execution steps, creating it on first use.
//
// Lazy construction for the same reason the projection reducer is lazy: a
// headless model that never renders costs nothing, and a model built directly
// as a struct literal (every test harness) still gets a real ledger rather than
// a nil one that would silently suppress all step rendering.
func (m *model) executionStepLedger() *ExecutionStepLedger {
	if m == nil {
		return nil
	}
	if m.executionSteps == nil {
		m.executionSteps = NewExecutionStepLedger()
	}
	return m.executionSteps
}

// routeStepStarted records a runtime StepStarted event. This is one of the two
// — and only two — ways an execution step can ever reach the screen.
//
// key is the step's runtime identity (the bounded-step ordinal) and name is
// optional display context. They are separate parameters because the start
// event carries a model id and the completion event does not: deriving the
// identity from the richer string would make the two events describe two
// different steps.
func (m *model) routeStepStarted(key, name string) {
	m.executionStepLedger().RecordStepStarted(key, name)
}

// routeStepCompleted records a runtime StepCompleted event under the same key
// the start event used.
func (m *model) routeStepCompleted(key string) {
	m.executionStepLedger().RecordStepCompleted(key)
}

// attestedExecutionSteps exposes the ledger for assertions and diagnostics. A
// test that wants to know whether the UI is showing invented progress asks this
// rather than counting glyphs in a rendered string.
func (m *model) attestedExecutionSteps() []ExecutionStep {
	return m.executionStepLedger().Steps()
}

// routeCost writes the cost metric. Formatting is the caller's, because cost
// formatting depends on the pricing registry the caller owns.
func (m *model) routeCost(amount string) {
	if m == nil {
		return
	}
	m.HUD().SetCost(amount)
}

// renderSidebarHUD renders the fixed telemetry block for the current width. It
// returns "" when the HUD has nothing real to show, so a caller can append it
// unconditionally: a fresh session mounts no telemetry at all rather than three
// rows of placeholders.
func (m *model) renderSidebarHUD() string {
	if m == nil {
		return ""
	}
	hud := m.HUD()
	if !hud.Active() {
		return ""
	}
	return hud.Render(m.PaneWidth())
}
