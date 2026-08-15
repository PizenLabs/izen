package ui

import (
	"time"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/router"
)

// ── Interaction-surface lifecycle (Phase 5) ──────────────────────────────────
//
// The TUI conflated several state lifetimes inside /clear, so a visual clear
// (a) left stale execution-activity surfaces (ActivityTree / Execution Log /
// control tree / thinking block) rendered below the cleared records, and (b)
// destroyed durable session state (history, tasks, ledger) — /new semantics —
// while (c) late engine events from a just-finished operation could re-append
// to the cleared records and activity tree ("resurrected activity").
//
// This file establishes the explicit lifecycle boundaries:
//
//	SESSION   — durable conversational/runtime context. NEVER touched by /clear.
//	WORKSPACE — repository state + current mode. NEVER touched by /clear.
//	OPERATION — the single authoritative foreground execution. /clear does not
//	           kill it; it only stops projecting its remaining events.
//	EXECUTION ACTIVITY — transient records produced by an execution
//	           (read/edit/apply/verify/model stages). Owned by the execution,
//	           cleared by /clear.
//	PRESENTATION — everything currently rendered (stream, thinking, loading
//	           dock, chips, viewport buffers). Cleared by /clear.
//
// /clear === resetTransientInteraction(): clears PRESENTATION + EXECUTION
// ACTIVITY + the transient interaction gates, and seals the activity surface
// so late events from a cleared execution can never repopulate the viewport.
// /clear is NOT /new: session, workspace, mode, objective and token telemetry
// all survive.

// clearPresentation clears the transient presentation surface: stream buffers,
// thinking buffers, loading shimmer, trace/output buffers, and streaming flags.
// It never touches execution-activity stores or durable session/workspace state.
func (m *model) clearPresentation() {
	m.currentPrompt = ""
	m.responseBuffer.Reset()
	m.reasoningBuffer.Reset()
	m.streamBuffer = ""
	m.currentStreamContent = ""
	m.traceBuffer.Reset()
	m.traceExpanded = false
	m.traceWindowStart = 0
	m.traceWindowAnchored = false
	m.pendingReasoningFragment = ""
	m.sentinelReasoningFlushed = 0
	m.resetStreamBlocks()
	m.streaming = false
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	if m.streamThrottle != nil {
		m.streamThrottle.Reset()
	}
	if m.thinkingBuffer != nil {
		m.thinkingBuffer.Reset()
	}
	if m.thinkingPanel != nil {
		m.thinkingPanel.Reset()
	}
	if m.liveCodePreview != nil {
		m.liveCodePreview.Reset()
	}
	m.stopShimmer()
}

// clearExecutionActivity clears the execution-activity surfaces rendered below
// the records in the viewport: the structured ActivityTree, the foldable
// Execution Log, the fact-only control tree, the authoritative stage, the
// retained telemetry snapshot, the workflow result (action chips) and any
// proposed shell command. Durable session/workspace state is never touched.
func (m *model) clearExecutionActivity() {
	if m.activityTree != nil {
		m.activityTree.Reset()
	}
	if m.logStore != nil {
		m.logStore.Clear()
	}
	m.controlSnapshot = nil
	m.controlRunID = ""
	m.lastExecutionSnapshot = execution.TelemetrySnapshot{}
	m.lastExecutionTelemetry = nil
	if m.stage != nil {
		m.stage.mu.Lock()
		m.stage.Label = ""
		m.stage.Target = ""
		m.stage.State = stageDone
		m.stage.LastTs = time.Now()
		m.stage.Bytes = 0
		m.stage.Elapsed = 0
		m.stage.Tokens = 0
		m.stage.Telemetry = nil
		m.stage.mu.Unlock()
	}
	// currentResult drives the Action Chip rendering — a cleared surface must
	// not keep stale chips from a completed/obsolete workflow.
	m.currentResult = nil
	m.proposedShellCmd = ""
}

// resetTransientInteraction is the /clear entry point. It clears the visible
// interaction surface AND every transient interaction gate while preserving
// the durable session, workspace, current mode, objective and token telemetry.
// It then seals the activity surface so any late engine/result event from the
// execution that was just cleared can never repopulate the viewport.
func (m *model) resetTransientInteraction() {
	m.records = nil
	m.PreRenderedHistory = ""
	m.showBanner = true

	m.clearPresentation()
	m.clearExecutionActivity()

	// ── Transient interaction gates (pending proposal/approval/route state) ──
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.acceptedProposals = nil
	m.acceptAll = false
	m.pendingBuildApproval = false
	m.pendingBuildTask = nil
	m.pendingBuildAllowAlways = false
	m.pendingHotfixTask = nil
	m.pendingHotfixPatch = nil
	m.pendingHotfixAmbiguous = nil
	m.hotfixCandidatesMode = false
	m.pendingHotfixCandidate = nil
	m.appliedHotfixFile = ""
	m.pendingRouteConfirm = false
	m.pendingRouteInput = ""
	m.pendingRouteResult = router.ClassificationResult{}
	m.pendingRouteOptions = nil
	m.pendingRouteIdx = 0
	m.currentBuildTaskID = 0
	m.pendingTestConfirm = false
	m.pendingTestTarget = ""
	m.investigateInvocationCount = 0
	m.buildRecoveryCount = 0
	m.buildVerifyPending = false
	m.fastTrackTargets = nil
	if m.toolCallBuffer != nil {
		m.toolCallBuffer.Reject()
	}

	// ── Transient inter-operation caches (never durable session state) ──
	m.handoffCtx = HandoffContext{}
	m.handoffLedgerContent = ""
	m.lastInvestigateLedger = nil
	m.lastTestOutput = ""
	m.lastTestFailed = false
	m.lastTestTarget = ""
	m.pendingFileRefs = nil
	m.uiNotice = ""

	// ── Pending plan artifact ─────────────────────────────────────
	// The staged plan task list is a pending execution/proposal artifact (the
	// ownership table's "pending artifact" row): /clear discards it so a
	// subsequent /build starts from a clean slate. Durable session state
	// (history, objective, context ledger, investigation/review IDs) survives.
	if m.sess != nil {
		m.sess.ClearTasks()
	}

	// ── Unwind transient busy flags + spinner to interactive chat ──
	// clearBusyFlags ONLY clears transient processing flags — it never cancels
	// cancellation handles (streamCancel/shellCancel) or the active operation,
	// so a genuinely running operation keeps running (its remaining events are
	// simply not projected while the surface is sealed).
	m.clearBusyFlags()
	m.resolveApprovalState()
	m.syncUIState()
	m.ti.Focus()
	m.recalcViewportHeight()

	// ── Seal the activity surface against late events from the cleared
	// execution. A new user submission (submitEnter) or a new foreground
	// operation (beginOperation) opens it again. ──
	m.sealActivitySurface()

	m.refreshViewportContent()
	if m.Ready {
		m.Viewport.GotoBottom()
	}
}

// sealActivitySurface closes the activity surface. While sealed, every
// engine-derived projection (domain events, engine telemetry, shell output,
// terminal result records, reasoning streams, control facts) is dropped so a
// late event from a cleared execution can never resurrect stale activity.
func (m *model) sealActivitySurface() {
	m.activitySurfaceSealed = true
}

// unsealActivitySurface reopens the activity surface. It is called when a new
// foreground operation begins (beginOperation) or a new user submission is
// processed (submitEnter) — the moment the user starts a fresh interaction,
// its events are welcome in the viewport again.
func (m *model) unsealActivitySurface() {
	m.activitySurfaceSealed = false
}
