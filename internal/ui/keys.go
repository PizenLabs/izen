package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
)

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	// Arrow keys, j/k, ctrl+u, ctrl+d must forward to viewport so the
	// user can fluidly inspect long file diffs without scroll lockout.
	// Tracks scroll-up for user-scroll-lock to prevent auto-scroll jank.
	if m.state == StateAwaitingApproval {
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
			case msg.Type == tea.KeySpace:
				m.userIsScrollingUp = false
				m.Viewport.GotoBottom()
				return m, nil
			}
		}
	}

	// ── Awaiting approval (alt+ modifier only) ──────────────────────────────
	if m.state == StateAwaitingApproval {
		switch {
		case msg.String() == "alt+a":
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
