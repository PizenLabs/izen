package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// Init initializes the spinner tick and text input blink.
func (m *model) Init() tea.Cmd {
	m.currentTip = randomTip()
	if m.initStage != initNone && m.initStage != initComplete {
		return m.spinnerTickCmd()
	}
	return tea.Batch(m.spinnerTickCmd(), m.ti.Focus())
}

// Update routes state machines and events.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ── GLOBAL PANIC RECOVERY ──────────────────────────────────────────
	// Any panic inside the update loop is caught here, the full stack trace
	// is written to stderr for debugging, and the program exits cleanly.
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			fmt.Fprintf(os.Stderr, "\nIZEN PANIC: %v\nStack:\n%s\n", r, buf[:n])
			os.Exit(1)
		}
	}()

	// ── HARD KEYBOARD INTERCEPT: Approval/Processing states bypass all sub-components ──
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			return m.handleKey(keyMsg)
		}
	}

	// ── GLOBAL INTERCEPT: [Alt+P] Toggle Hotkey ────────────────────────────
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "alt+p" {
			if m.state == StateAwaitingApproval && len(m.pendingProposals) > 0 {
				m.pendingProposals[0].Expanded = !m.pendingProposals[0].Expanded
				m.proposalDiffOffset = 0
				m.recalcViewportHeight()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return m, nil
			}
		}
	}

	// ── Triple-Escape detection: 3 consecutive esc presses enter vi-mode ─
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Type == tea.KeyEsc {
		now := time.Now()
		if now.Sub(m.lastEscTime) > viTripleEscMax {
			m.escCount = 1
		} else {
			m.escCount++
		}
		m.lastEscTime = now
		if m.escCount >= 3 {
			m.escCount = 0
			m.lastEscTime = time.Time{}
			if !m.inViMode && m.state == StateChat && !m.streaming && !m.agentRunning {
				m.enterViMode()
				return m, nil
			}
		}
	} else if _, ok := msg.(tea.KeyMsg); ok {
		m.escCount = 0
	}

	// ── VI-MODE INTERCEPT: route all key events to the vi-mode handler ──
	if keyMsg, ok := msg.(tea.KeyMsg); ok && m.inViMode {
		return m.handleViModeKey(keyMsg)
	}

	// ── INIT STAGE ROUTING: intercept all key messages during setup ─────
	if m.initStage != initNone && m.initStage != initComplete {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			// Defensive blur: no focused textinput should consume keys
			// during any init stage. Each sub-handler re-focuses as needed.
			m.ti.Blur()
			return m.handleInitKeyMsg(keyMsg)
		}
		// Allow tickMsg to pass through so the spinner continues animating
		// during init stages.
		if _, ok := msg.(tickMsg); ok {
			return m, m.spinnerTickCmd()
		}
		// Allow async result messages to reach their handlers in the main
		// type switch below. Without this, gitInitResultMsg gets swallowed
		// and the init stage never advances after pressing 'Y'.
		switch msg.(type) {
		case tea.WindowSizeMsg, gitInitResultMsg, providerSwitchMsg, graphBuiltMsg:
			// fall through to main type switch
		default:
			return m, nil
		}
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ti.Width = msg.Width - 8

		vpHeight := m.computeVpHeight()

		if !m.Ready {
			m.Viewport = viewport.New(msg.Width, vpHeight)
			m.Ready = true
		} else {
			m.Viewport.Width = msg.Width
			m.Viewport.Height = vpHeight
		}

		if m.streamParser != nil {
			m.streamParser.SetWidth(msg.Width - 2)
		}

		m.refreshViewportContent()
		return m, nil

	case tickMsg:
		// IZEN SAFETY VALVE: force-clear stale review lock after 30s
		// Uses absolute wall-clock comparison (time.Now().Sub) to ensure
		// the timeout cannot be starved or deferred by sequential message
		// stream timing anomalies.
		if m.reviewRunning && !m.lastActionTime.IsZero() && time.Since(m.lastActionTime) > 30*time.Second {
			m.reviewRunning = false
			m.agentRunning = false
			m.agentLabel = ""
			m.agentDone = true
			m.lastActionTime = time.Time{}
			m.sanitizeInputPrompt()
			m.push(roleSystem, mutedStyle.Render("[safety] review action timed out — spinner force-cleared"))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}

		// SPINNER SANITY: if pipeline crashes mid-stream or a boundary
		// refusal was triggered, drop spinnerFrame to 0 immediately so
		// the braille spinner in the status bar shows no residual animation.
		if m.spinnerFrame > 0 && !m.streaming && !m.agentRunning && !m.reviewRunning &&
			m.state != StateProcessing && m.state != StateAwaitingApproval && !m.pipelineRunning {
			m.spinnerFrame = 0
		}

		// DETERMINISTIC RECONCILE: a frozen spinner (frame still advancing,
		// animation requested below) with no owning producer is a leaked
		// state. Force-clear it so the UI can never lock on "✦ streaming…".
		//
		// IDLE-GATE FIX: the leak detector must NOT wipe m.streaming /
		// m.agentRunning on the first ticks before a background worker has had
		// a chance to write a process update. We only reconcile when the agent
		// flag is set but there is NO live stream channel driving it AND no
		// deferred orchestration result is expected (planPending) AND the last
		// recorded agent activity has been idle for at least 15 seconds — this
		// safely catches a genuine long-term hang (e.g. a deadlocked /build
		// handoff) while never freezing the spinner of a legitimate /plan or
		// /investigate worker that owns the flags until its terminal result
		// message arrives.
		const agentHangTimeout = 15 * time.Second
		if m.agentRunning && m.streamCh == nil && !m.planPending && m.state == StateChat &&
			!m.reviewRunning && !m.pipelineRunning &&
			m.state != StateProcessing && m.state != StateAwaitingApproval &&
			!m.lastAgentActivity.IsZero() && time.Since(m.lastAgentActivity) > agentHangTimeout {
			m.reconcileSpinner()
		}

		// ── UNIFIED TICK PATTERN ───────────────────────────────────────────
		// The render loop is driven purely by lightweight boolean flags.
		// While any background operation is in flight we advance the spinner
		// frame, repaint the viewport from its live buffers, and re-dispatch
		// the next tick. When idle we return nil and the loop stops — no
		// custom tick-source ownership, no locks, no deadlock.
		hasActiveWork := m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning ||
			m.state == StateProcessing || m.state == StateAwaitingApproval
		if hasActiveWork {
			// Keep the activity heartbeat fresh while any execution indicator
			// is live. The idle-gate in the reconcile block above relies on
			// this to avoid prematurely force-clearing a healthy spinner.
			m.lastAgentActivity = time.Now()
			// 1. Physically advance the spinner frame.
			m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
			// 2. Repaint the viewport from the live stream/agent buffers.
			if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.state == StateProcessing {
				m.refreshViewportContent()
			}
			// 3. Re-dispatch the tick to keep the render loop alive.
			return m, m.spinnerTickCmd()
		}
		return m, nil

	case agentStartMsg:
		m.agentRunning = true
		m.agentDone = false
		m.agentLabel = msg.label
		m.spinnerFrame = 0
		m.lastAgentActivity = time.Now()
		return m, nil

	case hotfixProgressMsg:
		// Stream a $hot lifecycle log line to the terminal so the developer
		// sees active progress while the LLM generates the patch. Only accept
		// lines while the hotfix is still generating (the proposal/error
		// message clears these flags), preventing stale trailing logs from
		// polluting the approval view.
		if m.agentRunning && m.agentLabel == "hotfix" {
			m.push(roleActivity, msg.Line)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}
		return m, nil

	case agentDoneMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case investigateResultMsg:
		m.lastAgentActivity = time.Now()
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "investigation error: "+msg.err.Error())
			// PERSISTENT NAVIGATION CHIPS (BUG 1): even on failure the user
			// must never be left on a dead viewport. Surface Re-investigate
			// so the diagnostic loop can be retried.
			m.currentResult = investigateResultActions()
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		if msg.sessionKey != "" {
			m.sess.SetInvestigationID(msg.sessionKey)
		}
		// Force-reset streaming middleware flags to guarantee streamCmd can run
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		m.pushRecords(msg.records)
		// Store the raw Context-Ledger data before anything else — this is
		// the authoritative source for handoff, not the LLM's transient output.
		m.handoffLedgerContent = ctxpkg.SanitizeLedger(msg.ledgerContent)

		// Capture the structured forensic ledger so bridgeInvestigationToLedger
		// can inject its findings as sequential, ID-addressed packets into the
		// canonical session.ContextLedger.
		m.lastInvestigateLedger = msg.investigateLedger

		// BRIDGE: project read-only forensic findings into the canonical
		// session.ContextLedger (handoff SSOT) for downstream /plan consumption.
		m.bridgeInvestigationToLedger(m.handoffLedgerContent, msg.err)

		// Populate the handoff context so the /investigate workspace renders
		// its interactive Action Chip ("Formulate Execution Plan" → /plan).
		// Without this the terminal shows the completion notice but no buttons,
		// stranding the user into manually typing /plan.
		if m.handoffLedgerContent != "" {
			m.handoffCtx.ProposedFix = m.handoffLedgerContent
		}

		// SYSTEM BOUNDARY: when the engine already produced a resolved ledger,
		// its structured data IS the output. Re-streaming the escalation as
		// free-form chat leaks conversational fluff to the viewport, so we
		// suppress it and surface only the bounded "ready for /plan" notice
		// plus the Action Chip.
		if m.handoffLedgerContent == "" && msg.escalationContent != "" {
			m.push(roleSystem, "Diagnostics collected. Analyzing...")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, tea.Batch(flush, m.streamCmd(msg.escalationContent))
		}

		if len(msg.records) == 0 {
			m.push(roleSystem, "Investigation complete — no structured findings to report.")
		}
		m.push(roleStatus, "Investigation complete. Context-Ledger ready for /plan handoff.")
		// PERSISTENT NAVIGATION CHIPS (BUG 1): always populate the navigation
		// controls so the user is never left with a dead viewport after a fast
		// (cached) transition. "📋 Plan Solution" submits /plan against the
		// structured diagnostic payload; "🔄 Re-investigate" re-runs /investigate.
		m.currentResult = investigateResultActions()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case planResultMsg:
		// Terminal handler for the asynchronous PlanEngine synthesis. Only here
		// do we stage tasks and clear streaming state — never while the LLM call
		// is in flight (that would re-block the event loop).
		m.planPending = false
		m.planStartedAt = time.Time{}

		// ALWAYS clear the transient loading flags first so the spinner can
		// never freeze, regardless of which branch below we take.
		m.reconcileSpinner()

		if msg.Err != nil {
			m.push(roleError, fmt.Sprintf("Failed to synthesize plan from ledger: %v", msg.Err))
			// Retain a baseline Action Chip so the user is never left with a
			// dead viewport and no buttons — they can re-investigate the failure.
			m.currentResult = failureResult(m.handoffLedgerContent)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		if len(msg.Tasks) == 0 {
			// Deterministic fallback: a handoff that yields zero constructive
			// tasks must immediately clear the view-model flags rather than
			// leave the UI frozen on the spinner. We still surface a baseline
			// Action Chip (Investigate Root Cause) so the terminal stays alive.
			m.push(roleError, "plan synthesis produced zero tasks — investigation data may be insufficient")
			m.currentResult = failureResult(m.handoffLedgerContent)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		m.sess.StageTaskList(&msg.Tasks)
		// BRIDGE: mirror the structured /plan queue into the canonical
		// session.ContextLedger as []AtomicTask — the SSOT /build consumes.
		m.bridgePlanToLedger(msg.Tasks)
		m.handoffCtx.PendingTodos = make([]string, len(msg.Tasks))
		for i, t := range msg.Tasks {
			m.handoffCtx.PendingTodos[i] = t.Type + ": " + t.Target + " — " + t.Description
		}
		m.push(roleStatus, fmt.Sprintf("Plan staged: %d task(s). Use /build to execute.", len(msg.Tasks)))
		// Render the staged task list into the viewport so the developer can
		// see exactly what /build will execute.
		var tb strings.Builder
		tb.WriteString("## STAGED EXECUTION PLAN\n")
		for i, t := range msg.Tasks {
			fmt.Fprintf(&tb, "%d. [%s] %s — %s\n", i+1, t.Type, t.Target, t.Description)
		}
		m.push(roleStatus, tb.String())
		if m.buildLedger == nil {
			m.buildLedger = ctxpkg.NewTaskLedger()
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case graphBuiltMsg:
		m.agentRunning = false
		m.sanitizeInputPrompt()
		if msg.err == nil && msg.graph != nil {
			m.graph = msg.graph
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case reviewResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "review error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.pushRecords(msg.records)
		if msg.sessionKey != "" {
			m.sess.SetReviewID(msg.sessionKey)
		}
		if msg.saveReportFn != nil {
			msg.saveReportFn()
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case testResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.lastTestOutput = msg.output
		m.lastTestFailed = !msg.passed
		m.lastTestTarget = ""
		if msg.err != nil {
			m.push(roleError, "test execution error: "+msg.err.Error())
		}
		if msg.output != "" {
			for _, line := range strings.Split(msg.output, "\n") {
				if line == "" {
					continue
				}
				role := roleSystem
				if strings.Contains(line, "FAIL") || strings.Contains(line, "error") {
					role = roleError
				} else if strings.Contains(line, "PASS") || strings.Contains(line, "ok") {
					role = roleStatus
				}
				m.push(role, line)
			}
		}
		statusLine := fmt.Sprintf("tests: %d total, %d failed", msg.total, msg.failed)
		if msg.passed {
			statusLine = greenStyle.Render("✓ all tests passed (" + strconv.Itoa(msg.total) + ")")
		} else {
			statusLine = redStyle.Render("✗ " + statusLine)
		}
		m.push(roleSystem, infoStyle.Render(statusLine))

		// ── Handoff: Capture failure context for mode pipeline ────────────
		if !msg.passed && msg.output != "" {
			m.handoffCtx.LastFailurePayload = msg.output
			m.handoffCtx.TargetScope = m.lastTestTarget
			// Expose the failure as a workflow result: the capability to
			// investigate its root cause is now available for the current
			// view. Cleared on mode entry, so it never persists as a stale
			// chip. A passing run clears any prior failure result.
			m.currentResult = failureResult(msg.output)
		} else if msg.passed {
			m.currentResult = nil
		}

		// ── Build verification: post-mutation test auto-result ───────────
		if m.buildVerifyPending {
			m.buildVerifyPending = false

			// ── Automated error recovery loop ───────────────────────────
			// When verification fails after a build patch, silently trigger
			// a recovery cycle: re-read the file, re-generate a corrected
			// AST block, and re-apply — without producing any UI chatter.
			//
			// RULE B: If the failure is caused by missing Go modules (e.g.,
			// "no required module provides package" or "to add it: go get"),
			// halt the recovery loop immediately. Do NOT waste recovery
			// attempts on import hallucinations — the .go imports are valid,
			// the dependency just needs to be fetched.
			if !msg.passed && m.resolver.Current() == modes.ModeBuild &&
				m.buildRecoveryCount < maxBuildRecoveryAttempts {
				if hasMissingModuleError(msg.output) {
					m.acceptAll = false
					m.push(roleError, "[BUILD HALTED] Build failed due to missing Go module dependency. Auto-recovery cannot fix import paths. Run 'go get <package>' manually, then retry.")
				} else {
					m.buildRecoveryCount++
					m.acceptAll = true
					m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
						"⚙ [recovery %d/%d] auto-correcting compilation errors...",
						m.buildRecoveryCount, maxBuildRecoveryAttempts)))
					flush := m.flushPendingRecords()
					return m, tea.Batch(flush, m.runFixCmd(""))
				}
			}

			// If recovery exhausted or verification passed, expose the
			// corresponding workflow result (commit / rollback).
			if m.buildRecoveryCount >= maxBuildRecoveryAttempts {
				m.acceptAll = false
			}
			m.currentResult = buildVerifyResult(msg.passed)

			// ── FAIL-FAST MACHINE: mirror outcome into the canonical
			// session.ContextLedger. On a hard failure (recovery exhausted or
			// missing-module halt) the active task is marked Failed and the
			// queue is frozen — no subsequent task is advanced, leaving the
			// workspace in its broken state for developer inspection.
			m.bridgeBuildResultToLedger(m.currentBuildTaskID, msg.passed, msg.output)
			if !msg.passed && m.resolver.Current() == modes.ModeBuild {
				if m.buildRecoveryCount >= maxBuildRecoveryAttempts || hasMissingModuleError(msg.output) {
					m.push(roleError, fmt.Sprintf(
						"[BUILD HALTED] Step %d failed verification. Queue frozen — %d/%d task(s) complete. Inspect and fix, then re-run /build.",
						m.currentBuildTaskID, m.countCompletedLedgerTasks(), len(m.sess.CurrentTasks)))
				} else if m.buildRecoveryCount < maxBuildRecoveryAttempts {
					// Soft failure within recovery budget: ledger still marks
					// the attempt, but the auto-recovery cycle continues.
					m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
						"Step %d verification failed — entering auto-recovery (attempt %d/%d).",
						m.currentBuildTaskID, m.buildRecoveryCount, maxBuildRecoveryAttempts)))
				}
			}
			if m.resolver.Current() == modes.ModeBuild {
				m.push(roleSystem, "Build verification complete.")
			}
		}

		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case hotfixProposalMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()

		// ── HOTFIX PROPOSAL FAILURE ───────────────────────────────────
		// Patch generation failed: surface the error, abort the hotfix, and
		// restore the stashed plan so the pipeline returns to PAUSED cleanly.
		if msg.Err != nil {
			m.push(roleError, "[HOTFIX] Patch generation failed: "+msg.Err.Error())
			m.hotfixActive = false
			if stashedTasks, rerr := m.restorePlan(); rerr == nil && len(stashedTasks) > 0 {
				m.sess.StageTaskList(&stashedTasks)
				_ = m.sess.Save()
			}
			m.push(roleSystem, infoStyle.Render("[HOTFIX] Pipeline PAUSED. No files were modified."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, m.flushPendingRecords()
		}

		// ── CRITICAL: freeze and request authorization (Bug Fix 2) ─────
		// Store the synthesized patch + rendered diff proposal. Enter the
		// StateAwaitingApproval approval gate so the developer can inspect the
		// code diff and explicitly approve (y) or reject (n) BEFORE any change
		// is written to disk.
		m.pendingHotfixTask = msg.Task
		m.pendingHotfixPatch = msg.Patch

		// Render the diff through the standard proposal dock (MutationRenderer),
		// exactly like a normal /build file-mutation proposal.
		target := msg.Task.Target
		proposal := SemanticProposal{
			ID:   msg.Patch.ID,
			Diff: msg.Diff,
			Target: SemanticTarget{
				QualifiedName: target,
				Module:        filepath.Dir(target),
				Language:      langFromPath(target),
			},
			Expanded: true,
		}
		m.pendingProposals = []SemanticProposal{proposal}

		// ── CLEAN TRANSITION TO PROPOSAL VIEW (Feature) ──────────────
		// Emit the final lifecycle log then swap the pane into the
		// MutationRenderer diff view. The spinner/transient progress lines are
		// superseded by the explicit approval prompt below.
		m.push(roleActivity, "  ⚙ Compiling unified diff schema...")

		m.state = StateAwaitingApproval
		m.ti.Blur()
		m.recalcViewportHeight()

		m.push(roleStatus, fmt.Sprintf(
			"[HOTFIX APPROVAL] Proposed patch to %s", target))
		m.push(roleSystem, infoStyle.Render(
			"Review the code diff below. Apply this patch? (y/n): "))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil

	case buildResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.lastTestOutput = msg.output
		m.lastTestFailed = msg.exitCode != 0

		// ── FIX 1: Flush prompt buffer on task failure ────────────────
		// Wipe the volatile user input cache so the next keystroke or
		// command is parsed as a brand-new clean request, rather than
		// appending to the failed context. This prevents the prompt buffer
		// from getting stuck on historical commands.
		if msg.exitCode != 0 {
			m.ti.SetValue("")
			m.syncInputFromTI()
			m.input.Reset()
			m.currentPrompt = ""
			m.responseBuffer.Reset()
			m.currentStreamContent = ""
			m.streamBuffer = ""
			m.historyIndex = -1
		}

		if msg.err != nil {
			m.push(roleError, "build execution error: "+msg.err.Error())
		}
		if msg.output != "" {
			for _, line := range strings.Split(msg.output, "\n") {
				if line == "" {
					continue
				}
				m.push(roleSystem, line)
			}
		}
		if msg.exitCode == 0 {
			m.push(roleSystem, infoStyle.Render("Execution successful."))
		} else {
			m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Execution failed (exit %d).", msg.exitCode)))
		}

		// ── $hot HOTFIX: restore stashed plan AFTER hotfix ────────────
		// The hotfix lifecycle is fully contained here:
		//   1. Rollback any hotfix mutations on failure.
		//   2. Restore the stashed plan deterministically (Go-level, no LLM).
		//   3. Mark the pipeline PAUSED — no auto-advance, no stalled-marking.
		//   4. Return early, cutting off all fall-through execution paths.
		//
		// This prevents the restored plan's pending tasks from being mistaken
		// as the active pipeline's next steps (which would trigger automatic
		// re-execution of previously rejected SHELL_EXEC tasks).
		if m.hotfixActive {
			// Rollback on failure (no-op if no transaction was started).
			if msg.exitCode != 0 && m.execEng != nil {
				if errs := m.execEng.RollbackTransaction(); len(errs) > 0 {
					for _, err := range errs {
						m.push(roleError, fmt.Sprintf("build rollback error: %v", err))
					}
				}
			}

			// Restore the stashed plan deterministically.
			if stashedTasks, err := m.restorePlan(); err == nil {
				if len(stashedTasks) > 0 {
					m.sess.StageTaskList(&stashedTasks)
					_ = m.sess.Save()
				}
			} else {
				m.push(roleError, fmt.Sprintf("[HOTFIX] Failed to restore stashed plan: %v", err))
			}
			m.hotfixActive = false

			// Pipeline is PAUSED — the restored plan is frozen until the
			// user explicitly types "run" or provides feedback.
			m.push(roleSystem, infoStyle.Render("[HOTFIX] Stashed plan restored successfully. Pipeline PAUSED."))

			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		// ── FIX 2: Freeze state machine on task failure ───────────────
		// If a step fails, the overall plan status must be STALLED. It is
		// strictly forbidden to advance the internal task index pointer.
		// All remaining idle tasks are marked "stalled" so subsequent
		// /build invocations see them blocked rather than silently
		// advancing into corrupted state.
		if msg.exitCode != 0 {
			// ── ROLLBACK ON FAILURE ─────────────────────────────────
			// Any disk mutations performed during this build execution
			// are rolled back so the workspace is never left in a broken
			// state. The transaction is then reset for the next attempt.
			if m.execEng != nil {
				if errs := m.execEng.RollbackTransaction(); len(errs) > 0 {
					for _, err := range errs {
						m.push(roleError, fmt.Sprintf("build rollback error: %v", err))
					}
				}
			}

			// ── CLEAR DIALOG BUFFER ON TASK FAILURE ────────────────
			// Wipe the LLM conversation history so the next diagnostic
			// or restart prompt starts with a clean context scope, never
			// appending to stale failed-task history.
			if m.sess != nil {
				m.sess.ClearHistory()
				_ = m.sess.Save()
			}

			tasks := m.sess.CurrentTasks
			changed := false
			for i := range tasks {
				if tasks[i].Status == "idle" {
					tasks[i].Status = "stalled"
					changed = true
				}
			}
			if changed {
				m.sess.StageTaskList(&tasks)
				_ = m.sess.Save()
			}
			m.push(roleError, fmt.Sprintf(
				"[BUILD HALTED] Step %d failed. Queue frozen — remaining tasks marked stalled. Use /investigate or /plan to re-generate a valid ledger.",
				m.currentBuildTaskID))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		// After a SHELL_EXEC step finishes successfully, advance to the
		// next idle task so the build queue makes progress automatically.
		// When a $hot hotfix succeeds, the RESTORED plan is checked for
		// remaining work — the original execution flow resumes seamlessly.
		hasNext := false
		for _, t := range m.sess.CurrentTasks {
			if t.Status == "idle" || t.Status == "processing" {
				hasNext = true
				break
			}
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		if hasNext && m.resolver.Current() == modes.ModeBuild {
			return m, tea.Batch(flush, m.handleBuildRun(0))
		}
		return m, flush

	case logInputMsg:
		m.agentRunning = false
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.reviewRunning = false
			m.agentDone = true
			m.agentLabel = ""
			m.lastActionTime = time.Time{}
			m.pipelineRunning = false
			m.push(roleError, "$log: error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		return m, m.handleLogInput(msg)

	case investigateCompleteMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.pipelineRunning = false
			m.push(roleError, "silent analysis error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Analysis complete [%s].", msg.ledgerID)))
		return m, m.handleInvestigateComplete(msg)

	case blueprintReadyMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.pipelineRunning = false
			m.push(roleError, "blueprint error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		return m, m.handleBlueprintReady(msg)

	case fixResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "fix error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.push(roleSystem, "Analyzing failure...")
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		flush := m.flushPendingRecords()
		return m, tea.Batch(flush, m.streamCmd(msg.content))

	case envResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "env diagnostics error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		// Prepend env diagnostics to LastFailurePayload for cumulative forensic data
		if m.handoffCtx.LastFailurePayload != "" {
			m.handoffCtx.LastFailurePayload = msg.content + "\n" + m.handoffCtx.LastFailurePayload
		} else {
			m.handoffCtx.LastFailurePayload = msg.content
		}
		// env diagnostics carry a failure into the current view; expose it as
		// a workflow result so the investigate capability is available now.
		m.currentResult = failureResult(msg.content)
		m.push(roleSystem, msg.content)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case traceResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "trace execution error: "+msg.err.Error())
		}

		// Token optimization: truncate middle if output exceeds 4000 chars
		output := msg.output
		if len(output) > 4000 {
			top := output[:2000]
			bottom := output[len(output)-2000:]
			output = top + "\n... [TRUNCATED " + strconv.Itoa(len(msg.output)-4000) + " bytes] ...\n" + bottom
		}

		if output != "" {
			for _, line := range strings.Split(output, "\n") {
				if line == "" {
					continue
				}
				role := roleSystem
				if strings.Contains(line, "FAIL") || strings.Contains(line, "error") || strings.Contains(line, "panic") || strings.Contains(line, "WARNING: DATA RACE") {
					role = roleError
				} else if strings.Contains(line, "PASS") || strings.Contains(line, "ok") {
					role = roleStatus
				}
				m.push(role, line)
			}
		}

		// Pipe execution log into handoff context for $diagnose
		m.handoffCtx.LastFailurePayload = msg.output
		m.handoffCtx.TargetScope = msg.target
		// A trace that produced output exposes a failure result whose
		// investigate capability is available for the current view.
		m.currentResult = failureResult(msg.output)

		statusLine := fmt.Sprintf("trace: %d total, %d failed — target %q", msg.total, msg.failed, msg.target)
		if msg.passed {
			statusLine = greenStyle.Render("✓ trace passed (" + strconv.Itoa(msg.total) + ") — " + msg.target)
		} else {
			statusLine = redStyle.Render("✗ " + statusLine)
		}
		m.push(roleSystem, infoStyle.Render(statusLine))

		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case diagnoseResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "diagnosis error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		// ── FAIL-SAFE: Investigate mode diagnostic is read-only stream ──
		// The diagnostic content is piped through the LLM stream for analysis
		// output. No patches or mutations are ever applied here.
		m.push(roleSystem, "[System] Running deep root cause analysis on qwen2.5-coder with forensic evidence...")
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		flush := m.flushPendingRecords()
		return m, tea.Batch(flush, m.streamCmd(msg.content))

	case commitGeneratedMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.pipelineRunning = false
		m.sanitizeInputPrompt()

		if msg.err != nil {
			m.push(roleError, "commit error: "+msg.err.Error())
		} else {
			result := fmt.Sprintf("Commit: %s · %s", msg.hash, msg.subject)
			m.push(roleSystem, successBannerStyle.Render("[✓] "+result))
		}

		_ = m.sess.Save()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case objectiveAnalyzedMsg:
		if msg.err != nil {
			m.uiNotice = "Objective analysis failed: " + msg.err.Error()
			if m.sess.ObjectiveState != nil {
				m.sess.ObjectiveState.CurrentStatus = domain.ObjectiveIdle
				m.sess.SetObjectiveState(m.sess.ObjectiveState)
				_ = m.sess.Save()
			}
			return m, nil
		}
		if msg.objective == nil {
			m.uiNotice = "Objective analysis failed: empty objective result."
			return m, nil
		}
		m.sess.SetObjectiveState(msg.objective)
		_ = m.sess.Save()
		if msg.objective.TokenBudget.RequiresApproval {
			m.uiNotice = "Objective needs manual approval. Run /objective approve."
		} else {
			m.uiNotice = "Objective planned and active."
		}
		return m, nil

	case archDoneMsg:
		for _, line := range strings.Split(msg.Content, "\n") {
			m.push(roleSystem, infoStyle.Render(line))
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil

	case mutationResultMsg:
		if msg.err != nil {
			m.setApplyError("apply failed: " + msg.err.Error())
		} else {
			m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
				Target: msg.file,
				Status: msg.status,
			})
		}

		if len(m.pendingProposals) > 0 {
			m.pendingProposals = m.pendingProposals[1:]
		}
		m.proposalDiffOffset = 0

		if len(m.pendingProposals) == 0 {
			m.ti.Focus()
			m.state = StateChat
			m.recalcViewportHeight()
			m.awaitingConfirmation = false
			m.acceptAll = false
			if msg.err == nil {
				// ── COMMIT TRANSACTION ─────────────────────────────────
				// All mutations approved and applied — clear the snapshot
				// so the workspace is no longer pinned to the rollback point.
				if m.execEng != nil {
					m.execEng.CommitTransaction()
				}

				outcomeLine := fmt.Sprintf("%s %s • %s", successBannerStyle.Render("[✓]"), msg.file, msg.status)
				m.push(roleSystem, outcomeLine)
				m.createBuildCheckpoint(1)
				// Handoff: unbuffered build verification test
				if m.resolver.Current() == modes.ModeBuild {
					m.buildVerifyPending = true
					m.refreshViewportContent()
					m.push(roleSystem, "Verifying build...")
					flush := m.flushPendingRecords()
					return m, tea.Batch(flush, m.runTestEngine("./..."))
				}
			} else {
				m.push(roleSystem, failureBannerStyle.Render("[✗] "+msg.file+" — "+msg.err.Error()))
			}
		} else {
			m.state = StateAwaitingApproval
			m.recalcViewportHeight()
			m.Viewport.Height = m.computeVpHeight()
			m.refreshViewportContent()
		}

		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		return m, flush

	case applyAllResultMsg:
		applied := 0
		failed := 0
		for _, r := range msg.results {
			if r.err != nil {
				m.setApplyError("apply failed: " + r.err.Error())
				failed++
				continue
			}
			m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
				Target: r.file,
				Status: r.status,
			})
			applied++
		}
		m.pendingProposals = nil
		m.awaitingConfirmation = false
		m.acceptAll = false
		m.ti.Focus()
		m.state = StateChat
		m.recalcViewportHeight()
		var testCmd tea.Cmd
		switch {
		case applied > 0 && failed == 0:
			// ── COMMIT TRANSACTION ─────────────────────────────────
			// All mutations approved and applied — clear the snapshot.
			if m.execEng != nil {
				m.execEng.CommitTransaction()
			}

			summary := fmt.Sprintf("%s %d file(s) mutated. Checkpoint created.", successBannerStyle.Render("[✓]"), applied)
			m.push(roleSystem, summary)
			m.createBuildCheckpoint(applied)
			if m.resolver.Current() == modes.ModeBuild {
				m.buildVerifyPending = true
				m.push(roleSystem, "Verifying build...")
				testCmd = m.runTestEngine("./...")
			}
		case applied > 0:
			summary := fmt.Sprintf("%s %d mutated, %d failed.", warningBannerStyle.Render("[!]"), applied, failed)
			m.push(roleSystem, summary)
			m.createBuildCheckpoint(applied)
			if m.resolver.Current() == modes.ModeBuild {
				m.buildVerifyPending = true
				m.push(roleSystem, "Verifying build...")
				testCmd = m.runTestEngine("./...")
			}
		default:
			m.push(roleSystem, failureBannerStyle.Render(fmt.Sprintf("[✗] %d mutation(s) failed.", failed)))
		}
		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		if testCmd != nil {
			return m, tea.Batch(flush, testCmd)
		}
		return m, flush

	case shellOutputMsg:
		for _, line := range msg.lines {
			m.push(roleSystem, line)
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case smoothStreamTickMsg:
		// ── PHYSICALLY ADVANCE THE SPINNER FRAME ───────────────────────────
		// This 20ms smooth-tick loop drives token-stream rendering, but it is
		// ALSO the only tick loop dispatched for background ops that produce no
		// token stream — notably /plan synthesis (runPlanEngineCmd returns one
		// terminal planResultMsg, never tokens). Previously this handler shifted
		// the (empty) stream buffer but never touched m.spinnerFrame, so the
		// braille spinner (rendered as ProposalSpinnerFrames[spinnerFrame]) was
		// physically frozen on frame 0 (⠋) for the entire synthesis — the
		// classic "the spinner is stuck, the UI looks dead" report. Only the
		// 100ms tickMsg handler advanced the frame, and that loop is never
		// started for /plan. Advance the frame here too whenever a background
		// producer owns the flags, so the indicator animates regardless of
		// which tick loop is live. Throttled to ~100ms cadence so the animation
		// speed matches the tickMsg loop and 20ms token pacing stays smooth.
		// Keep the tick loop ALIVE for the entire duration of any background
		// op — including /plan synthesis, where m.streaming stays false and the
		// only other thing driving the event loop is the pending planResultMsg
		// from the background goroutine. If that goroutine hangs (unresponsive
		// local model), a dead tick loop starves the whole event loop: no
		// re-renders, no Ctrl+C responsiveness, no slow-notice — the UI appears
		// frozen. So the loop must keep self-scheduling whenever a background
		// producer still owns the flags.
		backgroundActive := m.streaming || m.agentRunning || m.reviewRunning ||
			m.pipelineRunning || m.planPending
		if backgroundActive {
			m.lastAgentActivity = time.Now()
			if time.Since(m.lastSpinnerAdvance) >= 100*time.Millisecond {
				m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
				m.lastSpinnerAdvance = time.Now()
			}
		}

		if len(m.streamBuffer) > 0 {
			// Emit word-aligned chunks for a natural reading rhythm.
			emit := 0
			minChars := 3
			for i, c := range m.streamBuffer {
				if i >= minChars && (c == ' ' || c == '\n') {
					emit = i + 1
					break
				}
			}
			if emit == 0 {
				emit = len(m.streamBuffer)
			}
			if emit > 80 {
				emit = 80
			}
			m.currentStreamContent += m.streamBuffer[:emit]
			m.streamBuffer = m.streamBuffer[emit:]
		}

		// Refresh viewport with streaming content.
		if m.Ready {
			m.refreshViewportContent()
			// Only auto-scroll to bottom if the user hasn't explicitly
			// scrolled up — respects user-inspect position during streaming.
			if m.streaming && !m.userIsScrollingUp {
				m.Viewport.GotoBottom()
			}
		}

		// Re-schedule the tick loop as long as ANY background producer owns
		// the flags. During /plan synthesis m.streaming is false and the stream
		// buffer is empty, so gating only on those would let the loop die and
		// starve the event loop (frozen UI). m.planPending / m.agentRunning are
		// the authoritative "a background op is still in flight" signals.
		if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.planPending {
			m.streamTickActive = true
			return m, m.smoothStreamTickCmd()
		}
		// Streaming complete
		m.streamTickActive = false
		return m, nil

	case planSlowNoticeMsg:
		// One-shot soft-timeout probe for /plan synthesis. Only act if THIS
		// synthesis is still pending (guard against a stale probe from a prior
		// run and against a synthesis that already resolved). This is purely
		// informational — it never cancels the 120s hard-timeout work, and it
		// is surfaced through the viewport (m.push), never a raw terminal print
		// that would corrupt the alt-screen frame.
		if m.planPending && msg.startedAt.Equal(m.planStartedAt) {
			m.push(roleSystem, mutedStyle.Render(fmt.Sprintf(
				"[timeout] LLM provider still synthesizing after %s — the local model may be unresponsive; check your model status. Ctrl+C to cancel.",
				planSlowNoticeDelay)))
			m.refreshViewportContent()
			if !m.userIsScrollingUp {
				m.Viewport.GotoBottom()
			}
			return m, m.flushPendingRecords()
		}
		return m, nil

	case tokenMsg:
		// LOCK-FREE CONSUMER: this per-token handler MUST NOT acquire any
		// ContextLedger / TaskLedger mutex. It only appends to local buffers
		// and schedules the next read. The ledger is committed once, at EOF,
		// by the streamDoneMsg handler below. Holding a ledger lock here would
		// serialize the stream against the renderer and reproduce the
		// 108-token freeze.
		raw := string(msg)
		m.responseBuffer.WriteString(raw)
		m.streamBuffer += raw
		if m.streamParser != nil {
			m.streamParser.ProcessChunk(raw)
		}
		var cmds []tea.Cmd
		cmds = append(cmds, m.readStream())
		if !m.streamTickActive {
			m.streamTickActive = true
			cmds = append(cmds, m.smoothStreamTickCmd())
		}
		// Keep cursor blink alive during streaming
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		cmds = append(cmds, tiCmd)
		return m, tea.Batch(cmds...)

	case streamDoneMsg:
		m.streamCh = nil
		m.streaming = false
		m.streamCancel = nil

		if m.streamParser != nil {
			m.streamParser.Flush()
			m.streamParser = nil
		}

		// Flush any remaining buffered stream content
		if m.streamTickActive {
			m.currentStreamContent += m.streamBuffer
			m.streamBuffer = ""
			m.streamTickActive = false
		}

		if m.sess.ObjectiveState != nil && m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveExecuting {
			m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
		}
		m.InputTokens += msg.tokenInput
		m.OutputTokens += msg.tokenOutput
		m.TotalTokens = m.InputTokens + m.OutputTokens

		// Use accumulated stream content as the canonical final text.
		// This avoids any race between async printing and the View cycle.
		final := m.currentStreamContent
		if final == "" {
			final = msg.content
		}
		if final == "" {
			final = m.responseBuffer.String()
		}
		m.responseBuffer.Reset()
		m.currentStreamContent = ""

		// Sanitize: strip tool execution artifacts so they don't pollute
		// the downstream JSON parser or render pipeline. Lines matching
		// telemetry/error markers are removed from leading/trailing context.
		final = sanitizeFinalContent(final)

		// Append the completed turn to PreRenderedHistory and freeze state.
		m.push(roleAI, final)

		// ── IMPLICIT PIPELINE INTERCEPT: pipe stream output to next step ──
		if m.pipelineRunning {
			if m.pipelineStep == "analyzing failure" || m.pipelineStep == "analyzing trace" {
				// Step 1 complete → silently pipe analysis into plan blueprinting
				m.pipelineStep = "blueprinting"
				m.push(roleSystem, infoStyle.Render("Step 2/3: Generating blueprint..."))
				m.handoffCtx.ProposedFix = final

				var planCtx strings.Builder
				planCtx.WriteString("## ANALYSIS OUTPUT\n\n")
				planCtx.WriteString(final)
				planCtx.WriteString("\n\n## INSTRUCTION\n")
				planCtx.WriteString("Based on the analysis above, produce a precise execution plan with Markdown code ")
				planCtx.WriteString("diff blocks or complete file replacements for each fix. Output the plan as a structured ")
				planCtx.WriteString("task list with file targets and descriptions.\n")

				flush := m.flushPendingRecords()
				m.streamCh = nil
				m.streaming = false
				m.streamParser = nil
				return m, tea.Batch(flush, m.streamCmd(planCtx.String()))
			}

			if m.pipelineStep == "blueprinting" && final != "" {
				// Step 2 complete → blueprint is ready, but auto-build is blocked
				pipelineID := ""
				if m.ledger != nil {
					pipelineID = fmt.Sprintf("#%d", m.ledger.ActiveID)
				}
				m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Pipeline complete [%s].", pipelineID)))
				flush := m.flushPendingRecords()
				return m, tea.Batch(flush, func() tea.Msg {
					return blueprintReadyMsg{blueprint: final, ledgerID: pipelineID}
				})
			}
		}

		// ── Handoff: Capture ProposedFix from investigate mode ──────────
		// The "Formulate Execution Plan" capability is derived from
		// handoffCtx.ProposedFix in BuildViewContext; no UI cache to refresh.
		//
		// DATA HIERARCHY (Context-Ledger > Transaction Cache):
		// 1. handoffLedgerContent (structured Context-Ledger from the
		//    investigate engine's FormatLedgerForPlan) — authoritative SSOT.
		// 2. LastFailurePayload (raw compilation errors / test output).
		// 3. LLM output (final) — transient Transaction Cache, used only
		//    as a last resort when all structured sources are empty.
		// This prevents context poisoning where /plan receives a generic
		// greeting instead of actual engineering diagnostics.
		if m.resolver.Current() == modes.ModeInvestigate && final != "" {
			switch {
			case m.handoffLedgerContent != "":
				m.handoffCtx.ProposedFix = m.handoffLedgerContent
			case m.handoffCtx.LastFailurePayload != "" && IsGenericGreeting(final):
				m.handoffCtx.ProposedFix = m.handoffCtx.LastFailurePayload
			default:
				m.handoffCtx.ProposedFix = final
			}
		}

		// ── Auto-transition: investigate → build on mutation detection ──
		// When a read-only analysis ($diagnose, $test) in investigate mode
		// concludes with a concrete mutation proposal (code blocks with
		// language annotations), automatically transition to /build and
		// initiate the fix pipeline. This eliminates the manual handoff step.
		//
		// STRICT GUARD: If the last compilation state contains [build failed]
		// or any AST/syntax errors, the agent is strictly prohibited from
		// jumping directly to /build. Instead it must route to /plan for
		// structured recovery (Sanitization → Package Isolation → Atomic Fixes).
		if m.resolver.Current() == modes.ModeInvestigate && m.handoffCtx.ProposedFix != "" {
			if containsMutationIntention(m.handoffCtx.ProposedFix) {
				// ── Compile failure guard ────────────────────────────────
				compileFailure := detectCompileFailure(m.handoffCtx.LastFailurePayload)
				if !compileFailure && m.lastTestOutput != "" {
					compileFailure = detectCompileFailure(m.lastTestOutput)
				}

				if compileFailure {
					// ── FORCED ROUTING TO /plan ──────────────────────────
					// Structural errors require a deliberate design phase
					// before any patching is attempted.
					// Save handoff data and clear auto-trigger sources so
					// setMode does NOT double-fire the plan engine.
					savedLastFailure := m.handoffCtx.LastFailurePayload
					savedProposedFix := m.handoffCtx.ProposedFix
					m.handoffCtx.ProposedFix = ""
					m.handoffCtx.LastFailurePayload = ""
					m.handoffLedgerContent = ""

					m.push(roleSystem, warningBannerStyle.Render(
						"[!] Compile failure detected — routing to /plan for structured recovery."))
					m.push(roleSystem, infoStyle.Render(
						"Recovery checklist: Sanitization → Package Isolation → Atomic Fixes."))
					m.setMode(modes.ModePlan)

					recoveryPrompt := "## STRUCTURED RECOVERY CHECKLIST\n\n" +
						"The codebase has compilation errors. Create a structured recovery plan with:\n\n" +
						"### 1. Sanitization\n" +
						"- Identify and document all syntax/type errors in the compilation output\n" +
						"- Do NOT propose blind patches — first understand the full scope of breakage\n\n" +
						"### 2. Package Isolation\n" +
						"- Group related errors by package/file\n" +
						"- Determine dependency order for fixes\n\n" +
						"### 3. Atomic Fixes\n" +
						"- For each error, specify the minimal corrective change\n" +
						"- Output each fix as a separate task with file target and description\n\n" +
						"### Compilation Errors\n```\n" +
						savedLastFailure +
						"\n```\n" +
						"### Root Cause Analysis\n" +
						CleanHandoffPayload(savedProposedFix)

					m.currentPrompt = recoveryPrompt
					m.streamCh = nil
					m.streaming = false
					m.streamParser = nil
					flush := m.flushPendingRecords()
					m.refreshViewportContent()
					return m, tea.Batch(flush, m.streamCmd(recoveryPrompt))
				}

				// ── HANDOFF DATA PRESERVATION ───────────────────────────
				// ProposedFix is intentionally kept intact so the Action Chip
				// remains available for the user to manually trigger the
				// investigate → plan transition via the workspace. The user
				// has full agency to click the chip or type /plan manually;
				// the auto-trigger in setMode will handle the execution.
				m.push(roleSystem, infoStyle.Render(
					"Mutation proposals detected. Use the capability chip or /plan to formulate an execution plan."))
				flush := m.flushPendingRecords()
				m.refreshViewportContent()
				return m, flush
			}
		}

		// ── Handoff: Capture PendingTodos from plan mode ────────────────
		// The "Execute & Verify Patch" capability is derived from
		// handoffCtx.PendingTodos in BuildViewContext; no UI cache to refresh.
		if m.resolver.Current() == modes.ModePlan && final != "" {
			m.handoffCtx.PendingTodos = extractTodosFromPlan(final)
		}

		// SECTION 1: INTERCEPTING STREAM COMPLETION
		promptText := m.currentPrompt
		if promptText != "" {
			// Memory Context Update: Store the assistant reply in the sliding
			// window. The user message was already committed synchronously at
			// submit time (handleInput) to guarantee the model's context window
			// leads the API dispatch — so we append ONLY the assistant turn here
			// to avoid duplicating the user turn.
			m.sess.AddMessage("assistant", final, 5)

			// Securely commit session.json to disk
			if err := m.sess.Save(); err != nil {
				m.push(roleError, fmt.Sprintf("failed to save session: %v", err))
			}

			// History Stream (mutable, resettable on rollback): Write to history/input.log
			if err := session.WriteToHistoryLog(".", "user", promptText); err != nil {
				m.push(roleError, fmt.Sprintf("History Log Failure: %v", err))
			}
			if err := session.WriteToHistoryLog(".", "assistant", final); err != nil {
				m.push(roleError, fmt.Sprintf("History Log Failure: %v", err))
			}

			// Audit Trail (immutable): Log mutations if build mode
			if m.resolver.Current() == modes.ModeBuild || m.resolver.Current() == modes.ModeInvestigate {
				auditEntry := struct {
					Timestamp string `json:"timestamp"`
					Role      string `json:"role"`
					Mode      string `json:"mode"`
					Preview   string `json:"preview"`
				}{
					Role:    "assistant",
					Mode:    m.resolver.Current().String(),
					Preview: truncateString(final, 200),
				}
				data, _ := json.Marshal(auditEntry)
				data = append(data, '\n')
				auditPath := filepath.Join(".izen", "audit", "mutations.log")
				if f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
					_, _ = f.Write(data)
					_ = f.Close()
				}
			}

			// Clear cached prompt after use
			m.currentPrompt = ""
		}

		delta := msg.tokenInput + msg.tokenOutput
		m.IsCloudModel = m.cfg.ActiveProviderName() != "ollama"
		costStr := "$free"
		if m.IsCloudModel {
			turnCost := float64(msg.tokenInput)*(3.0/1_000_000) + float64(msg.tokenOutput)*(15.0/1_000_000)
			m.AccumulatedCost += turnCost
			costStr = fmt.Sprintf("$%.4f", turnCost)
		}
		latencySec := 0.0
		if !m.streamStartTime.IsZero() {
			latencySec = time.Since(m.streamStartTime).Seconds()
			m.streamStartTime = time.Time{}
		}
		m.push(roleStatus, dimmedStyle.Render(
			fmt.Sprintf("✔ done · +%d tok · %s · %.1fs", delta, costStr, latencySec)))

		if m.resolver.Current() == modes.ModePlan {
			// ── STRICT JSON SCHEMA ENFORCEMENT ───────────────────────
			// The /plan mode MUST consume ONLY the verified JSON structure
			// mapped by the schema (prompt/plan.go). If the handoff payload
			// is unparsed or corrupted, do NOT let the local LLM hallucinate
			// ambient tasks via markdown fallback. Force the controller to
			// surface the structural error and reject the output.
			// NOTE: raw final was already pushed as roleAI at line 771 and
			// rendered through cacheRecordToHistory → renderStreamingContent,
			// which handles JSON widget rendering. Do NOT push rendered content
			// again here — that creates a double-rendering loop.
			if jsonResult := plan.ParseJSONPlan(final); jsonResult != nil && jsonResult.Valid && jsonResult.Plan != nil {
				if len(jsonResult.Tasks) > 0 {
					tasks := jsonResult.Tasks
					m.sess.StageTaskList(&tasks)
					m.push(roleStatus, "System status: Plan staged. Use /build to execute changes.")
				}
			} else {
				errMsg := "plan rejected: output does not conform to JSON schema"
				if jsonResult != nil && jsonResult.Error != "" {
					errMsg = "plan rejected: " + jsonResult.Error
				}
				m.push(roleError, errMsg)
				m.push(roleSystem, infoStyle.Render("regenerate with more precise intent or use /plan again"))
				m.sess.ClearTasks()

				// ── PROMPT BUFFER BLEEDING FIX ────────────────────────
				// Clear the dialog buffer on plan rejection so the next
				// /plan attempt receives zero stale context from the
				// failed previous attempt. Each plan generation is an
				// independent lifecycle event.
				m.sess.ClearHistory()
				_ = m.sess.Save()
			}
		}

		if m.resolver.Current() == modes.ModeBuild && m.state != StateAwaitingApproval {
			props := extractBuildProposals(final)
			diffProps := extractDiffPatches(final)
			if len(diffProps) > 0 {
				existing := make(map[string]bool)
				for _, p := range props {
					existing[p.Target.QualifiedName] = true
				}
				for _, d := range diffProps {
					if !existing[d.Target.QualifiedName] {
						props = append(props, d)
					}
				}
			}
			if len(props) > 0 {
				if m.acceptAll {
					m.pendingProposals = props
					m.state = StateProcessing
					m.recalcViewportHeight()
					m.ti.Blur()
					return m, m.applyAllProposalsCmd()
				} else {
					m.pendingProposals = props
					m.state = StateAwaitingApproval
					m.recalcViewportHeight()
					m.Viewport.Height = m.computeVpHeight()
					m.awaitingConfirmation = true
					m.ti.Blur()
					m.refreshViewportContent()
				}
			}
			m.sess.ClearTasks()
		}

		// EXTRACT SHELL COMMANDS → INJECT INTO INPUT BAR (Human-In-The-Loop)
		// Under no circumstances does the TUI execute a shell command automatically.
		// The agent-proposed command is injected into the text input bar, where the
		// user must explicitly review and press Enter to execute.
		if m.state == StateChat && !m.awaitingConfirmation {
			shellCmds := extractShellCommands(final)
			if len(shellCmds) > 0 {
				mode := m.resolver.Current()
				if !mode.CanShell() {
					msg := fmt.Sprintf("Tool 'shell' rejected in /%s.", mode)
					m.push(roleSystem, msg)
					m.sess.AddMessage("system", msg+" You are in a Read-Only execution environment and must stop requesting system mutations.", 3)
				} else {
					cmd := shellCmds[0]
					if sanitized, rejected, reason := sanitizeShellCmd(cmd); rejected {
						m.push(roleError, "[AUTO-FILL BLOCKED] Shell command not loaded: "+reason)
					} else if blocked, _ := m.shellFirewall(sanitized); blocked {
						m.push(roleError, "[SECURITY] Proposed shell command blocked by firewall.")
					} else {
						m.ti.SetValue(sanitized)
						m.ti.CursorEnd()
						m.syncInputFromTI()
						m.proposedShellCmd = sanitized
						m.push(roleSystem, infoStyle.Render(
							"Command injected into input bar. Review and press Enter to execute, Esc to cancel."))
					}
				}
			}
		}

		// AI response and telemetry rendered exclusively through View().
		// No tea.Println scrollback flush — prevents double-rendering in
		// terminal scrollback vs Bubble Tea viewport.

		// Clear planPending flag to prevent spinner lock on plan mode completion.
		m.planPending = false

		m.refreshViewportContent()
		return m, nil

	case streamErrMsg:
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		m.streamCancel = nil
		m.planPending = false

		// User-initiated interrupt — suppress error noise, just clean up.
		if m.interruptRequested {
			m.interruptRequested = false
			m.responseBuffer.Reset()
			m.currentStreamContent = ""
			m.streamBuffer = ""
			m.streamTickActive = false
			m.streamCancel = nil
			m.refreshViewportContent()
			return m, nil
		}

		if m.sess.ObjectiveState != nil && m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveExecuting {
			m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
		}
		m.push(roleError, "stream error: "+msg.err.Error())
		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		return m, flush

	case TaskFinishedMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.pipelineRunning = false
		m.pipelineStep = ""
		m.streaming = false
		m.streamCh = nil
		m.planPending = false
		if m.streamCancel != nil {
			m.streamCancel()
			m.streamCancel = nil
		}
		m.streamBuffer = ""
		m.currentStreamContent = ""
		m.streamTickActive = false
		m.interruptRequested = false
		m.spinnerFrame = 0
		m.ti.Focus()
		m.sanitizeInputPrompt()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case traceUpdateMsg:
		m.currentTrace = msg.trace
		return m, nil

	case config.ConfigChangeMsg:
		newCfg, err := config.Load()
		if err == nil {
			m.cfg = newCfg
		}
		return m, nil

	case gitInitResultMsg:
		if msg.err != nil {
			m.initGitInitErr = msg.err.Error()
		} else if m.initStage == initGitCheck {
			m.initGitInitDone = true
			m.advancePastGitCheck()
		}
		return m, m.spinnerTickCmd()

	case tea.MouseMsg:
		// HARD GUARD: In destructive states (approval/exec), mouse events are
		// completely ignored — no viewport scrolling, no coordinate mapping.
		// This eliminates any possibility of accidental mutation via click.
		// During processing, wheel events are allowed for scroll inspection.
		if m.state == StateAwaitingApproval {
			return m, nil
		}
		if m.state == StateProcessing && msg.Button != tea.MouseButtonWheelUp && msg.Button != tea.MouseButtonWheelDown {
			return m, nil
		}
		// Track scroll-up (wheel up) to suppress auto-scroll during
		// user-inspection. Scroll-down does NOT reset the flag — only
		// SPACE or a new submission resets it.
		if msg.Button == tea.MouseButtonWheelUp {
			m.userIsScrollingUp = true
		}
		// Pure O(1) viewport YOffset shift. No refreshViewportContent, no
		// re-rendering, no string mutation — the viewport internal buffer is
		// already set and only its scroll origin moves.
		if m.Ready {
			var vpCmd tea.Cmd
			m.Viewport, vpCmd = m.Viewport.Update(msg)
			return m, vpCmd
		}
		return m, nil

	case tea.KeyMsg:

		// ── Capability Hotkeys (alt+ modifier only) ────────────────────
		// Single-character hotkeys are strictly banned to prevent key
		// collisions with normal prompt input (e.g., typing in /plan).
		// The active capabilities come from the workflow layer's render
		// context; the renderer/update loop never decides which exist.
		if !m.streaming && !m.agentRunning && m.state == StateChat {
			key := msg.String()
			for _, act := range m.BuildWorkspace().Actions {
				if act.Enabled && strings.EqualFold(act.Shortcut, key) {
					return m, m.handleChipActivation(act)
				}
			}
		}

		// In special states, route directly to handleKey.
		if m.state == StateAwaitingApproval || m.state == StateProcessing {
			resModel, cmd := m.handleKey(msg)
			return resModel, cmd
		}

		if strings.TrimSpace(m.ti.Value()) == "/clear" && msg.String() == "enter" {
			m.showBanner = true
		} else if msg.String() == "enter" && strings.TrimSpace(m.ti.Value()) != "" {
			m.showBanner = false
		}

		// ── '?' help toggle (only when input buffer is empty) ────────────
		if msg.String() == "?" && strings.TrimSpace(m.ti.Value()) == "" {
			m.showHelpOverlay = !m.showHelpOverlay
			return m, nil
		}
		if m.showHelpOverlay {
			if msg.String() == "?" || msg.Type == tea.KeyEscape {
				m.showHelpOverlay = false
				return m, nil
			}
			// Block all other input while help is showing
			return m, nil
		}

		// ── Autocomplete active: intercept navigation / dismissal ──────
		if m.autocompleteActive && len(m.autocompleteItems) > 0 {
			switch msg.Type {
			case tea.KeyEscape:
				m.dismissAutocomplete()
				return m, nil
			case tea.KeyUp:
				m.navigateAutocomplete(-1)
				return m, nil
			case tea.KeyDown:
				m.navigateAutocomplete(1)
				return m, nil
			case tea.KeyTab:
				m.completeAutocomplete()
				return m, nil
			case tea.KeyEnter:
				m.completeAutocomplete()
				return m, nil
			case tea.KeySpace:
				m.dismissAutocomplete()
				// fall through so space inserts into textinput
			}
		}

		if !m.autocompleteActive && !m.streaming && !m.agentRunning {
			switch msg.Type {
			case tea.KeyUp:
				if len(m.history) > 0 {
					if m.historyIndex == -1 {
						m.historyIndex = len(m.history) - 1
					} else if m.historyIndex > 0 {
						m.historyIndex--
					}
					m.ti.SetValue(m.history[m.historyIndex])
					m.ti.CursorEnd()
				}
				return m, nil

			case tea.KeyDown:
				if m.historyIndex != -1 {
					if m.historyIndex < len(m.history)-1 {
						m.historyIndex++
						m.ti.SetValue(m.history[m.historyIndex])
						m.ti.CursorEnd()
					} else {
						m.historyIndex = -1
						m.ti.SetValue("")
						m.ti.CursorEnd()
					}
				}
				return m, nil
			}
		}

		// ── Viewport scroll keys with scroll-lock tracking ──────────────────
		if m.Ready {
			switch msg.Type {
			case tea.KeyPgUp, tea.KeyHome:
				m.Viewport, _ = m.Viewport.Update(msg)
				m.userIsScrollingUp = true
				return m, nil
			case tea.KeyPgDown, tea.KeyEnd:
				m.Viewport, _ = m.Viewport.Update(msg)
				return m, nil
			}
		}

		// ── SPACE snap-to-bottom (resets user scroll-lock) ─────────────────
		if msg.Type == tea.KeySpace && !m.autocompleteActive {
			m.userIsScrollingUp = false
			if m.Ready {
				m.Viewport.GotoBottom()
			}
		}

		resModel, cmd := m.handleKey(msg)
		return resModel, cmd
	}

	// ── Viewport scroll keys (any state) ─────────────────────────────────────
	if m.Ready {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch keyMsg.Type {
			case tea.KeyPgUp, tea.KeyHome:
				m.Viewport, _ = m.Viewport.Update(keyMsg)
				m.userIsScrollingUp = true
				return m, nil
			case tea.KeyPgDown, tea.KeyEnd:
				m.Viewport, _ = m.Viewport.Update(keyMsg)
				return m, nil
			}
		}
	}

	// ── Text Input Pass-Through ──────────────────────────────────────────────
	var tiCmd tea.Cmd
	m.ti, tiCmd = m.ti.Update(msg)
	return m, tiCmd
}

func (m *model) spinnerTickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) smoothStreamTickCmd() tea.Cmd {
	return tea.Tick(20*time.Millisecond, func(t time.Time) tea.Msg {
		return smoothStreamTickMsg(t)
	})
}

// planSlowNoticeCmd schedules a one-shot soft-timeout probe for /plan synthesis.
// It captures the synthesis start time so the handler can verify the notice
// still applies to the CURRENT synthesis (a stale probe from a prior run is
// ignored). It never cancels or shortens the real work — it only surfaces a
// viewport-safe warning if the local model is slow to respond.
func (m *model) planSlowNoticeCmd() tea.Cmd {
	started := m.planStartedAt
	return tea.Tick(planSlowNoticeDelay, func(time.Time) tea.Msg {
		return planSlowNoticeMsg{startedAt: started}
	})
}

// containsMutationIntention detects whether an LLM analysis output from
// investigate mode proposes concrete file mutations. Uses language-annotated
// code blocks as the heuristic — when the agent outputs code blocks with known
// language identifiers (go, diff, python, etc.), it indicates a patch proposal.
// detectCompileFailure scans the given output for build/compile failure
// signatures. Returns true when the codebase is in a non-compilable state
// that requires structural recovery before any patch can be applied.
func detectCompileFailure(output string) bool {
	if output == "" {
		return false
	}
	lower := strings.ToLower(output)
	indicators := []string{
		"[build failed]",
		"syntax error",
		"compilation error",
		"expected declaration",
		"non-declaration statement outside function body",
		"expected ';'",
		"cannot find package",
		"undefined:",
		"not enough arguments",
		"too many errors",
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// hasMissingModuleError detects Go missing-dependency errors in build/test
// output. When a build fails because a module is not in go.sum/go.mod, the
// compiler prints "no required module provides package" or hints "to add it:
// go get". These errors cannot be fixed by editing .go files — they require
// running go get <package>. Returns true if any such pattern is found.
func hasMissingModuleError(output string) bool {
	if output == "" {
		return false
	}
	lower := strings.ToLower(output)
	indicators := []string{
		"no required module provides package",
		"to add it: go get",
		"missing go.sum entry for module",
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

func containsMutationIntention(content string) bool {
	lower := strings.ToLower(content)
	mutationLanguages := []string{
		"```go", "```diff", "```patch", "```python", "```typescript",
		"```javascript", "```java", "```rust", "```c", "```cpp", "```c++",
		"```rs", "```ts", "```js", "```py",
	}
	for _, lang := range mutationLanguages {
		if strings.Contains(lower, lang) {
			return true
		}
	}
	return false
}

// ── Vi-mode lifecycle ─────────────────────────────────────────────────────────

// enterViMode transitions the UI into navigation mode: blurs the text input,
// initializes cursor at the last record, resets selection state, and refreshes
// the viewport with cursor highlighting.
func (m *model) enterViMode() {
	m.inViMode = true
	m.viModeState = ViNormal
	m.cursorLine = max(0, len(m.records)-1)
	m.cursorCol = 0
	m.visualStartLine = 0
	m.visualStartCol = 0
	vpHeight := m.computeVpHeight()
	m.viTopLine = max(0, len(m.records)-vpHeight)
	if m.viTopLine > m.cursorLine {
		m.viTopLine = m.cursorLine
	}
	m.viSearchResults = nil
	m.viSearchIdx = -1
	m.viPendingPrefix = ""
	m.viCmdMode = false
	m.viCmdBuf = ""
	m.ti.Blur()
	m.refreshViewportContent()
}

// exitViMode returns the UI to normal interactive mode: clears selection,
// refocuses the text input, and resets all vi-mode state.
func (m *model) exitViMode() {
	m.inViMode = false
	m.viModeState = ViNormal
	m.cursorLine = 0
	m.cursorCol = 0
	m.visualStartLine = 0
	m.visualStartCol = 0
	m.viTopLine = 0
	m.viSearchResults = nil
	m.viSearchIdx = -1
	m.viPendingPrefix = ""
	m.viCmdMode = false
	m.viCmdBuf = ""
	m.searchActive = false
	m.searchQuery = ""
	m.ti.Focus()
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}

// ── Vi-mode key handler ───────────────────────────────────────────────────────

// handleViModeKey routes all keyboard events during vi-mode. It implements a
// state machine that handles motion (j/k/gg/G/Ctrl+d/Ctrl+u), search (/),
// visual selection (v), yank (y), command-line entry (:), and exit (i).
func (m *model) handleViModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ── Command-line mode (:q, /search) ──────────────────────────────────
	if m.viCmdMode {
		return m.handleViCmdInput(msg)
	}

	// ── Pending prefix: handle multi-key sequences like gg ─────────────
	if m.viPendingPrefix != "" {
		prefix := m.viPendingPrefix
		m.viPendingPrefix = ""
		if prefix == "g" && (msg.String() == "g" || (msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'g')) {
			// Jump to absolute top: reset logical coords and instantly snap
			// the viewport offset to the very first physical line.
			m.cursorLine = 0
			m.cursorCol = 0
			m.viTopLine = 0
			m.viForceTop = true
			m.viSearchIdx = -1
			m.syncViewportToCursor()
			return m, nil
		}
	}

	// ── Single-key vi-mode actions ──────────────────────────────────────
	switch msg.String() {
	// ── Exit / return to normal input ──
	case "i":
		m.exitViMode()
		return m, nil

	// ── 2D Motions ──
	case "h":
		if m.cursorCol > 0 {
			m.cursorCol--
		}
		m.syncViewportToCursor()
		return m, nil

	case "l":
		lineLen := m.lineRuneLen(m.cursorLine)
		if m.cursorCol < lineLen-1 {
			m.cursorCol++
		}
		m.syncViewportToCursor()
		return m, nil

	case "j":
		if m.cursorLine < len(m.records)-1 {
			m.cursorLine++
			// Horizontal safe-guard: clamp cursorCol to new line length
			if m.cursorCol > m.lineRuneLen(m.cursorLine) {
				m.cursorCol = max(0, m.lineRuneLen(m.cursorLine)-1)
			}
			m.viSearchIdx = -1
			m.syncViewportToCursor()
		}
		return m, nil

	case "k":
		if m.cursorLine > 0 {
			m.cursorLine--
			// Horizontal safe-guard: clamp cursorCol to new line length
			if m.cursorCol > m.lineRuneLen(m.cursorLine) {
				m.cursorCol = max(0, m.lineRuneLen(m.cursorLine)-1)
			}
			m.viSearchIdx = -1
			m.syncViewportToCursor()
		}
		return m, nil

	// ── Line-boundary motions ──
	case "0":
		m.cursorCol = 0
		m.syncViewportToCursor()
		return m, nil

	case "$":
		m.cursorCol = max(0, m.lineRuneLen(m.cursorLine)-1)
		m.syncViewportToCursor()
		return m, nil

	// ── Page motions ──
	case "ctrl+d":
		pageSize := m.computeVpHeight() / 2
		if pageSize < 1 {
			pageSize = 1
		}
		m.cursorLine = min(m.cursorLine+pageSize, max(0, len(m.records)-1))
		m.cursorCol = min(m.cursorCol, m.lineRuneLen(m.cursorLine))
		m.viSearchIdx = -1
		m.syncViewportToCursor()
		return m, nil

	case "ctrl+u":
		pageSize := m.computeVpHeight() / 2
		if pageSize < 1 {
			pageSize = 1
		}
		m.cursorLine = max(m.cursorLine-pageSize, 0)
		m.cursorCol = min(m.cursorCol, m.lineRuneLen(m.cursorLine))
		m.viSearchIdx = -1
		m.syncViewportToCursor()
		return m, nil

	// ── Jump to bottom ──
	case "G":
		totalLines := len(m.records)
		if totalLines == 0 {
			return m, nil
		}
		// Move logical cursor to the last line.
		m.cursorLine = totalLines - 1
		// Clamp the column to the printable length of the last line (ANSI-safe).
		m.cursorCol = min(m.cursorCol, m.lineRuneLen(m.cursorLine))
		// Anchor the viewport so the last physical line sits at the very
		// bottom of the visible screen (handled physically in syncViewportToCursor).
		m.viTopLine = m.cursorLine
		m.viForceBottom = true
		m.viSearchIdx = -1
		m.syncViewportToCursor()
		return m, nil

	// ── Prefix for multi-key sequences ──
	case "g":
		if len(m.records) > 0 {
			m.viPendingPrefix = "g"
		}
		return m, nil

	// ── Search ──
	case "/":
		m.viCmdMode = true
		m.viCmdBuf = "/"
		m.searchActive = true
		m.searchQuery = ""
		return m, nil

	case "n":
		if len(m.viSearchResults) > 0 && m.viSearchIdx >= 0 {
			m.viSearchIdx = (m.viSearchIdx + 1) % len(m.viSearchResults)
			m.cursorLine = m.viSearchResults[m.viSearchIdx]
			m.cursorCol = 0
			m.syncViewportToCursor()
		}
		return m, nil

	case "N":
		if len(m.viSearchResults) > 0 && m.viSearchIdx >= 0 {
			m.viSearchIdx--
			if m.viSearchIdx < 0 {
				m.viSearchIdx = len(m.viSearchResults) - 1
			}
			m.cursorLine = m.viSearchResults[m.viSearchIdx]
			m.cursorCol = 0
			m.syncViewportToCursor()
		}
		return m, nil

	// ── Visual selection (character-level) ──
	case "v":
		if m.viModeState == ViVisual {
			m.viModeState = ViNormal
			m.visualStartLine = 0
			m.visualStartCol = 0
		} else {
			m.viModeState = ViVisual
			m.visualStartLine = m.cursorLine
			m.visualStartCol = m.cursorCol
		}
		m.refreshViewportContent()
		return m, nil

	// ── Yank (copy selected text to clipboard) ──
	case "y":
		if m.viModeState == ViVisual {
			m.yankSelection()
			m.viModeState = ViNormal
			m.visualStartLine = 0
			m.visualStartCol = 0
		}
		m.refreshViewportContent()
		return m, nil

	// ── Command-line entry ──
	case ":":
		m.viCmdMode = true
		m.viCmdBuf = ":"
		return m, nil

	// ── Scrolling with arrow keys / pgup/pgdn in viewport ──
	case "up", "ctrl+y":
		var vpCmd tea.Cmd
		if m.Ready {
			m.Viewport, vpCmd = m.Viewport.Update(tea.KeyMsg{Type: tea.KeyUp})
			m.userIsScrollingUp = true
		}
		return m, vpCmd

	case "down", "ctrl+e":
		var vpCmd tea.Cmd
		if m.Ready {
			m.Viewport, vpCmd = m.Viewport.Update(tea.KeyMsg{Type: tea.KeyDown})
		}
		return m, vpCmd

	// ── Space: snap viewport to cursor ──
	case " ":
		m.syncViewportToCursor()
		return m, nil
	}

	// ── Handle key type-based fallthrough ──
	return m, nil
}

// handleViCmdInput processes input within vi command-line mode (: or /).
// The first character of viCmdBuf determines the mode:
//   - ":"  → vim command (q to exit, etc.)
//   - "/"  → forward search
func (m *model) handleViCmdInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		cmd := strings.TrimSpace(m.viCmdBuf)
		m.viCmdMode = false

		if strings.HasPrefix(cmd, ":") {
			sub := strings.TrimSpace(cmd[1:])
			switch sub {
			case "q", "q!", "quit", "wq", "x":
				m.exitViMode()
			}
			return m, nil
		}

		if strings.HasPrefix(cmd, "/") {
			query := cmd[1:]
			m.searchActive = false
			m.searchQuery = query
			m.performSearch(query, false)
			return m, nil
		}

		return m, nil

	case tea.KeyEscape:
		m.viCmdMode = false
		m.searchActive = false
		m.searchQuery = ""
		m.viCmdBuf = ""
		return m, nil

	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.viCmdBuf) > 0 {
			m.viCmdBuf = m.viCmdBuf[:len(m.viCmdBuf)-1]
		}
		return m, nil

	case tea.KeyRunes:
		m.viCmdBuf += string(msg.Runes)
		return m, nil

	default:
		return m, nil
	}
}

// performSearch scans m.records forward from cursorLine looking for a
// case-insensitive substring match. If reverse is true, scans backward.
func (m *model) performSearch(query string, reverse bool) {
	m.viSearchResults = nil
	m.viSearchIdx = -1

	if query == "" || len(m.records) == 0 {
		return
	}

	lowerQuery := strings.ToLower(query)
	start := m.cursorLine
	n := len(m.records)

	for i := 0; i < n; i++ {
		idx := (start + i) % n
		text := m.records[idx].text
		if strings.Contains(strings.ToLower(text), lowerQuery) {
			m.viSearchResults = append(m.viSearchResults, idx)
		}
	}

	if len(m.viSearchResults) > 0 {
		m.viSearchIdx = 0
		m.cursorLine = m.viSearchResults[0]
		m.syncViewportToCursor()
	}
}

// yankSelection copies the character-level visual selection to the system
// clipboard. It extracts rune-precise slices from the underlying text records
// (not from rendered view strings) to avoid any buffer truncation or multi-byte
// UTF-8 splitting.
func (m *model) yankSelection() {
	sLine, sCol := m.visualStartLine, m.visualStartCol
	eLine, eCol := m.cursorLine, m.cursorCol

	// Normalize: ensure start ≤ end in (line, col) tuple space
	if sLine > eLine || (sLine == eLine && sCol > eCol) {
		sLine, eLine = eLine, sLine
		sCol, eCol = eCol, sCol
	}

	var buf strings.Builder
	if sLine == eLine {
		// Single shared line: slice between sCol and eCol (inclusive of eCol)
		runes := []rune(m.records[sLine].text)
		endCol := eCol + 1
		if endCol > len(runes) {
			endCol = len(runes)
		}
		if sCol < endCol {
			buf.WriteString(string(runes[sCol:endCol]))
		}
	} else {
		for i := sLine; i <= eLine && i < len(m.records); i++ {
			runes := []rune(m.records[i].text)
			switch i {
			case sLine:
				// First line of multi-line: from sCol to end (inclusive)
				if sCol < len(runes) {
					buf.WriteString(string(runes[sCol:]))
				}
			case eLine:
				// Last line of multi-line: from start to eCol (inclusive)
				endCol := eCol + 1
				if endCol > len(runes) {
					endCol = len(runes)
				}
				if endCol > 0 {
					buf.WriteString(string(runes[:endCol]))
				}
			default:
				// Fully enclosed line: entire text
				buf.WriteString(m.records[i].text)
			}

			if i < eLine {
				buf.WriteString("\n")
			}
		}
	}

	text := buf.String()
	if text == "" {
		return
	}
	if err := clipboard.WriteAll(text); err != nil {
		m.push(roleSystem, mutedStyle.Render("clipboard error: "+err.Error()))
		m.refreshViewportContent()
	}
}

// syncViewportToCursor scrolls the viewport to bring the cursor line into
// view using viTopLine as the logical scroll anchor. Four constraints:
//  1. Vertical: if cursorLine < viTopLine, scroll viTopLine up to cursorLine.
//  2. Height: if cursorLine >= viTopLine+vpHeight, scroll viTopLine down.
//  3. Horizontal: cursorCol is clamped to the destination line length.
//
// syncViewportToCursor scrolls the viewport to bring the cursor line into
// view using viTopLine as the logical scroll anchor. Because chat records wrap
// into multiple physical terminal lines, all offset math is performed in
// PHYSICAL line space (cumulative rendered line counts) rather than raw record
// indexes, so wrapped lines never desync the viewport. Four constraints:
//  1. Vertical: if the cursor's physical row is above the viewport, scroll up.
//  2. Height: if the cursor's physical row is below the viewport, scroll down.
//  3. Horizontal: cursorCol is clamped to the printable length of the line.
//  4. TUI Sync: YOffset is computed from viTopLine via cumulative physical line
//     counts, and explicit gg/G anchors override with a definitive offset.
func (m *model) syncViewportToCursor() {
	if len(m.records) == 0 {
		return
	}

	vpHeight := m.computeVpHeight()
	if vpHeight < 1 {
		vpHeight = 1
	}

	// Horizontal safe-guard: ensure cursorCol is within the printable length
	// of the cursor line (ANSI-safe — operates on stripped text).
	lineLen := m.lineRuneLen(m.cursorLine)
	if m.cursorCol > lineLen {
		m.cursorCol = max(0, lineLen-1)
	}

	// Build cumulative physical (wrapped) line offsets across all records.
	n := len(m.records)
	phys := make([]int, n+1)
	for i := 0; i < n; i++ {
		phys[i+1] = phys[i] + m.renderedLineCount(m.records[i])
	}
	totalPhys := phys[n]

	// Convert the logical viTopLine anchor into a physical YOffset baseline.
	if m.viTopLine < 0 {
		m.viTopLine = 0
	}
	if m.viTopLine >= n {
		m.viTopLine = n - 1
	}
	yOffset := phys[m.viTopLine]

	// Physical row range occupied by the cursor line (it may wrap).
	cursorStart := phys[m.cursorLine]
	cursorEnd := phys[m.cursorLine+1] - 1

	// Keep the cursor visible: scroll within the physical coordinate space.
	if cursorEnd >= yOffset+vpHeight {
		yOffset = cursorEnd - vpHeight + 1
		m.viTopLine = m.logicalLineAtPhysical(phys, yOffset)
	}
	if cursorStart < yOffset {
		yOffset = cursorStart
		m.viTopLine = m.cursorLine
	}

	// Explicit anchoring requested by gg / G — overrides the window logic with
	// a definitive physical offset.
	if m.viForceTop {
		yOffset = 0
		m.viTopLine = 0
		m.viForceTop = false
	}
	if m.viForceBottom {
		yOffset = max(0, totalPhys-vpHeight)
		m.viTopLine = m.logicalLineAtPhysical(phys, yOffset)
		m.viForceBottom = false
	}

	// Clamp YOffset so the viewport never overscrolls.
	maxOffset := max(0, totalPhys-vpHeight)
	if yOffset < 0 {
		yOffset = 0
	}
	if yOffset > maxOffset {
		yOffset = maxOffset
	}

	m.refreshViewportContent()

	// Sync Bubble Tea viewport YOffset using cumulative physical line counts.
	if m.Ready {
		m.Viewport.YOffset = yOffset
	}
}

// logicalLineAtPhysical returns the logical record index whose physical line
// range contains the given physical row offset.
func (m *model) logicalLineAtPhysical(phys []int, yOffset int) int {
	for i := 0; i < len(phys)-1; i++ {
		if yOffset < phys[i+1] {
			return i
		}
	}
	return max(0, len(phys)-2)
}

// sanitizeFinalContent strips tool execution artifacts and telemetry markers
// from the LLM output before it reaches the JSON parser or render pipeline.
// Lines matching known error/telemetry patterns are removed when they appear
// as leading or trailing noise around the actual LLM response payload.
func sanitizeFinalContent(content string) string {
	lines := strings.Split(content, "\n")
	var clean []string
	inPayload := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inPayload {
			if trimmed == "" || strings.HasPrefix(trimmed, "[FAIL]") ||
				strings.HasPrefix(trimmed, "[ OK ]") ||
				strings.HasPrefix(trimmed, "INFO:") ||
				strings.HasPrefix(trimmed, "WARN:") ||
				strings.HasPrefix(trimmed, "ERROR:") {
				continue
			}
			inPayload = true
		}

		clean = append(clean, line)
	}

	result := strings.Join(clean, "\n")
	return strings.TrimSpace(result)
}
