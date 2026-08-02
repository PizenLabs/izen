package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
)

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ── GLOBAL: Alt+O toggles reasoning block visibility ────────────
	if msg.String() == "alt+o" {
		if m.thinkingPanel != nil {
			m.thinkingPanel.Toggle()
		}
		m.showReasoning = m.thinkingPanel != nil && m.thinkingPanel.Expanded()
		m.refreshViewportContent()
		if m.Ready && !m.userIsScrollingUp {
			m.Viewport.GotoBottom()
		}
		return m, nil
	}

	// ── GLOBAL: Ctrl+O cycles through all foldable log entries ──
	// Each press expands the next entry, collapsing the previous one.
	// When no entry is expanded, the first entry is expanded.
	// Pressing Ctrl+O again cycles to the next entry.
	if msg.Type == tea.KeyCtrlO {
		if m.logStore != nil {
			newID := m.logStore.ToggleCycle()
			if newID >= 0 {
				m.refreshViewportContent()
				if m.Ready && !m.userIsScrollingUp {
					m.Viewport.GotoBottom()
				}
			}
		}
		return m, nil
	}

	// ── GLOBAL: Alt+F / Option+F / Meta+F — Handoff from /ask to /investigate ──
	// Checks the latest valid /ask Context Ledger (ask_handoff packet), and if
	// present, transitions to /investigate with the ledger injected as context.
	// If no valid /ask Context Ledger exists, rejects with a clear TUI notice.
	if msg.String() == "alt+f" {
		if m.state == StateProcessing || m.state == StateAwaitingApproval {
			return m, nil
		}
		if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning {
			return m, nil
		}

		// Check for a valid /ask Context Ledger
		hasAskHandoff := false
		handoffContent := ""
		if m.sess != nil && m.sess.ContextLedger != nil {
			// Check for an "ask_handoff" packet in the ledger
			for _, p := range m.sess.ContextLedger.Packets {
				if p.Kind == "ask_handoff" {
					hasAskHandoff = true
					handoffContent = p.Payload
					break
				}
			}
			// Fallback: check Diagnostics if no ask_handoff packet found
			if !hasAskHandoff && m.sess.ContextLedger.Diagnostics != "" {
				hasAskHandoff = true
				handoffContent = m.sess.ContextLedger.Diagnostics
			}
		}
		// Also check the transient handoffLedgerContent
		if !hasAskHandoff && m.handoffLedgerContent != "" {
			hasAskHandoff = true
			handoffContent = m.handoffLedgerContent
		}

		if !hasAskHandoff || handoffContent == "" {
			m.push(roleError, "No active Context Ledger from /ask. Run $prompt <query> in any mode first to generate a Forensic Context Ledger.")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, nil
		}

		// Create Handoff Context from the ask handoff payload
		m.handoffCtx.LastFailurePayload = handoffContent
		m.handoffCtx.ProposedFix = handoffContent
		m.handoffLedgerContent = handoffContent

		// ── INTENT-BASED /ask HANDOFF BYPASS ───────────────────────────
		// FRONTEND_UI and code-generation/rewrite prompts add ZERO diagnostic
		// value to /investigate: the engine short-circuits them in 0s with an
		// "Inconclusive" result while injecting forensic overhead into the TUI
		// and context ledger. Bypass the engine and transition directly to
		// /plan (UI/layout tasks) or /build (code mutation).
		intent := investigate.ClassifyIntent(handoffContent)
		if intent.IsFrontendUI() {
			m.handoffLedgerContent = "frontend ui intent detected — hand off to plan"
			m.handoffCtx.ProposedFix = handoffContent
			m.modeChangeAuthorized = true
			m.currentResult = nil
			return m, m.setMode(modes.ModePlan)
		}
		if hasMutationIntent(handoffContent) && hasExecutableBuildTarget(handoffContent, m) {
			m.handoffCtx.PendingTodos = synthesizeBuildTodosFromMutation(handoffContent)
			m.modeChangeAuthorized = true
			m.currentResult = nil
			return m, m.setMode(modes.ModeBuild)
		}

		// Transition mode to /investigate (clean transition)
		m.modeChangeAuthorized = true
		m.currentResult = nil
		cmd := m.setMode(modes.ModeInvestigate)
		return m, cmd
	}

	// ── StateProcessing: block input but allow viewport navigation ──────
	if m.state == StateProcessing {
		if m.Ready {
			switch {
			case msg.Type == tea.KeyUp || msg.String() == "k" || msg.Type == tea.KeyCtrlU:
				m.userIsScrollingUp = true
				var vpCmd tea.Cmd
				m.Viewport, vpCmd = m.Viewport.Update(msg)
				return m, vpCmd
			case msg.Type == tea.KeyDown || msg.String() == "j" || msg.Type == tea.KeyCtrlD:
				var vpCmd tea.Cmd
				m.Viewport, vpCmd = m.Viewport.Update(msg)
				return m, vpCmd
			case msg.Type == tea.KeyPgUp || msg.Type == tea.KeyHome:
				m.Viewport, _ = m.Viewport.Update(msg)
				m.userIsScrollingUp = true
				return m, nil
			case msg.Type == tea.KeyPgDown || msg.Type == tea.KeyEnd:
				m.Viewport, _ = m.Viewport.Update(msg)
				return m, nil
			case msg.Type == tea.KeySpace:
				m.userIsScrollingUp = false
				m.Viewport.GotoBottom()
				return m, nil
			}
		}
		return m, nil
	}

	// ── Viewport navigation pass-through in locked states ───────────────
	// j/k and ctrl+u/ctrl+d forward to the main workspace viewport so
	// the user can fluidly inspect the log/context history behind the
	// approval/proposal modal. Arrow keys are reserved for proposal
	// diff scrolling (see block below). Tracks scroll-up to prevent
	// auto-scroll jank when the user inspects log history.
	if m.state == StateAwaitingApproval {
		if m.Ready {
			switch {
			case msg.String() == "k" || msg.Type == tea.KeyCtrlU:
				m.userIsScrollingUp = true
				var vpCmd tea.Cmd
				m.Viewport, vpCmd = m.Viewport.Update(msg)
				return m, vpCmd
			case msg.String() == "j" || msg.Type == tea.KeyCtrlD:
				var vpCmd tea.Cmd
				m.Viewport, vpCmd = m.Viewport.Update(msg)
				return m, vpCmd
			case msg.Type == tea.KeySpace:
				m.userIsScrollingUp = false
				m.Viewport.GotoBottom()
				return m, nil
			}
		}
	}

	// ── Proposal diff scroll (inner view) ─────────────
	// Arrow keys and page keys scroll the proposal diff content
	// inside the proposal dock when an expanded diff is available.
	// When no expanded diff is active, arrow keys fall through to
	// the viewport for collapsed proposals and build approval prompts.
	// j/k are reserved for the main workspace viewport.
	if m.state == StateAwaitingApproval && m.Ready {
		if len(m.pendingProposals) > 0 {
			p := m.pendingProposals[0]
			if p.Expanded && p.Diff != "" {
				diffLines := strings.Split(p.Diff, "\n")
				totalDiff := 0
				for _, l := range diffLines {
					if l == "" {
						continue
					}
					if strings.HasPrefix(l, "---") || strings.HasPrefix(l, "+++") {
						continue
					}
					totalDiff++
				}
				maxOffset := totalDiff - maxProposalDiffHeight
				if maxOffset < 0 {
					maxOffset = 0
				}

				switch msg.Type {
				case tea.KeyUp:
					if m.proposalDiffOffset > 0 {
						m.proposalDiffOffset--
					}
					return m, nil
				case tea.KeyDown:
					if m.proposalDiffOffset < maxOffset {
						m.proposalDiffOffset++
					}
					return m, nil
				case tea.KeyPgUp:
					m.proposalDiffOffset = max(0, m.proposalDiffOffset-maxProposalDiffHeight)
					return m, nil
				case tea.KeyPgDown:
					m.proposalDiffOffset = min(maxOffset, m.proposalDiffOffset+maxProposalDiffHeight)
					return m, nil
				}
			}
		}
	}

	// ── Awaiting approval ────────────────────────────────────────────
	if m.state == StateAwaitingApproval {
		// ── Hybrid Intent Gateway mode-selection prompt ─────────────
		// The router classified the prompt with confidence below the policy
		// threshold. Digits select a mode directly, ←/→ cycle the highlight,
		// Enter confirms the highlighted mode, Esc falls back to /ask.
		if m.pendingRouteConfirm && len(m.pendingRouteOptions) > 0 {
			switch msg.String() {
			case "1", "2", "3", "4", "5":
				idx := int(msg.String()[0] - '1')
				if idx >= 0 && idx < len(m.pendingRouteOptions) {
					m.pendingRouteIdx = idx
					m.refreshViewportContent()
					return m, m.confirmRouteSelection(m.pendingRouteOptions[idx])
				}
			case "left":
				m.pendingRouteIdx = (m.pendingRouteIdx - 1 + len(m.pendingRouteOptions)) % len(m.pendingRouteOptions)
				m.refreshViewportContent()
				return m, nil
			case "right":
				m.pendingRouteIdx = (m.pendingRouteIdx + 1) % len(m.pendingRouteOptions)
				m.refreshViewportContent()
				return m, nil
			case "enter":
				return m, m.confirmRouteSelection(m.pendingRouteOptions[m.pendingRouteIdx])
			case "esc":
				return m, m.cancelRouteSelection()
			}
		}

		// ── Effort Selector (←/→) ────────────────────────────────────
		if msg.Type == tea.KeyLeft {
			if m.currentEffort > EffortAuto {
				m.currentEffort--
			}
			m.recalcViewportHeight()
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, nil
		}
		if msg.Type == tea.KeyRight {
			if m.currentEffort < EffortHigh {
				m.currentEffort++
			}
			m.recalcViewportHeight()
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, nil
		}

		// ── Arrow-key fallback: scroll the main viewport when
		// there is no expanded proposal diff to inspect. This
		// keeps navigation functional for collapsed proposals and
		// build approval prompts where the diff scroll block
		// above does not intercept. ──────────────────────────
		if msg.Type == tea.KeyUp || msg.Type == tea.KeyDown ||
			msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
			var vpCmd tea.Cmd
			m.Viewport, vpCmd = m.Viewport.Update(msg)
			return m, vpCmd
		}

		// ── $hot HOTFIX APPROVAL GATE ─────────────────────────────
		// The hotfix patch was generated but NOT applied. The developer must
		// explicitly authorize (Alt+A / Enter) or reject (Alt+R / Esc).
		// On approval the patch is written to disk and the stashed plan
		// restored; on rejection the hotfix aborts cleanly to PAUSED with
		// zero disk mutation.
		if m.pendingHotfixTask != nil && m.pendingHotfixPatch != nil {
			switch {
			case msg.String() == "alt+a" || msg.Type == tea.KeyEnter:
				task := m.pendingHotfixTask
				patch := m.pendingHotfixPatch
				m.pendingHotfixTask = nil
				m.pendingHotfixPatch = nil
				m.pendingProposals = nil
				m.state = StateChat
				m.ti.Focus()
				m.recalcViewportHeight()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				m.push(roleSystem, infoStyle.Render(
					fmt.Sprintf("  ✓ Approved — applying hotfix patch to %s...", patch.File)))

				return m, tea.Batch(
					func() tea.Msg { return agentStartMsg{label: "hotfix apply"} },
					m.applyHotfixPatch(task, patch),
					m.smoothStreamTickCmd(),
				)

			case msg.String() == "alt+r" || msg.Type == tea.KeyEscape:
				rejectedPath := m.pendingHotfixTask.Target
				m.pendingHotfixTask = nil
				m.pendingHotfixPatch = nil
				m.pendingProposals = nil
				m.state = StateChat
				m.ti.Focus()
				m.recalcViewportHeight()
				m.push(roleSystem, infoStyle.Render(
					"  ✗ Rejected — hotfix aborted. No files were modified."))
				m.push(roleError, fmt.Sprintf(
					"[HOTFIX] Developer rejected patch to %s.",
					rejectedPath))

				m.hotfixActive = false
				if stashedTasks, rerr := m.restorePlan(); rerr == nil && len(stashedTasks) > 0 {
					m.sess.StageTaskList(&stashedTasks)
					_ = m.sess.Save()
				}
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return m, nil
			}
			return m, nil
		}

		// ── Build approval (SHELL_EXEC permission box) ──────────────
		if m.pendingBuildApproval && m.pendingBuildTask != nil {
			task := m.pendingBuildTask
			switch {
			case msg.String() == "alt+a" || msg.Type == tea.KeyEnter:
				m.pendingBuildApproval = false
				m.pendingBuildTask = nil
				m.state = StateChat
				m.recalcViewportHeight()
				m.ti.Focus()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				m.push(roleSystem, infoStyle.Render("  ✓ Approved — executing shell command..."))
				return m, tea.Batch(
					func() tea.Msg { return agentStartMsg{label: "shell exec"} },
					m.runBuildShellExec(task),
					m.smoothStreamTickCmd(),
				)

			case msg.String() == "alt+l":
				m.pendingBuildAllowAlways = true
				m.pendingBuildApproval = false
				m.pendingBuildTask = nil
				m.state = StateChat
				m.recalcViewportHeight()
				m.ti.Focus()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				m.push(roleSystem, infoStyle.Render(
					"  ✓ Approved (always) — executing shell command..."))
				return m, tea.Batch(
					func() tea.Msg { return agentStartMsg{label: "shell exec"} },
					m.runBuildShellExec(task),
					m.smoothStreamTickCmd(),
				)

			case msg.String() == "alt+r" || msg.Type == tea.KeyEscape:
				m.pendingBuildApproval = false
				m.pendingBuildTask = nil
				m.state = StateChat
				m.recalcViewportHeight()
				m.ti.Focus()
				if m.sess != nil {
					tasks := m.sess.CurrentTasks
					for i := range tasks {
						if tasks[i].StepNum == task.StepNum {
							tasks[i].Status = "stalled"
							break
						}
					}
					m.sess.StageTaskList(&tasks)
					_ = m.sess.Save()
				}
				m.push(roleSystem, infoStyle.Render(
					"  ✗ Rejected — shell execution aborted."))
				m.push(roleError, fmt.Sprintf(
					"[SECURITY] Aborting unauthorized shell execution: %s",
					task.Target))
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return m, nil
			}
			return m, nil
		}

		// ── Native Tool Call Buffer Approval ──────────────────────────
		if m.toolCallBuffer != nil && m.toolCallBuffer.HasPending() {
			switch {
			case msg.String() == "a":
				// Accept single call — apply approved calls to disk
				return m, m.applyToolCallBuffer()
			case msg.String() == "l":
				// Allow all — approve all and apply
				m.toolCallBuffer.ApproveAll()
				return m, m.applyToolCallBuffer()
			case msg.String() == "r" || msg.Type == tea.KeyEscape:
				// Reject — discard buffer
				m.toolCallBuffer.Reject()
				m.state = StateChat
				m.recalcViewportHeight()
				m.push(roleSystem, infoStyle.Render("tool calls rejected"))
				return m, nil
			case msg.String() == "e":
				// Cycle effort level
				m.cycleEffort()
				m.refreshViewportContent()
				return m, nil
			}
			return m, nil
		}

		// ── File-mutation proposal approval ─────────────────────────
		switch {
		case msg.String() == "alt+a" || msg.Type == tea.KeyEnter:
			return m, m.applySingleProposal()
		case msg.String() == "alt+l":
			m.acceptAll = true
			return m, m.applyAllProposals()
		case msg.String() == "alt+p":
			if len(m.pendingProposals) > 0 {
				m.pendingProposals[0].Expanded = !m.pendingProposals[0].Expanded
				m.proposalDiffOffset = 0
				m.recalcViewportHeight()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
			}
			return m, nil
		case msg.String() == "alt+r" || msg.Type == tea.KeyEscape:
			if m.execEng != nil {
				if errs := m.execEng.RollbackTransaction(); len(errs) > 0 {
					for _, err := range errs {
						m.push(roleError, fmt.Sprintf("rollback error: %v", err))
					}
				}
			}
			if m.sess != nil {
				m.sess.ClearHistory()
				_ = m.sess.Save()
			}

			if m.currentBuildTaskID > 0 && m.sess != nil {
				tasks := m.sess.CurrentTasks
				for i := range tasks {
					if tasks[i].StepNum == m.currentBuildTaskID {
						tasks[i].Status = "stalled"
						break
					}
				}
				m.sess.StageTaskList(&tasks)
				_ = m.sess.Save()
			}

			m.ti.Focus()
			m.state = StateChat
			m.recalcViewportHeight()
			m.awaitingConfirmation = false
			m.pendingProposals = nil
			m.acceptAll = false
			m.push(roleSystem, infoStyle.Render("changes rejected"))
			return m, nil
		}
		return m, nil
	}

	if msg.Type == tea.KeyEscape {
		if m.showHelpOverlay {
			m.showHelpOverlay = false
			return m, nil
		}
		if m.showSuggestions {
			m.dismissSuggestions()
			return m, nil
		}
		if m.streaming && m.streamCancel != nil {
			m.streamCancel()
			m.streamCancel = nil
			m.interruptRequested = true
			return m, func() tea.Msg { return TaskFinishedMsg{} }
		}
		if m.proposedShellCmd != "" {
			m.proposedShellCmd = ""
			m.push(roleSystem, infoStyle.Render("Command cancelled."))
		}
		// Build approval is now handled inside StateAwaitingApproval in the
		// block above. The escape key there stalls the task and returns to chat.
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlD:
		if m.ti.Value() == "" && !m.agentRunning && !m.streaming && !m.reviewRunning && !m.pipelineRunning {
			return m, m.cleanShutdownCmd()
		}
		return m, nil

	case tea.KeyCtrlC:
		if m.showSuggestions {
			m.dismissSuggestions()
			return m, nil
		}
		if m.agentRunning || m.streaming || m.reviewRunning || m.pipelineRunning || m.planPending {
			execution.KillAllOrphans()
			m.cancelAllBackgroundContexts()
			m.push(roleSystem, infoStyle.Render("Interrupted."))
			return m, func() tea.Msg { return TaskFinishedMsg{} }
		}
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()
		return m, nil

		// ── Enter: submit (only when autocomplete is NOT active) ───────────────
		// STALE-VIEWPORT GUARD: Every submission path MUST call
		// refreshViewportContent+GotoBottom before returning, guaranteeing
		// the user's input appears immediately rather than waiting for the
		// next UI tick. This prevents the "stale screen until next keypress"
		// regression.
		//
		// HUMAN-IN-THE-LOOP CHECKPOINT: If the agent proposed a shell command
		// (proposedShellCmd is set), the command was injected into the input bar
		// for review. Pressing Enter executes it as a shell command rather than
		// sending it to the LLM. The system remains deterministic, fully visible,
		// and safe from unintended execution.
	case tea.KeyEnter:
		m.userIsScrollingUp = false

		userInput := m.ti.Value()
		m.dismissSuggestions()

		// ── Proposed shell command checkpoint ──────────────────────────────
		if m.proposedShellCmd != "" {
			cmd := m.proposedShellCmd
			m.proposedShellCmd = ""
			m.ti.SetValue("")
			m.ti.Reset()
			m.syncInputFromTI()
			m.push(roleUser, "$ "+cmd)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, m.execShellCmd(cmd)
		}

		if userInput != "" {
			m.currentPrompt = userInput
			if m.showBanner {
				m.showBanner = false
			}
			m.ti.SetValue("")
			m.ti.Reset()
			m.syncInputFromTI()

			m.history = append(m.history, userInput)
			m.historyIndex = len(m.history)
			m.saveHistory()

			m.push(roleUser, userInput)

			m.streamStartTime = time.Now()
			cmd := m.handleInput(userInput)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, cmd
		}
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil

		// ── History navigation (only when suggestions are NOT active) ─────────
	case tea.KeyUp:
		if m.showSuggestions && len(m.suggestions) > 0 {
			return m, nil
		}
		m.proposedShellCmd = ""
		if len(m.history) == 0 {
			return m, nil
		}
		if m.historyIndex > 0 {
			m.historyIndex--
		}
		m.ti.SetValue(m.history[m.historyIndex])
		m.ti.CursorEnd()
		m.syncInputFromTI()
		return m, nil

	case tea.KeyDown:
		if m.showSuggestions && len(m.suggestions) > 0 {
			return m, nil
		}
		m.proposedShellCmd = ""
		if m.historyIndex < len(m.history)-1 {
			m.historyIndex++
			m.ti.SetValue(m.history[m.historyIndex])
			m.ti.CursorEnd()
		} else {
			m.historyIndex = len(m.history)
			m.ti.SetValue("")
		}
		m.syncInputFromTI()
		return m, nil

		// ── '/' and '@' → forward to text input AND trigger suggestions ──────────
	case tea.KeyRunes:
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		m.syncInputFromTI()
		m.updateSuggestions()
		return m, tiCmd

		// ── Text-editing keys: forward directly to textinput, no swallowing ────
	default:
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		m.syncInputFromTI()
		return m, tiCmd
	}
}

func (m *model) syncInputFromTI() {
	m.input.Reset()
	// Defensive strip: never let raw ANSI / mouse-tracking escape sequences
	// (e.g. \x1b[<0;26;37M) into the editable command buffer. Under normal
	// operation Bubble Tea parses mouse into tea.MouseMsg before it reaches the
	// textinput, but this guarantees the buffer stays clean regardless of
	// terminal raw-mode state during /build shell execution.
	m.input.WriteString(sanitizeInputBuffer(m.ti.Value()))
}
