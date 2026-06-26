package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.awaitingConfirmation {
		switch msg.String() {
		case "1":
			return m, m.applySingleProposal()
		case "2":
			m.acceptAll = true
			return m, m.applyAllProposals()
		case "3", "esc":
			m.awaitingConfirmation = false
			m.pendingProposals = nil
			m.acceptAll = false
			m.push(roleSystem, infoStyle.Render("changes rejected"))
			return m, nil
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEscape:
		if m.showSuggestions {
			m.dismissSuggestions()
			return m, nil
		}
		m.sess.SetMode(m.resolver.Current())
		m.sess.Save()
		return m, tea.Quit

	case tea.KeyEnter:
		line := m.input.String()
		m.input.Reset()
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
				m.input.WriteString(line)
				return m, nil
			}
			line = sel
		}
		m.dismissSuggestions()
		if line != "" {
			cmd := m.handleInput(line)
			return m, cmd
		}
		return m, nil

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

	case tea.KeyBackspace:
		s := m.input.String()
		if len(s) > 0 {
			m.input.Reset()
			m.input.WriteString(s[:len(s)-1])
			m.updateSuggestions()
		} else {
			m.dismissSuggestions()
		}
		return m, nil

	case tea.KeySpace:
		m.input.WriteString(" ")
		m.updateSuggestions()
		return m, nil

	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyUp, tea.KeyDown:
		return m, nil

	case tea.KeyRunes:
		m.input.WriteString(string(msg.Runes))
		m.updateSuggestions()
		return m, nil
	}

	return m, nil
}
