package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	if m.state == StateAwaitingApproval || m.state == StateAwaitingShellExec {
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

	// ── Awaiting shell execution approval (alt+ modifier only) ──────────────
	if m.state == StateAwaitingShellExec {
		switch {
		case msg.String() == "alt+a":
			if !m.resolver.Current().CanShell() {
				m.pendingShellExec = nil
				m.state = StateChat
				m.recalcViewportHeight()
				m.push(roleSystem, infoStyle.Render("[System] Shell execution blocked by mode capabilities"))
				return m, nil
			}
			block := m.pendingShellExec[m.shellAwaitingIdx]
			m.pendingShellExec = append(m.pendingShellExec[:m.shellAwaitingIdx], m.pendingShellExec[m.shellAwaitingIdx+1:]...)
			if len(m.pendingShellExec) == 0 {
				m.state = StateChat
				m.recalcViewportHeight()
			}
			return m, m.execShellCmd(block.Command)

		case msg.String() == "alt+r":
			m.pendingShellExec = append(m.pendingShellExec[:m.shellAwaitingIdx], m.pendingShellExec[m.shellAwaitingIdx+1:]...)
			if len(m.pendingShellExec) == 0 {
				m.state = StateChat
				m.recalcViewportHeight()
			}
			m.push(roleSystem, infoStyle.Render("shell command skipped"))
			return m, nil

		case msg.Type == tea.KeyEscape:
			m.pendingShellExec = nil
			m.state = StateChat
			m.recalcViewportHeight()
			m.push(roleSystem, infoStyle.Render("shell execution cancelled"))
			return m, nil
		}
		return m, nil
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

	// Handle Escape key requiring three presses to quit (unless help is showing)
	if msg.Type == tea.KeyEscape {
		if m.showHelpOverlay {
			m.showHelpOverlay = false
			return m, nil
		}
		m.escPressCount++
		if m.escPressCount >= 3 {
			m.escPressCount = 0
			if m.showSuggestions {
				m.dismissSuggestions()
				return m, nil
			}
			m.sess.SetMode(m.resolver.Current())
			_ = m.sess.Save()
			return m, tea.Quit
		}
		return m, nil
	}
	m.escPressCount = 0

	switch msg.Type {
	case tea.KeyCtrlC:
		if m.showSuggestions {
			m.dismissSuggestions()
			return m, nil
		}
		m.sess.SetMode(m.resolver.Current())
		_ = m.sess.Save()
		return m, tea.Quit

		// ── Enter: submit (only when autocomplete is NOT active) ───────────────
	case tea.KeyEnter:
		// New message submission resets user scroll-lock so auto-scroll
		// resumes for the incoming response.
		m.userIsScrollingUp = false

		userInput := m.ti.Value()
		m.dismissSuggestions()

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
			return m, cmd
		}
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()
		return m, nil

		// ── History navigation (only when suggestions are NOT active) ─────────
	case tea.KeyUp:
		if m.showSuggestions && len(m.suggestions) > 0 {
			return m, nil
		}
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
	m.input.WriteString(m.ti.Value())
}
