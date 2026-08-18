package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/execution"
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
//	           kill it; it only stops projecting its remaining events. /drop
//	           cancels it explicitly (discard pending action).
//	EXECUTION ACTIVITY — transient records produced by an execution
//	           (read/edit/apply/verify/model stages). Cleared by /clear.
//	PRESENTATION — everything currently rendered (stream, thinking, loading
//	           dock, chips, viewport buffers). Cleared by /clear.
//
// Command contract (see commands.go):
//
//	/clear = "Clear what I SEE."  resetTransientInteraction() clears
//	           PRESENTATION + EXECUTION ACTIVITY + the visible approval
//	           presentation and seals the activity surface. It keeps the
//	           session, workspace, mode, context (context ledger, attached
//	           files, staged plan tasks, persistent history), git and token
//	           telemetry. It executes nothing and creates nothing.
//	/drop   = "Discard what I am ABOUT TO DO."  discardPendingAction() cancels
//	           the active transient execution and discards every pending
//	           proposal / pending action / unresolved mutation / staged plan.
//	           It keeps the conversation (records), session and workspace.
//	/new    = FUTURE session boundary: new session, new conversation context,
//	           reset transient state, fresh presentation. NOT implemented in
//	           this phase — /clear is deliberately NOT /new.
//
// /clear is NOT /new: session, workspace, mode, objective, context, staged
// plan tasks and token telemetry all survive a clear.

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
	m.lastPromptEnvelope = PromptEnvelope{}
	m.lastExecutionProof = ExecutionProof{}
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
	m.hotfixCandidatesMode = false
	m.appliedHotfixFile = ""
	m.clearAutonomyProposal()
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

// discardPendingAction is the /drop entry point: "Discard what I am ABOUT TO
// DO." It cancels the active transient execution (if any) and discards every
// pending proposal, pending action, unresolved mutation and the staged plan
// task list. Unlike /clear it NEVER touches the conversation: records,
// execution activity surfaces, session history, objective, context ledger and
// workspace all survive a drop. It also never seals the activity surface — the
// conversation stays live.
func (m *model) discardPendingAction() {
	// ── Cancel active transient execution (if applicable) ─────────
	// The single authoritative terminal path releases the operation and
	// cancels its context so provider/subprocess/worker observe cancellation.
	if m.activeOp != nil {
		m.finalizeOperation(OpOutcomeCancelled, context.Canceled)
	}
	m.cancelAllBackgroundContexts()
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	if m.shellCancel != nil {
		m.shellCancel()
		m.shellCancel = nil
	}
	execution.KillAllOrphans()

	// ── ROLL BACK THE ACTIVE MUTATION BOUNDARY (Phase 9A/9B) ────────
	// If a mutation already occurred (an apply in flight), /drop restores the
	// workspace by rolling back exactly the current MutationSet — the one owned
	// by the operation being discarded. A pending (unapplied) set rolls back as
	// a clean no-op. This is the /drop counterpart of the Ctrl+C cancel path.
	if m.execEng != nil {
		if errs := m.execEng.RollbackTransaction(); len(errs) > 0 {
			for _, err := range errs {
				m.push(roleError, fmt.Sprintf("drop rollback error: %v", err))
			}
		}
	}

	// ── Discard pending proposals / pending actions ─────────────────
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.acceptedProposals = nil
	m.acceptAll = false
	m.pendingBuildApproval = false
	m.pendingBuildTask = nil
	m.pendingBuildAllowAlways = false
	m.pendingHotfixTask = nil
	m.pendingHotfixPatch = nil
	m.hotfixCandidatesMode = false
	m.appliedHotfixFile = ""
	// Discard the multi-file execution graph (Phase 9B). The MutationSet it
	// owned was rolled back above.
	m.activeGraph = nil
	m.clearAutonomyProposal()
	m.pendingTestConfirm = false
	m.pendingTestTarget = ""
	// Discard unresolved mutations pending in the tool-call buffer.
	if m.toolCallBuffer != nil {
		m.toolCallBuffer.Reject()
	}

	// ── Discard the staged plan task list (a pending execution plan
	// is precisely "what I am about to do"). The durable session record
	// (history, objective, context ledger) survives. ──
	if m.sess != nil {
		m.sess.ClearTasks()
	}

	// ── Unwind transient busy flags / approval gate back to chat ──
	m.resolveApprovalState()
	m.syncUIState()
	m.ti.Focus()
}
