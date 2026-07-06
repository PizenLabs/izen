package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ── Awaiting shell execution approval ─────────────────────────────────────
	if m.state == StateAwaitingShellExec {
		switch msg.String() {
		case "a", "A":
			if !m.resolver.Current().CanShell() {
				m.pendingShellExec = nil
				m.state = StateChat
				m.push(roleSystem, infoStyle.Render("[System] Shell execution blocked by mode capabilities"))
				return m, nil
			}
			block := m.pendingShellExec[m.shellAwaitingIdx]
			m.pendingShellExec = append(m.pendingShellExec[:m.shellAwaitingIdx], m.pendingShellExec[m.shellAwaitingIdx+1:]...)
			if len(m.pendingShellExec) == 0 {
				m.state = StateChat
			}
			return m, m.execShellCmd(block.Command)

		case "r", "R":
			m.pendingShellExec = append(m.pendingShellExec[:m.shellAwaitingIdx], m.pendingShellExec[m.shellAwaitingIdx+1:]...)
			if len(m.pendingShellExec) == 0 {
				m.state = StateChat
			}
			m.push(roleSystem, infoStyle.Render("shell command skipped"))
			return m, nil

		case "esc":
			m.pendingShellExec = nil
			m.state = StateChat
			m.push(roleSystem, infoStyle.Render("shell execution cancelled"))
			return m, nil
		}
		return m, nil
	}

	// ── Awaiting approval ────────────────────────────────────────────────────
	if m.state == StateAwaitingApproval {
		switch msg.String() {
		case "a", "A":
			return m, m.applySingleProposal()
		case "l", "L":
			m.acceptAll = true
			return m, m.applyAllProposals()
		case "r", "R", "esc":
			m.state = StateChat
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
