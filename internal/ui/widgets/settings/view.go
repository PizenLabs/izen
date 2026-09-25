package settings

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	settingsTitleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	settingsTabStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	settingsActiveTab        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	settingsValueStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	settingsMutedStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	settingsStatusStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	settingsRowStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))
	settingsActiveRow        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	settingsDescriptionStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#bac2de")).
					Border(lipgloss.RoundedBorder()).
					BorderForeground(lipgloss.Color("#585b70")).
					Padding(0, 1)
)

var settingLabels = [...]string{
	"Response Style",
	"Hide Thinking Blocks",
	"Auto-Scroll Behavior",
}

var settingTabs = [...]string{
	"Response",
	"Visibility",
	"Viewport",
}

// View renders the reduced settings schema. It intentionally presents no
// provider, model, variant, role, credential, authorization, or execution
// controls; those belong to other surfaces.
func (m Model) View() string {
	m.normalizeActive()
	width := max(1, m.innerWidth)
	if m.innerWidth == 0 {
		width = max(1, m.width-settingsModalHorizontalChrome)
	}
	height := m.innerHeight
	if height == 0 {
		height = max(0, m.height-settingsModalVerticalChrome)
	}

	upper := make([]string, 0, 10)
	if height >= 14 {
		upper = append(upper,
			settingsTitleStyle.Render("IZEN SETTINGS"),
			settingsMutedStyle.Width(width).Render("Response, visibility, and viewport preferences"),
			"",
		)
	} else {
		// Small split panes keep the controls and contextual footer visible by
		// dropping optional title prose instead of clipping the bottom of the
		// modal.
		upper = append(upper, settingsTitleStyle.Render("IZEN SETTINGS"))
	}
	upper = append(upper, m.renderTabs())

	if height >= 12 {
		upper = append(upper, "")
	}
	for _, index := range settingRowsByTab[m.activeTab] {
		upper = append(upper, m.renderRow(index, m.displayValue(index), width))
	}
	if m.status != "" {
		upper = append(upper, settingsStatusStyle.Width(width).Render(m.status))
	}

	// Keep the description box at the actual bottom of the inner canvas. The
	// host adds the outer border, so padding here (rather than after the box)
	// prevents a large empty region from appearing below the contextual help.
	footer := []string{
		settingsMutedStyle.Width(width).Render("Tab/Shift+Tab pane · ↑/↓ row · Space/Enter change · Esc close"),
		"",
		m.renderDescription(width),
	}
	upperHeight := lipgloss.Height(strings.Join(upper, "\n"))
	footerHeight := lipgloss.Height(strings.Join(footer, "\n"))
	if height > 0 && upperHeight+footerHeight > height {
		// At very small split-pane heights, retain the tab and focused row
		// but give the description the remaining canvas. The bounded
		// renderer keeps complete boxes intact when there is room and
		// otherwise falls back to readable text instead of a half-box.
		upper = []string{m.renderTabs()}
		for _, index := range settingRowsByTab[m.activeTab] {
			upper = append(upper, m.renderRow(index, m.displayValue(index), width))
		}
		upperHeight = lipgloss.Height(strings.Join(upper, "\n"))
		available := max(0, height-upperHeight)
		if available < 3 {
			// A two-line plain explanation is preferable to removing the
			// focused setting altogether. If only one line remains, keep the
			// focused row and let the description occupy the other line.
			if available == 1 && height >= 3 && upperHeight > 1 {
				focused := make([]string, 0, len(settingRowsByTab[m.activeTab]))
				for _, index := range settingRowsByTab[m.activeTab] {
					focused = append(focused, m.renderRow(index, m.displayValue(index), width))
				}
				upper = focused
				upperHeight = lipgloss.Height(strings.Join(upper, "\n"))
				available = max(0, height-upperHeight)
			} else if available == 0 || height < 3 {
				upper = nil
				upperHeight = 0
				available = height
			}
		}
		footer = []string{m.renderDescriptionBounded(width, available)}
		footerHeight = lipgloss.Height(strings.Join(footer, "\n"))
	}
	if height > upperHeight+footerHeight {
		upper = append(upper, make([]string, height-upperHeight-footerHeight)...)
	}
	upper = append(upper, footer...)
	return m.fitHeight(strings.Join(upper, "\n"))
}

func (m Model) displayValue(index int) string {
	switch index {
	case 1:
		return displayBool(m.values.HideThinking)
	case 2:
		return displayAutoScroll(m.values.AutoScroll)
	default:
		return displayResponseStyle(m.values.ResponseStyle)
	}
}

func (m Model) renderTabs() string {
	tabs := make([]string, 0, len(settingTabs))
	for i, label := range settingTabs {
		style := settingsTabStyle
		if i == m.activeTab {
			style = settingsActiveTab
		}
		if i > 0 {
			tabs = append(tabs, settingsMutedStyle.Render("  "))
		}
		tabs = append(tabs, style.Render("["+label+"]"))
	}
	return strings.Join(tabs, "")
}

func (m Model) renderRow(index int, value string, width int) string {
	label := settingLabels[index]
	marker := settingsMutedStyle.Render("  ")
	style := settingsRowStyle
	if index == m.activeRow {
		marker = settingsActiveRow.Render("> ")
		style = settingsActiveRow
	}
	plain := marker + style.Render(label)
	// Keep the value column stable without relying on byte lengths after ANSI
	// styling. Padding is calculated from the unstyled label.
	labelWidth := lipgloss.Width(label)
	gap := width - labelWidth - lipgloss.Width(value) - 6
	if gap < 2 {
		gap = 2
	}
	return plain + strings.Repeat(" ", gap) + settingsValueStyle.Render(value)
}

func (m Model) renderDescription(width int) string {
	return m.renderDescriptionBounded(width, 0)
}

func (m Model) renderDescriptionBounded(width, maxHeight int) string {
	index := m.activeRow
	if index < 0 || index >= len(settingDescriptions) {
		index = 0
	}
	description := settingDescriptions[index]
	if maxHeight > 0 && maxHeight < 3 {
		// There is not enough room for a complete bordered box. A clipped
		// top border is less useful than a bounded, readable explanation.
		return settingsMutedStyle.Width(width).MaxHeight(maxHeight).Render(description)
	}

	// Width includes the style's horizontal padding; its border adds two
	// more visible cells. Subtracting those border cells keeps the footer
	// inside the modal's strict content width.
	boxWidth := max(1, width-2)
	contentStyle := lipgloss.NewStyle().Width(boxWidth)
	if maxHeight > 0 {
		contentStyle = contentStyle.MaxHeight(maxHeight - 2)
	}
	content := contentStyle.Render(description)
	return settingsDescriptionStyle.Width(boxWidth).Render(content)
}

func (m Model) fitHeight(content string) string {
	lines := strings.Split(content, "\n")
	height := m.innerHeight
	if height == 0 {
		height = max(0, m.height-settingsModalVerticalChrome)
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
