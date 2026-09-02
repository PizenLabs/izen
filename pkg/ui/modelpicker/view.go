package modelpicker

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/pkg/provider/capability"
)

// palette
const (
	colorAccent = "#f5a623"
	colorMauve  = "#cba6f7"
	colorMuted  = "#6c7086"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	effortStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
)

// View implements tea.Model.
func (m Model) View() string {
	if len(m.models) == 0 {
		return titleStyle.Render(" Model Picker ") + "\n\n" + mutedStyle.Render(" no models loaded ")
	}
	if m.Highlighted() == nil {
		return titleStyle.Render(" Model Picker ") + "\n\n" + mutedStyle.Render(" no models match the filter ")
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(" Model Picker "))
	b.WriteString("\n\n")

	if m.filter != "" {
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d matches for %q", len(m.filtered), m.filter)))
	} else {
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d models", len(m.models))))
	}
	b.WriteString("\n\n")

	b.WriteString(m.renderEffort())
	b.WriteString("\n\n")

	b.WriteString(m.renderList())
	b.WriteString("\n")

	footer := "↑/↓ navigate  ←/→ effort  enter select  type to filter"
	b.WriteString(mutedStyle.Render(footer))
	return b.String()
}

// renderEffort renders the dynamically bound effort selector for the
// highlighted model, together with its active thinking budget and total max
// tokens. Non-reasoning models render no selector and no budget line.
func (m Model) renderEffort() string {
	hl := m.Highlighted()
	if hl == nil || !m.HasEffortOptions() {
		return ""
	}
	opts := m.EffortOptions()
	var cells []string
	for i, o := range opts {
		if i == m.EffortIndex() {
			cells = append(cells, effortStyle.Render(string(o)))
		} else {
			cells = append(cells, mutedStyle.Render(string(o)))
		}
	}
	budget := m.ThinkingBudget()
	total := m.TotalMaxTokens()
	info := fmt.Sprintf("Thinking Budget: %s | Total Max: %s tokens",
		capability.FormatTokens(budget), capability.FormatTokens(total))

	return fmt.Sprintf(" %s  %s\n %s",
		mutedStyle.Render("Effort:"),
		strings.Join(cells, " · "),
		mutedStyle.Render(info))
}

// renderList renders the model rows, highlighting the cursor.
func (m Model) renderList() string {
	var b strings.Builder
	for i, mcap := range m.filtered {
		if i == m.cursor {
			b.WriteString(accentStyle.Render("► " + mcap.ModelID))
			b.WriteString(mutedStyle.Render(fmt.Sprintf("  %s · %s", ctxBadge(mcap), mcap.Provider)))
		} else {
			b.WriteString(mutedStyle.Render("  " + mcap.ModelID))
			b.WriteString(mutedStyle.Render(fmt.Sprintf("  %s · %s", ctxBadge(mcap), mcap.Provider)))
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ctxBadge renders the context-window badge, or the model family tag when no
// window is known.
func ctxBadge(m capability.ModelCapabilities) string {
	if m.ContextWindow > 0 {
		return capability.FormatTokens(m.ContextWindow)
	}
	if m.MaxOutputTokens > 0 {
		return "out " + capability.FormatTokens(m.MaxOutputTokens)
	}
	return "—"
}
