package model_picker

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// View implements tea.Model. Pure view: renders the search line, the model
// list with inline color-coded badges ([DEFAULT]/[PLAN]/[SMOL]/[VISION-role]/
// [ADVISER] plus classifier [THINKING]/[VISION]), the provider-native
// reasoning effort bar, and the hotkey footer. It never blocks on fetch:
// an empty snapshot renders "no models loaded", never a fullscreen modal.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Model Picker "))
	b.WriteString("\n\n")

	if m.err != nil {
		b.WriteString(errStyle.Render(" cache load failed: " + m.err.Error()))
		b.WriteString("\n")
	}
	models := m.snapshotModels()
	if len(models) == 0 {
		b.WriteString(mutedStyle.Render(" no models loaded "))
		return b.String()
	}

	focus := "search"
	if !m.searchFocused {
		focus = "list"
	}
	scope := "local"
	if m.isGlobal {
		scope = "global"
	}
	b.WriteString(mutedStyle.Render(fmt.Sprintf(" focus:%s scope:%s  query:%q  %d/%d models", focus, scope, m.query, len(m.filtered), len(models))))
	b.WriteString("\n\n")

	if len(m.filtered) == 0 {
		b.WriteString(mutedStyle.Render(" no models match the filter "))
		b.WriteString("\n")
	} else {
		b.WriteString(m.renderList())
		b.WriteString("\n")
	}

	b.WriteString(m.renderReasoningSection())
	b.WriteString("\n")

	if m.status != "" {
		b.WriteString(mutedStyle.Render(" " + m.status))
		b.WriteString("\n")
	}
	footer := "type to filter  tab focus  ↑/↓ navigate  ←/→ effort  d/p/s/v/a bind default/plan/smol/vision/adviser  g scope  enter select"
	b.WriteString(mutedStyle.Render(footer))
	return b.String()
}

// snapshotModels reads the immutable snapshot RAM slice (no copy on the hot
// render path; callers must not mutate).
func (m Model) snapshotModels() []registry.ModelDescriptor {
	if m.snap == nil {
		return nil
	}
	return m.snap.Models
}

func (m Model) renderList() string {
	var b strings.Builder
	limit := len(m.filtered)
	if m.height > 0 && limit > m.height-8 {
		limit = m.height - 8
		if limit < 1 {
			limit = 1
		}
	}
	for i := 0; i < limit; i++ {
		d := m.filtered[i]
		badges := renderBadges(BadgesFor(d, m.roles))
		line := fmt.Sprintf("  %s  %s %s", d.ID, mutedStyle.Render(d.Provider), badges)
		if i == m.cursor {
			line = accentStyle.Render("► "+d.ID) + "  " + mutedStyle.Render(d.Provider) + " " + badges
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(m.filtered) > limit {
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" … %d more", len(m.filtered)-limit)))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderReasoningSection renders the provider-native reasoning bar for the
// highlighted model (no universal low/medium/high forcing).
func (m Model) renderReasoningSection() string {
	hl := m.Highlighted()
	if hl == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(mutedStyle.Render(" reasoning: "))
	b.WriteString(m.RenderReasoningBar())
	return b.String()
}

func renderBadges(badges []string) string {
	if len(badges) == 0 {
		return ""
	}
	cells := make([]string, 0, len(badges))
	for _, badge := range badges {
		cells = append(cells, styleBadge(badge))
	}
	return strings.Join(cells, " ")
}

func styleBadge(badge string) string {
	switch badge {
	case "[DEFAULT]":
		return defaultBadge.Render(badge)
	case "[PLAN]":
		return planBadge.Render(badge)
	case "[THINKING]":
		return thinkBadge.Render(badge)
	case "[VISION]":
		return visionBadge.Render(badge)
	default:
		return otherBadge.Render(badge)
	}
}
