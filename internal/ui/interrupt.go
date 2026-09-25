package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Double-Tap Esc Interrupt Protocol ─────────────────────────────────────
// Safe stream cancellation: the first Esc press arms a 1.5s window (footer
// flips to "Press Esc again!"); only a second Esc inside the window cancels.
// Ctrl+C stays a silent hard-interrupt fallback (never rendered). A single
// event-driven tea.Tick per arming disarms — zero tickers, zero polling, zero
// CPU overhead. sequenceID drops stale ticks so rapid presses can never
// disarm a newer window.

// isInterruptArmed reports whether the Esc double-tap window is currently live.
func (m *model) isInterruptArmed() bool {
	if m == nil {
		return false
	}
	if !m.interruptState.armed {
		return false
	}
	return time.Since(m.interruptState.armedAt) <= InterruptWindow
}

// disarmInterrupt clears the arming window unconditionally.
func (m *model) disarmInterrupt() {
	m.interruptState.armed = false
}

// cancelStreamCmd emits the single stream-cancellation signal. The Update
// handler funnels it through the emergency interrupt path.
func (m *model) cancelStreamCmd() tea.Cmd {
	return func() tea.Msg { return MsgCancelStream{} }
}

// armInterruptCmd returns the single event-driven disarm tick for seq.
func armInterruptCmd(seq uint64) tea.Cmd {
	return tea.Tick(InterruptWindow, func(_ time.Time) tea.Msg {
		return MsgResetInterruptState{SequenceID: seq}
	})
}

// isInterruptModalActive reports whether an overlay/modal owns Esc right now.
// While true, Esc must close/cancel the modal first and MUST NOT arm or fire
// the stream-cancellation flow (modal input isolation).
func (m *model) isInterruptModalActive() bool {
	if m == nil {
		return false
	}
	// Approval gates consume Esc as reject — never an interrupt.
	if m.state == StateAwaitingApproval || m.state == StateHotfixAmbiguous {
		return true
	}
	if m.pendingPermission != nil {
		return true
	}
	if m.pendingQuitConfirm {
		return true
	}
	if m.diffActive() {
		return true
	}
	if m.showModelPicker {
		return true
	}
	if m.showStatus {
		return true
	}
	if m.showSessionPicker {
		return true
	}
	if m.pendingAutonomyProposal != nil {
		return true
	}
	if len(m.pendingAutonomyTargets) > 0 {
		return true
	}
	if m.autonomousBoundary != nil {
		return true
	}
	if m.proposalTUI != nil {
		return true
	}
	if m.pendingBuildApproval || m.pendingBuildTask != nil {
		return true
	}
	if m.pendingHotfixTask != nil {
		return true
	}
	if m.toolCallBuffer != nil && m.toolCallBuffer.HasPending() {
		return true
	}
	if m.showTraceOverlay {
		return true
	}
	if m.showHelpOverlay {
		return true
	}
	if m.inViMode {
		return true
	}
	return false
}

// handleInterruptEsc implements the double-tap Esc state machine. It must only
// be called for an exact tea.KeyEsc while executing and with no modal active.
// It returns (handled, cmd): handled=false means Esc keeps its normal role.
func (m *model) handleInterruptEsc() (bool, tea.Cmd) {
	// Exact KeyEsc match: Bubble Tea's internal key decoder already resolves
	// multi-byte ANSI arrow sequences (\x1b[A, etc.) to distinct key types, so
	// matching Type == KeyEsc here never intercepts arrows and never blocks
	// input polling.
	if m.isInterruptModalActive() {
		return false, nil
	}
	if !m.isExecuting() {
		return false, nil
	}
	// Second press inside a live window → cancel the active stream.
	if m.interruptState.armed && time.Since(m.interruptState.armedAt) <= InterruptWindow {
		m.disarmInterrupt()
		// Suppress the triple-Esc vi-mode counter: interrupt taps are not
		// idle navigation gestures.
		m.escCount = 0
		return true, m.cancelStreamCmd()
	}
	// First press → arm the window and schedule the single disarm tick.
	m.interruptState.armed = true
	m.interruptState.armedAt = time.Now()
	m.interruptState.sequenceID++
	seq := m.interruptState.sequenceID
	m.escCount = 0
	return true, armInterruptCmd(seq)
}

// handleInterruptCtrlC is the silent hard-interrupt fallback: it disarms any
// pending Esc window and emits the same cancellation signal immediately,
// ignoring the double-tap requirement.
//
// update.go disarms first and funnels through handleCtrlC/handleEmergencyInterrupt
// to preserve the double-Ctrl+C grace and parked-run routing.
//
//nolint:unused // Explicit spec fallback entry point; the live Ctrl+C path in
func (m *model) handleInterruptCtrlC() tea.Cmd {
	m.disarmInterrupt()
	return m.cancelStreamCmd()
}
