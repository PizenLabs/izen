package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/ui/status"
)

// Fixed footer styles
var (
	footerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderBottom(false).
				BorderLeft(false).
				BorderRight(false).
				BorderForeground(lipgloss.Color(colorSubtle))
	notificationStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorOrange))
	notificationCriticalStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color(colorRed))

	// ── Compact telemetry styles (Catppuccin Mocha) ──────────────
	// Threshold-based counter colours per the footer telemetry spec:
	//   · 0/low usage    → Subtext/Muted #6c7086
	//   · > 70% usage    → Yellow       #f9e2af
	//   · > 90% usage    → Red          #f38ba8
	telemetryLowStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	telemetryAlertStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f9e2af"))
	telemetryDangerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f38ba8"))
	telemetryMaxStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
)

// Telemetry glyphs (Nerd Fonts). The codebase already ships Nerd Font glyphs in
// the shared Icon set; these are the dedicated footer counter icons.
const (
	telemetryFileIcon    = "\U000F0214" // 󰈔 nf-cod-files
	telemetryDiffIcon    = "\U000F03EB" // 󰏫 nf-cod-diff
	telemetryAttemptIcon = "\U000F046E" // 󰑮 nf-cod-references
)

// telemetryThresholds: usage percentage at which a counter escalates colour.
const (
	telemetryAlertThreshold  = 0.7 // > 70% → yellow
	telemetryDangerThreshold = 0.9 // > 90% → red
)

// renderFixedFooter renders the anchored bottom bar: MutationBudget usage
// counters (Files, Diffs, Attempts) and system notification status. Reads
// counters directly from RuntimeContext. Never stores or caches.
//
// Contextual visibility: under a read-only mode (ask/plan/review) with zero
// budget consumption the full telemetry collapses to a single subtle indicator,
// so a pure Q&A session never clutters the footer with idle mutation counters.
// The notification toast is clipped to the width left over from the telemetry
// so the two regions can never overlap.
func renderFixedFooter(runtimeCtx *runtime.RuntimeContext, mode modes.Mode, notification string, width int) string {
	if runtimeCtx == nil || width < 20 {
		return ""
	}

	var b strings.Builder

	// ── Budget counters ──────────────────────────────────────
	if runtimeCtx.Budget != nil {
		if telemetry := renderBudgetCounters(runtimeCtx.Budget, mode); telemetry != "" {
			b.WriteString(telemetry)
		}
	}

	// ── Notification (clipped to the remaining width) ──────────
	if notification != "" {
		sep := ""
		if b.Len() > 0 {
			sep = "  "
		}
		isCritical := strings.Contains(strings.ToLower(notification), "error") ||
			strings.Contains(strings.ToLower(notification), "failed") ||
			strings.Contains(strings.ToLower(notification), "exhausted")
		var notif string
		if isCritical {
			notif = notificationCriticalStyle.Render("! " + notification)
		} else {
			notif = notificationStyle.Render(notification)
		}
		// Reserve the telemetry region + separator; the toast clips instead of
		// overlapping the counters.
		used := lipgloss.Width(b.String()) + lipgloss.Width(sep)
		avail := width - used
		if avail < 4 {
			avail = 4
		}
		notif = ansi.Truncate(notif, avail, "…")
		b.WriteString(sep)
		b.WriteString(notif)
	}

	if b.Len() == 0 {
		return ""
	}

	return footerBorderStyle.Width(width).Render(strings.TrimSuffix(b.String(), "\n"))
}

// renderBudgetCounters renders the MutationBudget usage as a single compact
// line of icon + current/max counters (e.g. "󰈔 0/10  󰏫 0/5.0k  󰑮 0/3").
// Current values are derived as max - remaining (budget exposes only remaining
// and max as public fields; current is computed). Thousands are abbreviated
// with 'k' (e.g. 5000 → 5.0k). Each counter is coloured by usage threshold:
// muted at 0/low, yellow above 70%, red above 90%.
func renderBudgetCounters(bgt *budget.MutationBudget, mode modes.Mode) string {
	type counter struct {
		icon string
		max  int
		cur  int
	}
	counters := []counter{
		{telemetryFileIcon, bgt.MaxFiles, bgt.MaxFiles - bgt.RemainingFiles()},
		{telemetryDiffIcon, bgt.MaxDiffLines, bgt.MaxDiffLines - bgt.RemainingDiffLines()},
		{telemetryAttemptIcon, bgt.MaxAttempts, bgt.MaxAttempts - bgt.RemainingAttempts()},
	}

	totalCur, totalMax := 0, 0
	for _, c := range counters {
		if c.max <= 0 {
			continue
		}
		totalCur += c.cur
		totalMax += c.max
	}
	if totalMax == 0 {
		return ""
	}

	// Contextual visibility: read-only mode with zero consumption hides the
	// counters in favour of a single subtle indicator.
	if mode.ReadOnly() && totalCur == 0 {
		return telemetryLowStyle.Render("·")
	}

	var parts []string
	for _, c := range counters {
		if c.max <= 0 {
			continue
		}
		pct := float64(c.cur) / float64(c.max)
		valStyle := telemetryStyleForUsage(pct)
		part := fmt.Sprintf("%s %s/%s",
			telemetryLowStyle.Render(c.icon),
			valStyle.Render(status.FormatTokens(c.cur)),
			telemetryMaxStyle.Render(status.FormatTokens(c.max)),
		)
		parts = append(parts, part)
	}
	return strings.Join(parts, "  ")
}

// telemetryStyleForUsage picks the Catppuccin counter colour for a usage ratio.
func telemetryStyleForUsage(pct float64) lipgloss.Style {
	switch {
	case pct > telemetryDangerThreshold:
		return telemetryDangerStyle
	case pct > telemetryAlertThreshold:
		return telemetryAlertStyle
	default:
		return telemetryLowStyle
	}
}
