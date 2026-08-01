package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/runtime"
)

// Fixed footer styles
var (
	footerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderBottom(false).
				BorderLeft(false).
				BorderRight(false).
				BorderForeground(lipgloss.Color(colorSubtle))
	budgetLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorMuted))
	budgetValueStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorText))
	budgetExhaustedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorRed))
	notificationStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorOrange))
	notificationCriticalStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color(colorRed))
)

// renderFixedFooter renders the anchored bottom bar: MutationBudget usage
// counters (Files, Diffs, Attempts) and system notification status. Reads
// counters directly from RuntimeContext. Never stores or caches.
func renderFixedFooter(runtimeCtx *runtime.RuntimeContext, notification string, width int) string {
	if runtimeCtx == nil || width < 20 {
		return ""
	}

	var b strings.Builder

	// ── Budget counters ──────────────────────────────────────
	if runtimeCtx.Budget != nil {
		b.WriteString(renderBudgetCounters(runtimeCtx.Budget))
	}

	// ── Notification ──────────────────────────────────────────
	if notification != "" {
		if b.Len() > 0 {
			b.WriteString("  ")
		}
		isCritical := strings.Contains(strings.ToLower(notification), "error") ||
			strings.Contains(strings.ToLower(notification), "failed") ||
			strings.Contains(strings.ToLower(notification), "exhausted")
		if isCritical {
			b.WriteString(notificationCriticalStyle.Render("! " + notification))
		} else {
			b.WriteString(notificationStyle.Render(notification))
		}
	}

	if b.Len() == 0 {
		return ""
	}

	return footerBorderStyle.Width(width).Render(strings.TrimSuffix(b.String(), "\n"))
}

// renderBudgetCounters renders the MutationBudget usage as a single compact line.
// Current values are derived as max - remaining (budget exposes only remaining
// and max as public fields; current is computed).
func renderBudgetCounters(bgt *budget.MutationBudget) string {
	type counter struct {
		label string
		max   int
		cur   int
	}
	counters := []counter{
		{"Files", bgt.MaxFiles, bgt.MaxFiles - bgt.RemainingFiles()},
		{"Diffs", bgt.MaxDiffLines, bgt.MaxDiffLines - bgt.RemainingDiffLines()},
		{"Attempts", bgt.MaxAttempts, bgt.MaxAttempts - bgt.RemainingAttempts()},
	}

	var parts []string
	for _, c := range counters {
		if c.max <= 0 {
			continue
		}
		valStyle := budgetValueStyle
		if c.cur >= c.max {
			valStyle = budgetExhaustedStyle
		}
		part := fmt.Sprintf("%s %s/%s",
			budgetLabelStyle.Render(c.label),
			valStyle.Render(fmt.Sprintf("%d", c.cur)),
			budgetValueStyle.Render(fmt.Sprintf("%d", c.max)),
		)
		parts = append(parts, part)
	}

	return strings.Join(parts, "  ")
}
