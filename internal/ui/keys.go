package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
			m.rebuildViewport()
			return m, nil
		}
		return m, nil
	}

	// Handle Escape key requiring three presses to quit
	if msg.Type == tea.KeyEscape {
		m.escPressCount++
		if m.escPressCount >= 3 {
			m.escPressCount = 0
			if m.showSuggestions {
				m.dismissSuggestions()
				return m, nil
			}
			m.sess.SetMode(m.resolver.Current())
			m.sess.Save()
			return m, tea.Quit
		}
		// Wait for more ESC presses
		return m, nil
	}
	// Reset escape counter on any other key
	m.escPressCount = 0

	switch msg.Type {
	// ── Quit ─────────────────────────────────────────────────────────────────
	case tea.KeyCtrlC:
		if m.showSuggestions {
			m.dismissSuggestions()
			return m, nil
		}
		m.sess.SetMode(m.resolver.Current())
		m.sess.Save()
		return m, tea.Quit

	// ── Enter: submit ─────────────────────────────────────────────────────────
	case tea.KeyEnter:
		line := m.ti.Value()

		if m.showSuggestions && len(m.suggestions) > 0 {
			sel := m.suggestions[m.suggestionIdx]
			if m.suggestionType == "@" {
				raw := line
				atIdx := strings.LastIndex(raw, "@")
				if atIdx >= 0 {
					line = raw[:atIdx] + sel
				} else {
					line = sel
				}
				m.pendingFileRefs = append(m.pendingFileRefs, sel)
				m.attachedFiles = append(m.attachedFiles, sel)
				m.dismissSuggestions()
				m.ti.SetValue(line)
				m.syncInputFromTI()
				return m, nil
			}
			line = sel
		}
		m.dismissSuggestions()

		if line != "" {
			// Banner: hide on first user message
			if m.showBanner {
				m.showBanner = false
			}

			// Echo user line into viewport
			userLine := gutterUserStyle.Render("▌") + " " +
				labelUserStyle.Render("you") +
				promptStyle.Render(" > ") +
				outputStyle.Render(line)
			m.records = append(m.records, record{role: roleUser, text: userLine})

			// History
			m.history = append(m.history, line)
			m.historyIndex = len(m.history)
			m.saveHistory()

			m.ti.SetValue("")
			m.syncInputFromTI()
			m.rebuildViewport()

			cmd := m.handleInput(line)
			return m, cmd
		}
		m.ti.SetValue("")
		m.syncInputFromTI()
		return m, nil

	// ── Tab: cycle suggestions ────────────────────────────────────────────────
	case tea.KeyTab:
		if m.showSuggestions && len(m.suggestions) > 0 {
			m.suggestionIdx = (m.suggestionIdx + 1) % len(m.suggestions)
		}
		return m, nil

	case tea.KeyShiftTab:
		if m.showSuggestions && len(m.suggestions) > 0 {
			m.suggestionIdx--
			if m.suggestionIdx < 0 {
				m.suggestionIdx = len(m.suggestions) - 1
			}
		}
		return m, nil

	// ── Up/Down: command history (when no suggestions) ────────────────────────
	case tea.KeyUp:
		if m.showSuggestions && len(m.suggestions) > 0 {
			m.suggestionIdx--
			if m.suggestionIdx < 0 {
				m.suggestionIdx = len(m.suggestions) - 1
			}
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
			m.suggestionIdx = (m.suggestionIdx + 1) % len(m.suggestions)
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

	// ── All other keys → textinput ────────────────────────────────────────────
	default:
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		m.syncInputFromTI()
		m.updateSuggestions()
		return m, tiCmd
	}
}

func (m *model) syncInputFromTI() {
	m.input.Reset()
	m.input.WriteString(m.ti.Value())
}

// printSystem / printInfo / printError — now push into viewport records.
func printSystem(text string) {
	// These are called from commands.go — they need model access.
	// Use a no-op here; commands.go methods call m.push() directly now.
	fmt.Print("") // suppress unused import
}

func printInfo(text string)  { _ = text }
func printError(text string) { _ = text }
